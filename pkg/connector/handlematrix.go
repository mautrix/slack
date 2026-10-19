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
	"time"

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-slack/pkg/connector/slackdb"
	"go.mau.fi/mautrix-slack/pkg/emoji"
	"go.mau.fi/mautrix-slack/pkg/msgconv"
	"go.mau.fi/mautrix-slack/pkg/slackid"
)

var (
	_ bridgev2.EditHandlingNetworkAPI        = (*SlackClient)(nil)
	_ bridgev2.RedactionHandlingNetworkAPI   = (*SlackClient)(nil)
	_ bridgev2.ReactionHandlingNetworkAPI    = (*SlackClient)(nil)
	_ bridgev2.ReadReceiptHandlingNetworkAPI = (*SlackClient)(nil)
	_ bridgev2.TypingHandlingNetworkAPI      = (*SlackClient)(nil)
	_ bridgev2.RoomNameHandlingNetworkAPI    = (*SlackClient)(nil)
	_ bridgev2.RoomTopicHandlingNetworkAPI   = (*SlackClient)(nil)
	_ bridgev2.MembershipHandlingNetworkAPI  = (*SlackClient)(nil)
)

func (s *SlackClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	if s.Client == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	_, channelID := slackid.ParsePortalID(msg.Portal.ID)
	if channelID == "" {
		return nil, errors.New("invalid channel ID")
	}
	conv, err := s.Main.MsgConv.ToSlack(ctx, s.Client, msg.Portal, msg.Content, msg.Event, msg.ThreadRoot, nil, msg.OrigSender, s.IsRealUser)
	if err != nil {
		return nil, wrapSlackConversionError(err)
	}
	timestamp, err := s.sendToSlack(ctx, channelID, conv, msg)
	if err != nil {
		return nil, wrapSlackMatrixError(err)
	}
	if timestamp == "" {
		return &bridgev2.MatrixMessageResponse{Pending: true}, nil
	}
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        slackid.MakeMessageID(s.TeamID, channelID, timestamp),
			SenderID:  slackid.MakeUserID(s.TeamID, s.UserID),
			Timestamp: slackid.ParseSlackTimestamp(timestamp),
		},
		StreamOrder: slackid.ParseSlackTimestamp(timestamp).UnixMicro(),
	}, nil
}

func newEmptyChan() chan struct{} {
	return make(chan struct{})
}

func (s *SlackClient) waitForFileCreated(ctx context.Context, fileID string) {
	waitChan := s.fileCreatedListeners.GetOrSetFactory(fileID, newEmptyChan)
	defer s.fileCreatedListeners.Delete(fileID)
	if waitChan != nil {
		select {
		case <-time.After(5 * time.Second):
			zerolog.Ctx(ctx).Warn().Str("file_id", fileID).Msg("Timed out waiting for file_created event from Slack")
		case <-waitChan:
			zerolog.Ctx(ctx).Debug().Str("file_id", fileID).Msg("File create listener fired, sending file message")
		}
	} else {
		zerolog.Ctx(ctx).Debug().Str("file_id", fileID).Msg("File create listener is nil, assuming file_created event was already received")
	}
}

func (s *SlackClient) sendToSlack(
	ctx context.Context,
	channelID string,
	conv *msgconv.ConvertedSlackMessage,
	msg *bridgev2.MatrixMessage,
) (string, error) {
	log := zerolog.Ctx(ctx)
	if conv.SendReq != nil {
		log.Debug().Msg("Sending message to Slack")
		_, timestamp, err := s.Client.PostMessageContext(ctx, channelID, conv.SendReq)
		return timestamp, err
	} else if conv.FileUpload != nil {
		log.Debug().Msg("Uploading attachment to Slack")
		file, err := s.Client.UploadFileContext(ctx, *conv.FileUpload)
		if err != nil {
			log.Err(err).Msg("Failed to upload attachment to Slack")
			return "", err
		}
		s.waitForFileCreated(ctx, file.ID)
		var shareInfo slack.ShareFileInfo
		// Slack puts the channel message info after uploading a file in either file.shares.private or file.shares.public
		if info, found := file.Shares.Private[channelID]; found && len(info) > 0 {
			shareInfo = info[0]
		} else if info, found = file.Shares.Public[channelID]; found && len(info) > 0 {
			shareInfo = info[0]
		}
		if shareInfo.Ts != "" {
			return shareInfo.Ts, nil
		}
		if msg != nil {
			msg.AddPendingToSave(nil, networkid.TransactionID(fmt.Sprintf("%s:%s", s.UserID, file.ID)), nil)
		}
		return "", nil
	} else if conv.FileShare != nil {
		for _, fileID := range conv.FileShare.Files {
			s.waitForFileCreated(ctx, fileID)
		}
		log.Debug().Strs("files", conv.FileShare.Files).Msg("Sharing already uploaded attachment to Slack")
		resp, err := s.Client.ShareFile(ctx, *conv.FileShare)
		if err != nil {
			log.Err(err).Msg("Failed to share attachment to Slack")
			return "", err
		}
		return resp.FileMsgTS, nil
	} else {
		return "", bridgev2.WrapErrorInStatus(errors.New("nothing to send")).
			WithErrorAsMessage().
			WithIsCertain(true).
			WithSendNotice(true).
			WithErrorReason(event.MessageStatusUnsupported).
			WithStatus(event.MessageStatusFail)
	}
}

func (s *SlackClient) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	if s.Client == nil {
		return bridgev2.ErrNotLoggedIn
	}
	_, channelID := slackid.ParsePortalID(msg.Portal.ID)
	if channelID == "" {
		return errors.New("invalid channel ID")
	}
	conv, err := s.Main.MsgConv.ToSlack(ctx, s.Client, msg.Portal, msg.Content, msg.Event, nil, msg.EditTarget, msg.OrigSender, s.IsRealUser)
	if err != nil {
		return wrapSlackConversionError(err)
	}
	msg.EditTarget.Metadata.(*slackid.MessageMetadata).LastEditTS, err = s.sendToSlack(ctx, channelID, conv, nil)
	return wrapSlackMatrixError(err)
}

func wrapSlackConversionError(err error) error {
	if err == nil || errors.As(err, &bridgev2.MessageStatus{}) {
		return err
	}
	if status := msgconvErrorStatus(err); status != nil {
		return status
	}
	if slackAPIErrorCode(err) != "" {
		return wrapSlackMatrixError(err)
	}
	return bridgev2.WrapErrorInStatus(err).
		WithMessage("failed to prepare message for Slack").
		WithIsCertain(true).
		WithSendNotice(true)
}

func msgconvErrorStatus(err error) error {
	switch {
	case errors.Is(err, msgconv.ErrUnknownMsgType):
		return bridgev2.ErrUnsupportedMessageType
	case errors.Is(err, msgconv.ErrMediaOnlyEditCaption):
		return bridgev2.WrapErrorInStatus(err).
			WithMessage("only the caption of media messages can be edited").
			WithErrorReason(event.MessageStatusUnsupported).
			WithStatus(event.MessageStatusFail).
			WithIsCertain(true).
			WithSendNotice(true)
	case errors.Is(err, msgconv.ErrMediaDownloadFailed):
		return bridgev2.ErrMediaDownloadFailed
	case errors.Is(err, msgconv.ErrMediaUploadFailed):
		return bridgev2.ErrMediaReuploadFailed
	case errors.Is(err, msgconv.ErrMediaConvertFailed):
		return bridgev2.ErrMediaConvertFailed
	}
	return nil
}

func wrapSlackMatrixError(err error) error {
	if err == nil || errors.As(err, &bridgev2.MessageStatus{}) {
		return err
	}
	if status := msgconvErrorStatus(err); status != nil {
		return status
	}
	switch slackAPIErrorCode(err) {
	case "edit_window_closed":
		return bridgev2.ErrEditTargetTooOld
	case "cant_update_message", "cant_delete_message":
		return bridgev2.WrapErrorInStatus(err).
			WithMessage("you don't have permission to change this message").
			WithErrorReason(event.MessageStatusNoPermission).
			WithStatus(event.MessageStatusFail).
			WithIsCertain(true).
			WithSendNotice(true)
	case "message_not_found":
		return bridgev2.WrapErrorInStatus(err).
			WithMessage("message not found on Slack").
			WithStatus(event.MessageStatusFail).
			WithIsCertain(true).
			WithSendNotice(true)
	case "ratelimited", "rate_limited":
		return bridgev2.WrapErrorInStatus(err).
			WithMessage("rate limited by Slack").
			WithErrorReason(event.MessageStatusNetworkError).
			WithIsCertain(true).
			WithSendNotice(true)
	case "not_in_channel", "channel_not_found":
		return bridgev2.WrapErrorInStatus(err).
			WithMessage("you don't have access to this Slack channel").
			WithErrorReason(event.MessageStatusNoPermission).
			WithStatus(event.MessageStatusFail).
			WithIsCertain(true).
			WithSendNotice(true)
	case "is_archived", "channel_is_archived":
		return bridgev2.WrapErrorInStatus(err).
			WithMessage("this Slack channel is archived").
			WithErrorReason(event.MessageStatusNoPermission).
			WithStatus(event.MessageStatusFail).
			WithIsCertain(true).
			WithSendNotice(true)
	case "restricted_action", "restricted_action_read_only_channel", "restricted_action_thread_only_channel",
		"restricted_action_non_threadable_channel", "no_permission", "not_authorized", "access_denied",
		"ekm_access_denied", "team_access_not_granted", "user_is_restricted", "missing_scope",
		"cant_invite", "cant_invite_self", "cant_kick_self", "cant_kick_from_general", "cant_leave_general":
		return bridgev2.WrapErrorInStatus(err).
			WithMessage("you don't have permission to do that on Slack").
			WithErrorReason(event.MessageStatusNoPermission).
			WithStatus(event.MessageStatusFail).
			WithIsCertain(true).
			WithSendNotice(true)
	case "msg_too_long", "too_long":
		return bridgev2.WrapErrorInStatus(err).
			WithMessage("message is too long for Slack").
			WithErrorReason(event.MessageStatusUnsupported).
			WithStatus(event.MessageStatusFail).
			WithIsCertain(true).
			WithSendNotice(true)
	case "":
		var statusErr slack.StatusCodeError
		if errors.As(err, &statusErr) {
			return bridgev2.WrapErrorInStatus(err).
				WithMessage("Slack server error").
				WithErrorReason(event.MessageStatusNetworkError).
				WithIsCertain(false).
				WithSendNotice(true)
		}
		return bridgev2.WrapErrorInStatus(err).
			WithMessage("failed to reach Slack").
			WithErrorReason(event.MessageStatusNetworkError).
			WithIsCertain(false).
			WithSendNotice(true)
	default:
		return bridgev2.WrapErrorInStatus(err).
			WithMessage("unexpected error from Slack").
			WithErrorReason(event.MessageStatusNetworkError).
			WithIsCertain(true).
			WithSendNotice(true)
	}
}

func (s *SlackClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	if s.Client == nil {
		return bridgev2.ErrNotLoggedIn
	}
	_, channelID, messageID, ok := slackid.ParseMessageID(msg.TargetMessage.ID)
	if !ok {
		return errors.New("invalid message ID")
	}
	_, _, err := s.Client.DeleteMessageContext(ctx, channelID, messageID)
	return wrapSlackMatrixError(err)
}

func unknownEmojiError(key string) error {
	return bridgev2.WrapErrorInStatus(fmt.Errorf("unknown emoji %q", key)).
		WithMessage("that emoji isn't available in this Slack workspace").
		WithIsCertain(true).
		WithSendNotice(true).
		WithErrorReason(event.MessageStatusUnsupported).
		WithStatus(event.MessageStatusFail)
}

func (s *SlackClient) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (resp bridgev2.MatrixReactionPreResponse, err error) {
	key := msg.Content.RelatesTo.Key
	var emojiID string
	if strings.ContainsRune(key, ':') {
		var dbEmoji *slackdb.Emoji
		dbEmoji, err = s.Main.DB.Emoji.GetByMXC(ctx, key)
		if err != nil {
			err = fmt.Errorf("failed to get emoji from db: %w", err)
		} else if dbEmoji == nil {
			err = unknownEmojiError(key)
		} else {
			emojiID = dbEmoji.EmojiID
		}
	} else {
		emojiID = emoji.GetShortcode(key)
		if emojiID == "" {
			err = unknownEmojiError(key)
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
	if s.Client == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	_, channelID, messageID, ok := slackid.ParseMessageID(msg.TargetMessage.ID)
	if !ok {
		return nil, errors.New("invalid message ID")
	}
	err = s.Client.AddReactionContext(ctx, string(msg.PreHandleResp.EmojiID), slack.ItemRef{
		Channel:   channelID,
		Timestamp: messageID,
	})
	err = wrapSlackMatrixError(err)
	return
}

func (s *SlackClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	if s.Client == nil {
		return bridgev2.ErrNotLoggedIn
	}
	_, channelID, messageID, ok := slackid.ParseMessageID(msg.TargetReaction.MessageID)
	if !ok {
		return errors.New("invalid message ID")
	}
	err := s.Client.RemoveReactionContext(ctx, string(msg.TargetReaction.EmojiID), slack.ItemRef{
		Channel:   channelID,
		Timestamp: messageID,
	})
	if slackAPIErrorCode(err) == "no_reaction" {
		return nil
	}
	return wrapSlackMatrixError(err)
}

func (s *SlackClient) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	if s.Client == nil {
		return bridgev2.ErrNotLoggedIn
	} else if !s.IsRealUser {
		return nil
	}
	if msg.ExactMessage != nil {
		_, channelID, messageTS, ok := slackid.ParseMessageID(msg.ExactMessage.ID)
		if !ok {
			return errors.New("invalid message ID")
		}
		return wrapSlackMatrixError(s.Client.MarkConversationContext(ctx, channelID, messageTS))
	}
	lastMessage, err := s.UserLogin.Bridge.DB.Message.GetLastPartAtOrBeforeTime(ctx, msg.Portal.PortalKey, msg.ReadUpTo)
	if err != nil {
		return err
	} else if lastMessage != nil {
		_, channelID, messageTS, ok := slackid.ParseMessageID(lastMessage.ID)
		if !ok {
			return errors.New("invalid message ID")
		}
		return wrapSlackMatrixError(s.Client.MarkConversationContext(ctx, channelID, messageTS))
	}
	return nil
}

func (s *SlackClient) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	if s.Client == nil {
		return bridgev2.ErrNotLoggedIn
	} else if !s.IsRealUser {
		return nil
	}
	_, channelID := slackid.ParsePortalID(msg.Portal.ID)
	if channelID == "" {
		return nil
	}
	s.RTM.SendMessage(s.RTM.NewTypingMessage(channelID))
	return nil
}

func (s *SlackClient) HandleMatrixRoomName(ctx context.Context, msg *bridgev2.MatrixRoomName) (bool, error) {
	_, channelID := slackid.ParsePortalID(msg.Portal.ID)
	if channelID == "" {
		return false, errors.New("invalid channel ID")
	}
	resp, err := s.Client.RenameConversationContext(ctx, channelID, strings.TrimPrefix(msg.Content.Name, "#"))
	zerolog.Ctx(ctx).Trace().Any("resp_data", resp).Msg("Renamed conversation")
	return err == nil, wrapSlackMatrixError(err)
}

func (s *SlackClient) HandleMatrixMembership(ctx context.Context, msg *bridgev2.MatrixMembershipChange) (*bridgev2.MatrixMembershipResult, error) {
	if msg.Type.IsSelf && msg.OrigSender != nil {
		return nil, nil
	}

	if s.Client == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	_, channelID := slackid.ParsePortalID(msg.Portal.ID)
	if channelID == "" {
		return nil, errors.New("invalid channel ID")
	}
	if msg.Portal.RoomType == database.RoomTypeDM || msg.Portal.RoomType == database.RoomTypeGroupDM {
		return nil, nil
	}

	var targetUserID string
	switch target := msg.Target.(type) {
	case *bridgev2.Ghost:
		_, targetUserID = slackid.ParseUserID(target.ID)
	case *bridgev2.UserLogin:
		_, targetUserID = slackid.ParseUserLoginID(target.ID)
	default:
		return nil, fmt.Errorf("unknown membership target type: %T", target)
	}
	if targetUserID == "" {
		return nil, errors.New("invalid target user ID")
	}

	log := zerolog.Ctx(ctx)
	switch msg.Type {
	case bridgev2.Invite:
		_, err := s.Client.InviteUsersToConversationContext(ctx, channelID, targetUserID)
		if err != nil {
			return nil, wrapSlackMatrixError(fmt.Errorf("failed to invite user to channel: %w", err))
		}
		log.Debug().Str("target_user_id", targetUserID).Msg("Invited user to Slack channel")
	case bridgev2.Leave:
		_, err := s.Client.LeaveConversationContext(ctx, channelID)
		if err != nil {
			return nil, wrapSlackMatrixError(fmt.Errorf("failed to leave channel: %w", err))
		}
		log.Debug().Msg("Left Slack channel")
	case bridgev2.Kick:
		err := s.Client.KickUserFromConversationContext(ctx, channelID, targetUserID)
		if err != nil {
			return nil, wrapSlackMatrixError(fmt.Errorf("failed to kick user from channel: %w", err))
		}
		log.Debug().Str("target_user_id", targetUserID).Msg("Kicked user from Slack channel")
	default:
		return nil, nil
	}
	return nil, nil
}

func (s *SlackClient) HandleMatrixRoomTopic(ctx context.Context, msg *bridgev2.MatrixRoomTopic) (bool, error) {
	_, channelID := slackid.ParsePortalID(msg.Portal.ID)
	if channelID == "" {
		return false, errors.New("invalid channel ID")
	}
	resp, err := s.Client.SetTopicOfConversationContext(ctx, channelID, msg.Content.Topic)
	zerolog.Ctx(ctx).Trace().Any("resp_data", resp).Msg("Changed conversation topic")
	return err == nil, wrapSlackMatrixError(err)
}
