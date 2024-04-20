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
	"strings"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/slack-go/slack"

	"go.mau.fi/mautrix-slack/database"
)

var (
	ErrUnexpectedParsedContentType = errors.New("unexpected parsed content type")
	ErrUnknownMsgType              = errors.New("unknown msgtype")
	ErrMediaDownloadFailed         = errors.New("failed to download media")
	ErrMediaOnlyEditCaption        = errors.New("only media message caption can be edited")
	ErrEditTargetNotFound          = errors.New("edit target message not found")
	ErrThreadRootNotFound          = errors.New("thread root message not found")
)

func (mc *MessageConverter) ToSlack(ctx context.Context, evt *event.Event) (sendReq slack.MsgOption, fileUpload *slack.FileUploadParameters, threadRootID string, editTarget *database.Message, err error) {
	log := zerolog.Ctx(ctx)
	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		return nil, nil, "", nil, ErrUnexpectedParsedContentType
	}

	if evt.Type == event.EventSticker {
		// Slack doesn't have stickers, just bridge stickers as images
		content.MsgType = event.MsgImage
	}

	var editTargetID string
	if replaceEventID := content.RelatesTo.GetReplaceID(); replaceEventID != "" {
		existing, err := mc.GetMessageInfo(ctx, replaceEventID)
		if err != nil {
			log.Err(err).Msg("Failed to get edit target message")
			return nil, nil, "", nil, fmt.Errorf("failed to get edit target message: %w", err)
		} else if existing == nil {
			return nil, nil, "", nil, ErrEditTargetNotFound
		} else {
			editTargetID = existing.MessageID
			editTarget = existing
			if content.NewContent != nil {
				content = content.NewContent
			}
		}
	} else {
		var threadMXID id.EventID
		threadMXID = content.RelatesTo.GetThreadParent()
		if threadMXID == "" {
			threadMXID = content.RelatesTo.GetReplyTo()
		}
		if threadMXID != "" {
			rootMessage, err := mc.GetMessageInfo(ctx, threadMXID)
			if err != nil {
				return nil, nil, "", nil, fmt.Errorf("failed to get thread root message: %w", err)
			} else if rootMessage == nil {
				return nil, nil, "", nil, ErrThreadRootNotFound
			} else if rootMessage.ThreadID != "" {
				threadRootID = rootMessage.ThreadID
			} else {
				threadRootID = rootMessage.MessageID
			}
		}
	}

	if editTargetID != "" && isMediaMsgtype(content.MsgType) {
		content.MsgType = event.MsgText
		if content.FileName == "" || content.FileName == content.Body {
			return nil, nil, "", editTarget, ErrMediaOnlyEditCaption
		}
	}

	switch content.MsgType {
	case event.MsgText, event.MsgEmote, event.MsgNotice:
		options := make([]slack.MsgOption, 0, 4)
		if content.Format == event.FormatHTML {
			options = append(options, slack.MsgOptionText(mc.MatrixHTMLParser.Parse(ctx, content.FormattedBody), false))
		} else {
			options = append(options,
				slack.MsgOptionText(content.Body, false),
				slack.MsgOptionDisableMarkdown())
		}
		if editTargetID != "" {
			options = append(options, slack.MsgOptionUpdate(editTargetID))
		} else if threadRootID != "" {
			options = append(options, slack.MsgOptionTS(threadRootID))
		}
		if content.MsgType == event.MsgEmote {
			options = append(options, slack.MsgOptionMeMessage())
		}
		return slack.MsgOptionCompose(options...), nil, threadRootID, editTarget, nil
	case event.MsgAudio, event.MsgFile, event.MsgImage, event.MsgVideo:
		data, err := mc.downloadMatrixAttachment(ctx, content)
		if err != nil {
			log.Err(err).Msg("Failed to download Matrix attachment")
			return nil, nil, "", editTarget, ErrMediaDownloadFailed
		}

		var filename, caption string
		if content.FileName == "" || content.FileName == content.Body {
			filename = content.Body
		} else {
			filename = content.FileName
			caption = content.Body
		}
		fileUpload = &slack.FileUploadParameters{
			Filename:        filename,
			Filetype:        content.Info.MimeType,
			Reader:          bytes.NewReader(data),
			Channels:        []string{mc.GetData(ctx).ChannelID},
			ThreadTimestamp: threadRootID,
		}
		if caption != "" {
			fileUpload.InitialComment = caption
		}
		return nil, fileUpload, threadRootID, editTarget, nil
	default:
		return nil, nil, "", editTarget, ErrUnknownMsgType
	}
}

func (mc *MessageConverter) uploadMedia(ctx context.Context, data []byte, content *event.MessageEventContent) error {
	content.Info.Size = len(data)
	if content.Info.Width == 0 && content.Info.Height == 0 && strings.HasPrefix(content.Info.MimeType, "image/") {
		cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
		content.Info.Width, content.Info.Height = cfg.Width, cfg.Height
	}

	uploadMimeType, file := mc.encryptFileInPlace(ctx, data, content.Info.MimeType)
	fileName := ""
	mxc, err := mc.UploadMatrixMedia(ctx, data, fileName, uploadMimeType)
	if err != nil {
		return err
	}

	if file != nil {
		file.URL = mxc
		content.File = file
	} else {
		content.URL = mxc
	}

	return nil
}

func (mc *MessageConverter) encryptFileInPlace(ctx context.Context, data []byte, mimeType string) (string, *event.EncryptedFileInfo) {
	if !mc.GetData(ctx).Encrypted {
		return mimeType, nil
	}

	file := &event.EncryptedFileInfo{
		EncryptedFile: *attachment.NewEncryptedFile(),
		URL:           "",
	}
	file.EncryptInPlace(data)
	return "application/octet-stream", file
}

func (mc *MessageConverter) downloadMatrixAttachment(ctx context.Context, content *event.MessageEventContent) ([]byte, error) {
	var file *event.EncryptedFileInfo
	rawMXC := content.URL

	if content.File != nil {
		file = content.File
		rawMXC = file.URL
	}

	data, err := mc.DownloadMatrixMedia(ctx, rawMXC)
	if err != nil {
		return nil, err
	}

	if file != nil {
		err = file.DecryptInPlace(data)
		if err != nil {
			return nil, err
		}
	}

	return data, nil
}
