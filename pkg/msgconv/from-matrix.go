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
	"github.com/slack-go/slack"
	"go.mau.fi/util/ffmpeg"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-slack/pkg/slackid"
)

var (
	ErrUnknownMsgType       = errors.New("unknown msgtype")
	ErrMediaDownloadFailed  = errors.New("failed to download media")
	ErrMediaUploadFailed    = errors.New("failed to reupload media")
	ErrMediaConvertFailed   = errors.New("failed to re-encode media")
	ErrMediaOnlyEditCaption = errors.New("only media message caption can be edited")
)

func isMediaMsgtype(msgType event.MessageType) bool {
	return msgType == event.MsgImage || msgType == event.MsgAudio || msgType == event.MsgVideo || msgType == event.MsgFile
}

type ConvertedSlackMessage struct {
	SendReq    slack.MsgOption
	FileUpload *slack.FileUploadParameters
	FileShare  *slack.ShareFileParams
}

func (mc *MessageConverter) ToSlack(
	ctx context.Context,
	client *slack.Client,
	portal *bridgev2.Portal,
	content *event.MessageEventContent,
	evt *event.Event,
	threadRoot *database.Message,
	editTarget *database.Message,
) (conv *ConvertedSlackMessage, err error) {
	log := zerolog.Ctx(ctx)

	if evt.Type == event.EventSticker {
		// Slack doesn't have stickers, just bridge stickers as images
		content.MsgType = event.MsgImage
	}

	var editTargetID, threadRootID string
	if editTarget != nil {
		if isMediaMsgtype(content.MsgType) {
			content.MsgType = event.MsgText
			if content.FileName == "" || content.FileName == content.Body {
				return nil, ErrMediaOnlyEditCaption
			}
		}
		var ok bool
		_, _, editTargetID, ok = slackid.ParseMessageID(editTarget.ID)
		if !ok {
			return nil, fmt.Errorf("failed to parse edit target ID")
		}
	}
	if threadRoot != nil {
		threadRootMessageID := threadRoot.ID
		if threadRoot.ThreadRoot != "" {
			threadRootMessageID = threadRoot.ThreadRoot
		}
		var ok bool
		_, _, threadRootID, ok = slackid.ParseMessageID(threadRootMessageID)
		if !ok {
			return nil, fmt.Errorf("failed to parse thread root ID")
		}
	}

	switch content.MsgType {
	case event.MsgText, event.MsgEmote, event.MsgNotice:
		options := make([]slack.MsgOption, 0, 4)
		var block slack.Block
		if content.Format == event.FormatHTML {
			block = mc.MatrixHTMLParser.Parse(ctx, content.FormattedBody, content.Mentions, portal)
		} else {
			block = slack.NewRichTextBlock("", slack.NewRichTextSection(slack.NewRichTextSectionTextElement(content.Body, nil)))
		}
		options = append(options, slack.MsgOptionBlocks(block))
		if editTargetID != "" {
			options = append(options, slack.MsgOptionUpdate(editTargetID))
		} else if threadRootID != "" {
			options = append(options, slack.MsgOptionTS(threadRootID))
		}
		if content.MsgType == event.MsgEmote {
			options = append(options, slack.MsgOptionMeMessage())
		}
		return &ConvertedSlackMessage{SendReq: slack.MsgOptionCompose(options...)}, nil
	case event.MsgAudio, event.MsgFile, event.MsgImage, event.MsgVideo:
		data, err := mc.Bridge.Bot.DownloadMedia(ctx, content.URL, content.File)
		if err != nil {
			log.Err(err).Msg("Failed to download Matrix attachment")
			return nil, ErrMediaDownloadFailed
		}

		var filename, caption, captionHTML, subtype string
		if content.FileName == "" || content.FileName == content.Body {
			filename = content.Body
		} else {
			filename = content.FileName
			caption = content.Body
			captionHTML = content.FormattedBody
		}
		useFileUpload := false
		if content.MSC3245Voice != nil && ffmpeg.Supported() {
			data, err = ffmpeg.ConvertBytes(ctx, data, ".webm", []string{}, []string{"-c:a", "copy"}, content.Info.MimeType)
			if err != nil {
				log.Err(err).Msg("Failed to convert voice message")
				return nil, ErrMediaConvertFailed
			}
			filename += ".webm"
			content.Info.MimeType = "audio/webm;codecs=opus"
			subtype = "slack_audio"
		}
		_, channelID := slackid.ParsePortalID(portal.ID)
		if useFileUpload {
			fileUpload := &slack.FileUploadParameters{
				Filename:        filename,
				Filetype:        content.Info.MimeType,
				Reader:          bytes.NewReader(data),
				Channels:        []string{channelID},
				ThreadTimestamp: threadRootID,
			}
			if caption != "" {
				fileUpload.InitialComment = caption
			}
			return &ConvertedSlackMessage{FileUpload: fileUpload}, nil
		} else {
			resp, err := client.GetFileUploadURL(ctx, slack.GetFileUploadURLParameters{
				Filename: filename,
				Length:   len(data),
				SubType:  subtype,
			})
			if err != nil {
				log.Err(err).Msg("Failed to get file upload URL")
				return nil, ErrMediaUploadFailed
			}
			err = client.UploadToURL(ctx, resp, content.Info.MimeType, data)
			if err != nil {
				log.Err(err).Msg("Failed to upload file")
				return nil, ErrMediaUploadFailed
			}
			err = client.CompleteFileUpload(ctx, resp)
			if err != nil {
				log.Err(err).Msg("Failed to complete file upload")
				return nil, ErrMediaUploadFailed
			}
			var block slack.Block
			if captionHTML != "" {
				block = mc.MatrixHTMLParser.Parse(ctx, content.FormattedBody, content.Mentions, portal)
			} else if caption != "" {
				block = slack.NewRichTextBlock("", slack.NewRichTextSection(slack.NewRichTextSectionTextElement(caption, nil)))
			}
			fileShare := &slack.ShareFileParams{
				Files:   []string{resp.File},
				Channel: channelID,
			}
			if block != nil {
				fileShare.Blocks = []slack.Block{block}
			}
			return &ConvertedSlackMessage{FileShare: fileShare}, nil
		}
	default:
		return nil, ErrUnknownMsgType
	}
}

func (mc *MessageConverter) uploadMedia(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data []byte, content *event.MessageEventContent) error {
	content.Info.Size = len(data)
	if content.Info.Width == 0 && content.Info.Height == 0 && strings.HasPrefix(content.Info.MimeType, "image/") {
		cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
		content.Info.Width, content.Info.Height = cfg.Width, cfg.Height
	}

	mxc, file, err := intent.UploadMedia(ctx, portal.MXID, data, "", content.Info.MimeType)
	if err != nil {
		return err
	}
	if file != nil {
		content.File = file
	} else {
		content.URL = mxc
	}
	return nil
}
