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
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/exmime"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"github.com/slack-go/slack"
	"go.mau.fi/mautrix-slack/database"
)

type ConvertedMessagePart struct {
	PartID  database.PartID
	Type    event.Type
	Content *event.MessageEventContent
	Extra   map[string]any
}

type ConvertedMessage struct {
	Parts []*ConvertedMessagePart
}

func isMediaMsgtype(msgType event.MessageType) bool {
	return msgType == event.MsgImage || msgType == event.MsgAudio || msgType == event.MsgVideo || msgType == event.MsgFile
}

func (cm *ConvertedMessage) MergeCaption() {
	if len(cm.Parts) != 2 {
		return
	}
	textPart := cm.Parts[0]
	imagePart := cm.Parts[1]
	if imagePart.Content.MsgType == event.MsgText {
		textPart, imagePart = imagePart, textPart
	}
	if textPart.PartID.String() != "" || textPart.Content.MsgType != event.MsgText || !isMediaMsgtype(imagePart.Content.MsgType) {
		return
	}
	imagePart.Content.FileName = imagePart.Content.Body
	imagePart.Content.Body = textPart.Content.Body
	imagePart.Content.Format = textPart.Content.Format
	imagePart.Content.FormattedBody = textPart.Content.FormattedBody
	maps.Copy(imagePart.Extra, textPart.Extra)
	imagePart.PartID = textPart.PartID
	cm.Parts = []*ConvertedMessagePart{imagePart}
}

func (mc *MessageConverter) ToMatrix(ctx context.Context, msg *slack.Msg) *ConvertedMessage {
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
	output := &ConvertedMessage{}
	var textPart *ConvertedMessagePart
	if len(msg.Blocks.BlockSet) != 0 || len(msg.Attachments) != 0 {
		textPart = mc.trySlackBlocksToMatrix(ctx, msg.Blocks, msg.Attachments)
	} else if text != "" {
		textPart = mc.slackTextToMatrix(ctx, text)
	}
	if textPart != nil {
		switch msg.SubType {
		case slack.MsgSubTypeMeMessage:
			textPart.Content.MsgType = event.MsgEmote
		case "huddle_thread":
			data := mc.GetData(ctx)
			textPart.Content.EnsureHasHTML()
			textPart.Content.Body += fmt.Sprintf("\n\nJoin via the Slack app: https://app.slack.com/client/%s/%s", data.TeamID, data.ChannelID)
			textPart.Content.FormattedBody += fmt.Sprintf(`<p><a href="https://app.slack.com/client/%s/%s">Click here to join via the Slack app</a></p>`, data.TeamID, data.ChannelID)
		}
		output.Parts = append(output.Parts, textPart)
	}
	for i, file := range msg.Files {
		partID := database.PartID{
			Type:  database.PartTypeFile,
			Index: i,
			ID:    file.ID,
		}
		output.Parts = append(output.Parts, mc.slackFileToMatrix(ctx, partID, &file))
	}
	return output
}

func (mc *MessageConverter) slackTextToMatrix(ctx context.Context, text string) *ConvertedMessagePart {
	content := format.HTMLToContent(mc.mrkdwnToMatrixHtml(ctx, text))
	return &ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: &content,
	}
}

func makeErrorMessage(partID database.PartID, message string, args ...any) *ConvertedMessagePart {
	if len(args) > 0 {
		message = fmt.Sprintf(message, args...)
	}
	return &ConvertedMessagePart{
		PartID: partID,
		Type:   event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    message,
		},
	}
}

func (mc *MessageConverter) slackFileToMatrix(ctx context.Context, partID database.PartID, file *slack.File) *ConvertedMessagePart {
	log := zerolog.Ctx(ctx).With().Str("file_id", file.ID).Logger()
	if file.FileAccess == "check_file_info" {
		connectFile, _, _, err := mc.GetClient(ctx).GetFileInfoContext(ctx, file.ID, 0, 0)
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
		err = mc.GetClient(ctx).GetFileContext(ctx, url, &data)
		if bytes.HasPrefix(data.Bytes(), []byte("<!DOCTYPE html>")) {
			log.Warn().Msg("Received HTML file from Slack, retrying in 5 seconds")
			time.Sleep(5 * time.Second)
			data.Reset()
			err = mc.GetClient(ctx).GetFileContext(ctx, file.URLPrivate, &data)
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
	err = mc.uploadMedia(ctx, data.Bytes(), &content)
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
	return &ConvertedMessagePart{
		PartID:  partID,
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
