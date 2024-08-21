// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package msgconv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"
	"go.mau.fi/util/exmime"
	"go.mau.fi/util/ffmpeg"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"go.mau.fi/mautrix-slack/pkg/slackid"
)

func (mc *MessageConverter) ToMatrix(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	source *bridgev2.UserLogin,
	msg *slack.Msg,
) *bridgev2.ConvertedMessage {
	ctx = context.WithValue(ctx, contextKeyPortal, portal)
	ctx = context.WithValue(ctx, contextKeySource, source)
	client := source.Client.(SlackClientProvider).GetClient()
	output := &bridgev2.ConvertedMessage{}
	if msg.ThreadTimestamp != "" && msg.ThreadTimestamp != msg.Timestamp {
		teamID, channelID := slackid.ParsePortalID(portal.ID)
		output.ThreadRoot = ptr.Ptr(slackid.MakeMessageID(teamID, channelID, msg.ThreadTimestamp))
	}
	textPart := mc.makeTextPart(ctx, msg, portal, intent)
	if textPart != nil {
		output.Parts = append(output.Parts, textPart)
	}
	for i, file := range msg.Files {
		// mode=tombstone seems to mean the file was deleted
		if file.Mode == "tombstone" {
			continue
		}
		partID := slackid.MakePartID(slackid.PartTypeFile, i, file.ID)
		output.Parts = append(output.Parts, mc.slackFileToMatrix(ctx, portal, intent, client, partID, &file))
	}
	for i, att := range msg.Attachments {
		if !isImageAttachment(&att) {
			continue
		}
		part, err := mc.renderImageBlock(ctx, portal, intent, att.Blocks.BlockSet[0].(*slack.ImageBlock).ImageURL)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to render image block")
		} else {
			part.ID = slackid.MakePartID(slackid.PartTypeAttachment, len(msg.Files)+i, strconv.Itoa(att.ID))
			output.Parts = append(output.Parts, part)
		}
	}
	if output.MergeCaption() {
		output.Parts[0].DBMetadata = &slackid.MessageMetadata{
			CaptionMerged: true,
		}
	}
	if msg.Username != "" {
		for _, part := range output.Parts {
			// TODO reupload avatar
			part.Content.BeeperPerMessageProfile = &event.BeeperPerMessageProfile{
				ID:          msg.Username,
				Displayname: msg.Username,
			}
		}
	}
	return output
}

func (mc *MessageConverter) EditToMatrix(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	source *bridgev2.UserLogin,
	msg *slack.Msg,
	origMsg *slack.Msg,
	existing []*database.Message,
) *bridgev2.ConvertedEdit {
	ctx = context.WithValue(ctx, contextKeyPortal, portal)
	ctx = context.WithValue(ctx, contextKeySource, source)
	client := source.Client.(SlackClientProvider).GetClient()
	output := &bridgev2.ConvertedEdit{}
	existingMap := make(map[networkid.PartID]*database.Message, len(existing))
	for _, part := range existing {
		existingMap[part.PartID] = part
		partType, _, innerPartID, ok := slackid.ParsePartID(part.PartID)
		if ok && partType == slackid.PartTypeAttachment {
			innerPartIDInt, _ := strconv.Atoi(innerPartID)
			attachmentStillExists := slices.ContainsFunc(msg.Attachments, func(attachment slack.Attachment) bool {
				return attachment.ID == innerPartIDInt
			})
			if !attachmentStillExists {
				output.DeletedParts = append(output.DeletedParts, part)
			}
		}
	}
	editTargetPart := existing[0]
	modifiedPart := mc.makeTextPart(ctx, msg, portal, intent)
	captionMerged := false
	for i, file := range msg.Files {
		partID := slackid.MakePartID(slackid.PartTypeFile, i, file.ID)
		existingPart, ok := existingMap[partID]
		if file.Mode == "tombstone" {
			if ok {
				output.DeletedParts = append(output.DeletedParts, existingPart)
			}
		} else {
			// For edits where there's either only one media part, or there was no text part,
			// we'll need to fetch the first media part to merge it in
			if !captionMerged && modifiedPart != nil && (len(msg.Files) == 1 || editTargetPart.PartID != "") {
				if editTargetPart.PartID != "" {
					editTargetPart = existingPart
				}
				filePart := mc.slackFileToMatrix(ctx, portal, intent, client, partID, &file)
				modifiedPart = bridgev2.MergeCaption(modifiedPart, filePart)
				modifiedPart.DBMetadata = &slackid.MessageMetadata{
					CaptionMerged: true,
				}
				captionMerged = true
			}
		}
	}
	if msg.Username != "" {
		modifiedPart.Content.BeeperPerMessageProfile = &event.BeeperPerMessageProfile{
			ID:          msg.Username,
			Displayname: msg.Username,
		}
	}
	// TODO this doesn't handle edits to captions in msg.Attachments gifs properly
	if modifiedPart != nil {
		output.ModifiedParts = append(output.ModifiedParts, modifiedPart.ToEditPart(editTargetPart))
	}
	return output
}

func (mc *MessageConverter) makeTextPart(ctx context.Context, msg *slack.Msg, portal *bridgev2.Portal, intent bridgev2.MatrixAPI) *bridgev2.ConvertedMessagePart {
	var text string
	if msg.Text != "" {
		text = msg.Text
	}
	for _, attachment := range msg.Attachments {
		if text != "" {
			text += "\n"
		}
		if attachment.Text != "" {
			text += attachment.Text
		} else if attachment.Fallback != "" {
			text += attachment.Fallback
		}
	}
	var textPart *bridgev2.ConvertedMessagePart
	if len(msg.Blocks.BlockSet) != 0 || len(msg.Attachments) != 0 {
		textPart = mc.trySlackBlocksToMatrix(ctx, portal, intent, msg.Blocks, msg.Attachments)
	} else if text != "" {
		textPart = mc.slackTextToMatrix(ctx, text)
	}
	if textPart != nil {
		switch msg.SubType {
		case slack.MsgSubTypeMeMessage:
			textPart.Content.MsgType = event.MsgEmote
		case "huddle_thread":
			teamID, channelID := slackid.ParsePortalID(portal.ID)
			textPart.Content.EnsureHasHTML()
			textPart.Content.Body += fmt.Sprintf("\n\nJoin via the Slack app: https://app.slack.com/client/%s/%s", teamID, channelID)
			textPart.Content.FormattedBody += fmt.Sprintf(`<p><a href="https://app.slack.com/client/%s/%s">Click here to join via the Slack app</a></p>`, teamID, channelID)
		}
	}
	return textPart
}

func (mc *MessageConverter) slackTextToMatrix(ctx context.Context, text string) *bridgev2.ConvertedMessagePart {
	mentions := &event.Mentions{}
	content := format.HTMLToContent(mc.mrkdwnToMatrixHtml(ctx, text, mentions))
	content.Mentions = mentions
	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: &content,
	}
}

func makeErrorMessage(partID networkid.PartID, message string, args ...any) *bridgev2.ConvertedMessagePart {
	if len(args) > 0 {
		message = fmt.Sprintf(message, args...)
	}
	return &bridgev2.ConvertedMessagePart{
		ID:   partID,
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    message,
		},
	}
}

type doctypeCheckingWriteProxy struct {
	io.Writer
	isStart bool
}

var errHTMLFile = errors.New("received HTML file")

func (dtwp *doctypeCheckingWriteProxy) Write(p []byte) (n int, err error) {
	if dtwp.isStart && bytes.HasPrefix(p, []byte("<!DOCTYPE html>")) {
		return 0, errHTMLFile
	}
	dtwp.isStart = false
	return dtwp.Writer.Write(p)
}

func (mc *MessageConverter) slackFileToMatrix(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, client *slack.Client, partID networkid.PartID, file *slack.File) *bridgev2.ConvertedMessagePart {
	log := zerolog.Ctx(ctx).With().Str("file_id", file.ID).Logger()
	if file.FileAccess == "check_file_info" {
		connectFile, _, _, err := client.GetFileInfoContext(ctx, file.ID, 0, 0)
		if err != nil || connectFile == nil {
			log.Err(err).Str("file_id", file.ID).Msg("Failed to fetch slack connect file info")
			return makeErrorMessage(partID, "Failed to fetch Slack Connect file")
		}
		file = connectFile
	}
	if file.Size > mc.MaxFileSize {
		log.Debug().Int("file_size", file.Size).Msg("Dropping too large file")
		return makeErrorMessage(partID, "Too large file (%d MB)", file.Size/1_000_000)
	}
	content := convertSlackFileMetadata(file)
	var url string
	if file.URLPrivateDownload != "" {
		url = file.URLPrivateDownload
	} else if file.URLPrivate != "" {
		url = file.URLPrivate
	}
	if url == "" && file.PermalinkPublic == "" {
		log.Warn().Msg("No usable URL found in file object")
		return makeErrorMessage(partID, "File URL not found")
	}
	convertAudio := file.SubType == "slack_audio" && ffmpeg.Supported()
	needsMediaSize := content.Info.Width == 0 && content.Info.Height == 0 && strings.HasPrefix(content.Info.MimeType, "image/")
	requireFile := convertAudio || needsMediaSize
	var retErr *bridgev2.ConvertedMessagePart
	var uploadErr error
	content.URL, content.File, uploadErr = intent.UploadMediaStream(ctx, portal.MXID, int64(file.Size), requireFile, func(dest io.Writer) (res *bridgev2.FileStreamResult, err error) {
		res = &bridgev2.FileStreamResult{
			ReplacementFile: "",
			FileName:        file.Name,
			MimeType:        content.Info.MimeType,
		}
		if url != "" {
			err = client.GetFileContext(ctx, url, &doctypeCheckingWriteProxy{Writer: dest})
			if errors.Is(err, errHTMLFile) {
				log.Warn().Msg("Received HTML file from Slack, retrying in 5 seconds")
				time.Sleep(5 * time.Second)
				err = client.GetFileContext(ctx, url, dest)
			}
		} else if file.PermalinkPublic != "" {
			var resp *http.Response
			// TODO don't use DefaultClient and use context
			resp, err = http.DefaultClient.Get(file.PermalinkPublic)
			if err == nil {
				_, err = io.Copy(dest, resp.Body)
			}
		}
		if err != nil {
			log.Err(err).Msg("Failed to download file from Slack")
			retErr = makeErrorMessage(partID, "Failed to download file from Slack")
			return
		}
		if convertAudio {
			destFile := dest.(*os.File)
			_ = destFile.Close()
			sourceMime := file.Mimetype
			// Slack claims audio messages are webm/opus, but actually stores mp4/aac?
			if strings.HasSuffix(url, ".mp4") {
				sourceMime = "audio/mp4"
			}
			tempFileWithExt := destFile.Name() + exmime.ExtensionFromMimetype(sourceMime)
			err = os.Rename(destFile.Name(), tempFileWithExt)
			if err != nil {
				log.Err(err).Msg("Failed to rename temp file")
				retErr = makeErrorMessage(partID, "Failed to rename temp file")
				return
			}
			res.ReplacementFile, err = ffmpeg.ConvertPath(ctx, tempFileWithExt, ".ogg", []string{}, []string{"-c:a", "libopus"}, true)
			if err != nil {
				log.Err(err).Msg("Failed to convert voice message")
				retErr = makeErrorMessage(partID, "Failed to convert voice message")
				return
			}
			content.Info.MimeType = "audio/ogg"
			content.Body += ".ogg"
			res.MimeType = "audio/ogg"
			res.FileName += ".ogg"
			if file.AudioWaveSamples == nil {
				file.AudioWaveSamples = []int{}
			}
			for i, val := range file.AudioWaveSamples {
				// Slack's waveforms are in the range 0-100, we need to convert them to 0-256
				file.AudioWaveSamples[i] = min(int(float64(val)*2.56), 256)
			}
			content.MSC1767Audio = &event.MSC1767Audio{
				Duration: content.Info.Duration,
				Waveform: file.AudioWaveSamples,
			}
			content.MSC3245Voice = &event.MSC3245Voice{}
		} else if needsMediaSize {
			destRS := dest.(io.ReadSeeker)
			_, err = destRS.Seek(0, io.SeekStart)
			if err == nil {
				cfg, _, _ := image.DecodeConfig(destRS)
				content.Info.Width, content.Info.Height = cfg.Width, cfg.Height
			}
		}
		return
	})
	if uploadErr != nil {
		if retErr != nil {
			return retErr
		}
		if errors.Is(uploadErr, mautrix.MTooLarge) {
			log.Err(uploadErr).Msg("Homeserver rejected too large file")
		} else if httpErr := (mautrix.HTTPError{}); errors.As(uploadErr, &httpErr) && httpErr.IsStatus(413) {
			log.Err(uploadErr).Msg("Proxy rejected too large file")
		} else {
			log.Err(uploadErr).Msg("Failed to upload file to Matrix")
		}
		return makeErrorMessage(partID, "Failed to transfer file")
	}
	return &bridgev2.ConvertedMessagePart{
		ID:      partID,
		Type:    event.EventMessage,
		Content: &content,
	}
}

func convertSlackFileMetadata(file *slack.File) event.MessageEventContent {
	content := event.MessageEventContent{
		Info: &event.FileInfo{
			MimeType: file.Mimetype,
			Size:     file.Size,
		},
	}
	if file.OriginalW != 0 {
		content.Info.Width = file.OriginalW
	}
	if file.OriginalH != 0 {
		content.Info.Height = file.OriginalH
	}
	if file.DurationMS != 0 {
		content.Info.Duration = file.DurationMS
	}
	if file.Name != "" {
		content.Body = file.Name
	} else {
		mimeClass := strings.Split(file.Mimetype, "/")[0]
		switch mimeClass {
		case "application":
			content.Body = "file"
		default:
			content.Body = mimeClass
		}

		content.Body += exmime.ExtensionFromMimetype(file.Mimetype)
	}

	if strings.HasPrefix(file.Mimetype, "image") {
		content.MsgType = event.MsgImage
	} else if strings.HasPrefix(file.Mimetype, "video") {
		content.MsgType = event.MsgVideo
	} else if strings.HasPrefix(file.Mimetype, "audio") {
		content.MsgType = event.MsgAudio
	} else {
		content.MsgType = event.MsgFile
	}

	return content
}
