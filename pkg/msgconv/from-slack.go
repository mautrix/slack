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
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"
	"go.mau.fi/util/exmime"
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
	existingMap := make(map[networkid.PartID]*database.Message, len(existing))
	for _, part := range existing {
		existingMap[part.PartID] = part
	}
	output := &bridgev2.ConvertedEdit{}
	textPart := mc.makeTextPart(ctx, msg, portal, intent)
	if textPart != nil {
		output.ModifiedParts = append(output.ModifiedParts, &bridgev2.ConvertedEditPart{
			Part:    existingMap[""],
			Type:    textPart.Type,
			Content: textPart.Content,
			Extra:   textPart.Extra,
		})
	}
	for i, file := range msg.Files {
		if file.Mode == "tombstone" {
			partID := slackid.MakePartID(slackid.PartTypeFile, i, file.ID)
			deletedPart, ok := existingMap[partID]
			if ok {
				output.DeletedParts = append(output.DeletedParts, deletedPart)
			}
		}
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
	content := format.HTMLToContent(mc.mrkdwnToMatrixHtml(ctx, text))
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
	var data bytes.Buffer
	var err error
	var url string
	if file.URLPrivateDownload != "" {
		url = file.URLPrivateDownload
	} else if file.URLPrivate != "" {
		url = file.URLPrivate
	}
	if url != "" {
		err = client.GetFileContext(ctx, url, &data)
		if bytes.HasPrefix(data.Bytes(), []byte("<!DOCTYPE html>")) {
			log.Warn().Msg("Received HTML file from Slack, retrying in 5 seconds")
			time.Sleep(5 * time.Second)
			data.Reset()
			err = client.GetFileContext(ctx, file.URLPrivate, &data)
		}
	} else if file.PermalinkPublic != "" {
		var resp *http.Response
		resp, err = http.DefaultClient.Get(file.PermalinkPublic)
		if err == nil {
			_, err = data.ReadFrom(resp.Body)
		}
	} else {
		log.Warn().Msg("No usable URL found in file object")
		return makeErrorMessage(partID, "File URL not found")
	}
	if err != nil {
		log.Err(err).Msg("Failed to download file from Slack")
		return makeErrorMessage(partID, "Failed to download file from Slack")
	}
	err = mc.uploadMedia(ctx, portal, intent, data.Bytes(), &content)
	if err != nil {
		if errors.Is(err, mautrix.MTooLarge) {
			log.Err(err).Msg("Homeserver rejected too large file")
		} else if httpErr := (mautrix.HTTPError{}); errors.As(err, &httpErr) && httpErr.IsStatus(413) {
			log.Err(err).Msg("Proxy rejected too large file")
		} else {
			log.Err(err).Msg("Failed to upload file to Matrix")
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
