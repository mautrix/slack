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

package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-slack/pkg/connector/slackdb"
	"go.mau.fi/mautrix-slack/pkg/msgconv/emoji"
	"go.mau.fi/mautrix-slack/pkg/slackid"
)

var (
	_ bridgev2.EditHandlingNetworkAPI        = (*SlackClient)(nil)
	_ bridgev2.RedactionHandlingNetworkAPI   = (*SlackClient)(nil)
	_ bridgev2.ReactionHandlingNetworkAPI    = (*SlackClient)(nil)
	_ bridgev2.ReadReceiptHandlingNetworkAPI = (*SlackClient)(nil)
	_ bridgev2.TypingHandlingNetworkAPI      = (*SlackClient)(nil)
)

func (s *SlackClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	_, channelID := slackid.ParsePortalID(msg.Portal.ID)
	if channelID == "" {
		return nil, errors.New("invalid channel ID")
	}
	sendOpts, fileUpload, err := s.Main.MsgConv.ToSlack(ctx, msg.Portal, msg.Content, msg.Event, msg.ThreadRoot, nil)
	if err != nil {
		return nil, err
	}
	timestamp, err := s.sendToSlack(ctx, channelID, sendOpts, fileUpload)
	if err != nil {
		return nil, err
	}
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        slackid.MakeMessageID(s.TeamID, channelID, timestamp),
			SenderID:  slackid.MakeUserID(s.TeamID, s.UserID),
			Timestamp: slackid.ParseSlackTimestamp(timestamp),
		},
	}, nil
}

func (s *SlackClient) sendToSlack(ctx context.Context, channelID string, sendReq slack.MsgOption, fileUpload *slack.FileUploadParameters) (string, error) {
	log := zerolog.Ctx(ctx)
	if sendReq != nil {
		log.Debug().Msg("Sending message to Slack")
		_, timestamp, err := s.Client.PostMessageContext(ctx, channelID, slack.MsgOptionAsUser(true), sendReq)
		return timestamp, err
	} else if fileUpload != nil {
		log.Debug().Msg("Uploading attachment to Slack")
		file, err := s.Client.UploadFileContext(ctx, *fileUpload)
		if err != nil {
			log.Err(err).Msg("Failed to upload attachment to Slack")
			return "", err
		}
		var shareInfo slack.ShareFileInfo
		// Slack puts the channel message info after uploading a file in either file.shares.private or file.shares.public
		if info, found := file.Shares.Private[channelID]; found && len(info) > 0 {
			shareInfo = info[0]
		} else if info, found = file.Shares.Public[channelID]; found && len(info) > 0 {
			shareInfo = info[0]
		} else {
			return "", errors.New("failed to upload media to Slack")
		}
		return shareInfo.Ts, nil
	} else {
		return "", errors.New("no message or attachment to send")
	}
}

func (s *SlackClient) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	_, channelID := slackid.ParsePortalID(msg.Portal.ID)
	if channelID == "" {
		return errors.New("invalid channel ID")
	}
	sendOpts, fileUpload, err := s.Main.MsgConv.ToSlack(ctx, msg.Portal, msg.Content, msg.Event, nil, msg.EditTarget)
	if err != nil {
		return err
	}
	_, err = s.sendToSlack(ctx, channelID, sendOpts, fileUpload)
	return err
}

func (s *SlackClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	_, channelID, messageID, ok := slackid.ParseMessageID(msg.TargetMessage.ID)
	if !ok {
		return errors.New("invalid message ID")
	}
	_, _, err := s.Client.DeleteMessageContext(ctx, channelID, messageID)
	return err
}

func (s *SlackClient) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (resp bridgev2.MatrixReactionPreResponse, err error) {
	key := msg.Content.RelatesTo.Key
	var emojiID string
	if strings.ContainsRune(key, ':') {
		var dbEmoji *slackdb.Emoji
		dbEmoji, err = s.Main.DB.Emoji.GetByMXC(ctx, key)
		if err != nil {
			err = fmt.Errorf("failed to get emoji from db: %w", err)
		}
		emojiID = dbEmoji.EmojiID
	} else {
		var ok bool
		emojiID, ok = emoji.UnicodeToShortcodeMap[key]
		if !ok {
			err = fmt.Errorf("unknown emoji %q", key)
		}
	}
	if err != nil {
		return
	}
	return bridgev2.MatrixReactionPreResponse{
		SenderID: slackid.MakeUserID(s.TeamID, s.UserID),
		EmojiID:  networkid.EmojiID(emojiID),
	}, nil
}

func (s *SlackClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (reaction *database.Reaction, err error) {
	_, channelID, messageID, ok := slackid.ParseMessageID(msg.TargetMessage.ID)
	if !ok {
		return nil, errors.New("invalid message ID")
	}
	err = s.Client.AddReactionContext(ctx, string(msg.PreHandleResp.EmojiID), slack.ItemRef{
		Channel:   channelID,
		Timestamp: messageID,
	})
	return
}

func (s *SlackClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	_, channelID, messageID, ok := slackid.ParseMessageID(msg.TargetReaction.MessageID)
	if !ok {
		return errors.New("invalid message ID")
	}
	err := s.Client.RemoveReactionContext(ctx, string(msg.TargetReaction.EmojiID), slack.ItemRef{
		Channel:   channelID,
		Timestamp: messageID,
	})
	if err != nil && err.Error() != "reaction" {
		return err
	}
	return nil
}

func (s *SlackClient) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	if msg.ExactMessage != nil {
		_, channelID, messageTS, ok := slackid.ParseMessageID(msg.ExactMessage.ID)
		if !ok {
			return errors.New("invalid message ID")
		}
		return s.Client.MarkConversationContext(ctx, channelID, messageTS)
	}
	lastMessage, err := s.UserLogin.Bridge.DB.Message.GetLastPartAtOrBeforeTime(ctx, msg.Portal.PortalKey, msg.ReadUpTo)
	if err != nil {
		return err
	} else if lastMessage != nil {
		_, channelID, messageTS, ok := slackid.ParseMessageID(lastMessage.ID)
		if !ok {
			return errors.New("invalid message ID")
		}
		return s.Client.MarkConversationContext(ctx, channelID, messageTS)
	}
	return nil
}

func (s *SlackClient) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	_, channelID := slackid.ParsePortalID(msg.Portal.ID)
	if channelID == "" {
		return nil
	}
	s.RTM.SendMessage(s.RTM.NewTypingMessage(channelID))
	return nil
}
