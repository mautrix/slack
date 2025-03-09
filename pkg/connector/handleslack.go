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
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-slack/pkg/slackid"
)

func (s *SlackClient) HandleSlackEvent(rawEvt any) {
	log := s.UserLogin.Log.With().
		Str("action", "handle slack event").
		Type("event_type", rawEvt).
		Logger()
	ctx := log.WithContext(context.TODO())
	switch evt := rawEvt.(type) {
	case *slack.ConnectingEvent:
		omitBridgeState := s.UserLogin.BridgeState.GetPrevUnsent().StateEvent == status.StateTransientDisconnect
		log.Debug().
			Int("attempt_num", evt.Attempt).
			Int("connection_count", evt.ConnectionCount).
			Bool("omit_bridge_state", omitBridgeState).
			Msg("Connecting to Slack")
		if !omitBridgeState {
			s.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})
		}
	case *slack.ConnectedEvent:
		log.Debug().Msg("Connected to websocket, waiting for hello event")
	case *slack.DisconnectedEvent:
		if evt.Intentional {
			log.Debug().Bool("intentional", evt.Intentional).Err(evt.Cause).Msg("Disconnected from Slack")
		} else {
			log.Warn().Bool("intentional", evt.Intentional).Err(evt.Cause).Msg("Disconnected from Slack")
			s.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateTransientDisconnect, Error: "slack-rtm-disconnected"})
		}
	case *slack.IncomingEventError:
		log.Warn().Err(evt.ErrorObj).Msg("Incoming event error")
	case *slack.UnmarshallingErrorEvent:
		logEvt := log.Debug().Err(evt.ErrorObj)
		if log.GetLevel() == zerolog.TraceLevel || strings.Contains(evt.ErrorObj.Error(), `Received unmapped event "error"`) {
			logEvt = logEvt.RawJSON("raw_data", evt.Raw)
		}
		logEvt.Msg("Unmarshalling error")
	case *slack.RTMErrorEvent:
		log.Error().
			Str(zerolog.ErrorFieldName, evt.Error.Msg).
			Int("error_code", evt.Error.Code).
			Msg("Got RTM error")
	case *slack.HelloEvent:
		log.Debug().Msg("Received hello event from websocket (now really connected)")
		s.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
	case *slack.InvalidAuthEvent:
		s.invalidateSession(ctx, status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      "slack-invalid-auth",
		})
	case *slack.RTMError:
		log.Err(evt).Msg("Got RTM error")
		s.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateUnknownError,
			Error:      status.BridgeStateErrorCode(fmt.Sprintf("slack-rtm-error-%d", evt.Code)),
			Message:    fmt.Sprintf("%d: %s", evt.Code, evt.Msg),
		})
	case *slack.MessageEvent, *slack.ReactionAddedEvent, *slack.ReactionRemovedEvent,
		*slack.UserTypingEvent, *slack.ChannelMarkedEvent, *slack.IMMarkedEvent, *slack.GroupMarkedEvent,
		*slack.ChannelJoinedEvent, *slack.ChannelLeftEvent, *slack.GroupJoinedEvent, *slack.GroupLeftEvent,
		*slack.MemberJoinedChannelEvent, *slack.MemberLeftChannelEvent,
		*slack.ChannelUpdateEvent:
		wrapped, err := s.wrapEvent(ctx, evt)
		if err != nil {
			log.Err(err).Msg("Failed to wrap Slack event")
		} else if wrapped != nil {
			s.UserLogin.Bridge.QueueRemoteEvent(s.UserLogin, wrapped)
		}
	case *slack.EmojiChangedEvent:
		go s.handleEmojiChange(ctx, evt)
	case *slack.FileSharedEvent, *slack.FilePublicEvent, *slack.FilePrivateEvent,
		*slack.FileCreatedEvent, *slack.FileChangeEvent, *slack.FileDeletedEvent,
		*slack.DesktopNotificationEvent, *slack.ReconnectUrlEvent, *slack.LatencyReport:
		// ignored intentionally, these are duplicates or do not contain useful information
	case *slack.UserChangeEvent:
		go s.handleUserChange(ctx, &evt.User)
	case *slack.UserInvalidatedEvent:
		go s.handleUserInvalidated(ctx, evt.User.ID)
	default:
		logEvt := log.Debug()
		if log.GetLevel() == zerolog.TraceLevel {
			logEvt = logEvt.Any("event_data", evt)
		}
		logEvt.Msg("Unrecognized Slack event type")
	}
}

func (s *SlackClient) HandleSocketModeEvent(evt socketmode.Event) {
	switch evt.Type {
	case socketmode.EventTypeConnecting:
		s.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})
	case socketmode.EventTypeConnectionError:
		s.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateTransientDisconnect, Error: "slack-socketmode-connection-error"})
	case socketmode.EventTypeConnected:
		s.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
	case socketmode.EventTypeEventsAPI:
		eaEvt, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			s.UserLogin.Log.Warn().Type("data_type", evt.Data).Msg("Unexpected event type in socket mode")
			return
		}
		if eaEvt.Type == slackevents.CallbackEvent {
			s.HandleSlackEvent(eaEvt.InnerEvent.Data)
		}
	case socketmode.EventTypeInteractive:
		//callback, ok := evt.Data.(slack.InteractionCallback)
	case socketmode.EventTypeSlashCommand:
		//cmd, ok := evt.Data.(slack.SlashCommand)
	}
	if evt.Request != nil && evt.Request.EnvelopeID != "" {
		s.SocketMode.Ack(*evt.Request)
	}
}

func (s *SlackClient) handleUserChange(ctx context.Context, user *slack.User) {
	ghost, err := s.Main.br.GetGhostByID(ctx, slackid.MakeUserID(s.TeamID, user.ID))
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get ghost")
		return
	}
	ghost.UpdateInfo(ctx, s.wrapUserInfo(user.ID, user, nil, ghost))
}

func (s *SlackClient) handleUserInvalidated(ctx context.Context, userID string) {
	ghost, err := s.Main.br.GetGhostByID(ctx, slackid.MakeUserID(s.TeamID, userID))
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get ghost")
		return
	}
	info, err := s.fetchUserInfo(ctx, userID, ghost.Metadata.(*slackid.GhostMetadata).SlackUpdatedTS, ghost)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to fetch user info after user invalidated event")
	} else if info != nil {
		ghost.UpdateInfo(ctx, info)
	}
}

func (s *SlackClient) wrapEvent(ctx context.Context, rawEvt any) (bridgev2.RemoteEvent, error) {
	var meta SlackEventMeta
	var metaErr error
	var wrapped bridgev2.RemoteEvent
	switch evt := rawEvt.(type) {
	case *slack.MessageEvent:
		if evt.SubType == slack.MsgSubTypeMessageChanged && evt.SubMessage.SubType == "huddle_thread" {
			return nil, nil
		}
		sender := evt.User
		if sender == "" {
			sender = evt.BotID
		}
		if sender == "" && evt.SubMessage != nil {
			sender = evt.SubMessage.User
			if sender == "" {
				sender = evt.SubMessage.BotID
			}
		}
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, sender, "")
		meta.CreatePortal = true
		meta.LogContext = func(c zerolog.Context) zerolog.Context {
			return c.
				Str("message_ts", evt.Timestamp).
				Str("message_id", string(meta.ID)).
				Str("message_sender", sender).
				Str("subtype", evt.SubType).
				Bool("hidden", evt.Hidden)
		}
		wrapped = &SlackMessage{
			SlackEventMeta: &meta,
			Data:           evt,
			Client:         s,
		}

	case *slack.ReactionAddedEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Item.Channel, nil, evt.User, evt.EventTimestamp)
		var err error
		wrapped, err = s.wrapReaction(ctx, &meta, evt.Reaction, true, evt.Item)
		if err != nil {
			return nil, fmt.Errorf("failed to get reaction info: %w", err)
		}
	case *slack.ReactionRemovedEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Item.Channel, nil, evt.User, evt.EventTimestamp)
		wrapped, _ = s.wrapReaction(ctx, &meta, evt.Reaction, false, evt.Item)

	case *slack.UserTypingEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, evt.User, "")
		wrapped = wrapTyping(&meta)

	case *slack.ChannelMarkedEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, s.UserID, evt.Timestamp)
		s.setLastReadCache(evt.Channel, evt.Timestamp)
		wrapped = wrapReadReceipt(&meta)
	case *slack.IMMarkedEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, s.UserID, evt.Timestamp)
		s.setLastReadCache(evt.Channel, evt.Timestamp)
		wrapped = wrapReadReceipt(&meta)
	case *slack.GroupMarkedEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, s.UserID, evt.Timestamp)
		s.setLastReadCache(evt.Channel, evt.Timestamp)
		wrapped = wrapReadReceipt(&meta)

	case *slack.ChannelJoinedEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel.ID, &evt.Channel, s.UserID, "")
		meta.CreatePortal = true
		wrapped = wrapMemberChange(&meta, meta.Sender, event.MembershipJoin, "")
	case *slack.ChannelLeftEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, s.UserID, evt.Timestamp)
		wrapped = wrapMemberChange(&meta, meta.Sender, event.MembershipLeave, event.MembershipJoin)
	case *slack.GroupJoinedEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel.ID, &evt.Channel, s.UserID, "")
		meta.CreatePortal = true
		wrapped = wrapMemberChange(&meta, meta.Sender, event.MembershipJoin, "")
	case *slack.GroupLeftEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, s.UserID, evt.Timestamp)
		wrapped = wrapMemberChange(&meta, meta.Sender, event.MembershipLeave, event.MembershipJoin)
	case *slack.MemberJoinedChannelEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, evt.User, evt.EventTimestamp)
		wrapped = wrapMemberChange(&meta, meta.Sender, event.MembershipJoin, "")
	case *slack.MemberLeftChannelEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, evt.User, evt.EventTimestamp)
		wrapped = wrapMemberChange(&meta, meta.Sender, event.MembershipLeave, event.MembershipJoin)

	case *slack.ChannelUpdateEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, "", evt.Timestamp)
		meta.Type = bridgev2.RemoteEventChatResync
		//meta.CreatePortal = true
		wrapped = &meta
	}
	return wrapped, metaErr
}

func (s *SlackClient) getReactionInfo(ctx context.Context, reaction string) (emoji string, extraContent map[string]any) {
	shortcode := fmt.Sprintf(":%s:", reaction)
	slackReactionInfo := map[string]any{
		"name": reaction,
	}
	var isImage bool
	emoji, isImage = s.GetEmoji(ctx, reaction)
	if isImage {
		slackReactionInfo["mxc"] = emoji
		if !s.Main.Config.CustomEmojiReactions {
			emoji = shortcode
		}
	}
	extraContent = map[string]any{
		"com.beeper.reaction.shortcode": shortcode,
		"fi.mau.slack.reaction":         slackReactionInfo,
	}
	return
}

func (s *SlackClient) wrapReaction(ctx context.Context, meta *SlackEventMeta, reaction string, add bool, target slack.ReactionItem) (*SlackReaction, error) {
	if add {
		meta.Type = bridgev2.RemoteEventReaction
	} else {
		meta.Type = bridgev2.RemoteEventReactionRemove
	}
	emoji, extraContent := s.getReactionInfo(ctx, reaction)
	return &SlackReaction{
		SlackEventMeta: meta,
		Emoji:          emoji,
		EmojiID:        networkid.EmojiID(reaction),
		Meta:           extraContent,
		TargetID:       slackid.MakeMessageID(s.TeamID, target.Channel, target.Timestamp),
	}, nil
}

func wrapTyping(meta *SlackEventMeta) *SlackTyping {
	meta.Type = bridgev2.RemoteEventTyping
	return &SlackTyping{SlackEventMeta: meta}
}

func wrapReadReceipt(meta *SlackEventMeta) *SlackReadReceipt {
	meta.Type = bridgev2.RemoteEventReadReceipt
	return &SlackReadReceipt{SlackEventMeta: meta}
}

func wrapMemberChange(meta *SlackEventMeta, sender bridgev2.EventSender, newMembership, prevMembership event.Membership) *SlackChatInfoChange {
	meta.Type = bridgev2.RemoteEventChatInfoChange
	meta.LogContext = func(c zerolog.Context) zerolog.Context {
		return c.
			Any("member_change_target", sender).
			Str("prev_membership", string(prevMembership)).
			Str("new_membership", string(newMembership))
	}
	return &SlackChatInfoChange{
		SlackEventMeta: meta,
		Change: &bridgev2.ChatInfoChange{
			MemberChanges: &bridgev2.ChatMemberList{
				Members: []bridgev2.ChatMember{{
					EventSender:    sender,
					Membership:     newMembership,
					PrevMembership: prevMembership,
				}},
			},
		},
	}
}

func (s *SlackClient) makeEventMeta(ctx context.Context, channelID string, channel *slack.Channel, senderID, timestamp string) (meta SlackEventMeta, err error) {
	if channel != nil {
		meta.PortalKey = s.makePortalKey(channel)
	} else {
		meta.PortalKey, err = s.UserLogin.Bridge.FindPortalReceiver(ctx, slackid.MakePortalID(s.TeamID, channelID), s.UserLogin.ID)
		if err != nil {
			err = fmt.Errorf("failed to find portal receiver: %w", err)
			return
		} else if meta.PortalKey.IsEmpty() {
			var ch *slack.Channel
			ch, err = s.fetchChatInfoWithCache(ctx, channelID)
			if err != nil {
				err = fmt.Errorf("failed to fetch channel info: %w", err)
				return
			}
			meta.PortalKey = s.makePortalKey(ch)
		}
	}
	if senderID != "" {
		meta.Sender = s.makeEventSender(senderID)
	}
	if timestamp != "" {
		meta.RawTimestamp = timestamp
		meta.ID = slackid.MakeMessageID(s.TeamID, channelID, timestamp)
		meta.Timestamp = slackid.ParseSlackTimestamp(timestamp)
	}
	meta.LogContext = func(c zerolog.Context) zerolog.Context { return c }
	return
}

type SlackEventMeta struct {
	Type         bridgev2.RemoteEventType
	PortalKey    networkid.PortalKey
	Sender       bridgev2.EventSender
	Timestamp    time.Time
	ID           networkid.MessageID
	LogContext   func(zerolog.Context) zerolog.Context
	CreatePortal bool
	RawTimestamp string
}

func (s *SlackEventMeta) GetType() bridgev2.RemoteEventType {
	return s.Type
}

func (s *SlackEventMeta) GetPortalKey() networkid.PortalKey {
	return s.PortalKey
}

func (s *SlackEventMeta) AddLogContext(c zerolog.Context) zerolog.Context {
	c = c.Str("event_ts", s.RawTimestamp)
	if s.LogContext == nil {
		return c
	}
	return s.LogContext(c)
}

func (s *SlackEventMeta) GetSender() bridgev2.EventSender {
	return s.Sender
}

func (s *SlackEventMeta) ShouldCreatePortal() bool {
	return s.CreatePortal
}

func (s *SlackEventMeta) GetTimestamp() time.Time {
	if s.Timestamp.IsZero() {
		return time.Now()
	}
	return s.Timestamp
}

func (s *SlackEventMeta) GetID() networkid.MessageID {
	return s.ID
}

var (
	_ bridgev2.RemoteEvent                    = (*SlackEventMeta)(nil)
	_ bridgev2.RemoteEventWithTimestamp       = (*SlackEventMeta)(nil)
	_ bridgev2.RemoteEventThatMayCreatePortal = (*SlackEventMeta)(nil)
)

type SlackChatInfoChange struct {
	*SlackEventMeta
	Change *bridgev2.ChatInfoChange
}

var _ bridgev2.RemoteChatInfoChange = (*SlackChatInfoChange)(nil)

func (s *SlackChatInfoChange) GetChatInfoChange(ctx context.Context) (*bridgev2.ChatInfoChange, error) {
	return s.Change, nil
}

type SlackReadReceipt struct {
	*SlackEventMeta
}

var _ bridgev2.RemoteReadReceipt = (*SlackReadReceipt)(nil)

func (s *SlackReadReceipt) GetLastReceiptTarget() networkid.MessageID {
	return s.ID
}

func (s *SlackReadReceipt) GetReceiptTargets() []networkid.MessageID {
	return nil
}

func (s *SlackReadReceipt) GetReadUpTo() time.Time {
	return s.Timestamp
}

type SlackTyping struct {
	*SlackEventMeta
}

var _ bridgev2.RemoteTyping = (*SlackTyping)(nil)

func (s *SlackTyping) GetTimeout() time.Duration {
	return 5 * time.Second
}

type SlackReaction struct {
	*SlackEventMeta
	TargetID networkid.MessageID
	EmojiID  networkid.EmojiID
	Emoji    string
	Meta     map[string]any
}

func (s *SlackReaction) GetTargetMessage() networkid.MessageID {
	return s.TargetID
}

func (s *SlackReaction) GetReactionEmoji() (string, networkid.EmojiID) {
	return s.Emoji, s.EmojiID
}

func (s *SlackReaction) GetRemovedEmojiID() networkid.EmojiID {
	return s.EmojiID
}

func (s *SlackReaction) GetReactionExtraContent() map[string]any {
	return s.Meta
}

var (
	_ bridgev2.RemoteReaction                 = (*SlackReaction)(nil)
	_ bridgev2.RemoteReactionRemove           = (*SlackReaction)(nil)
	_ bridgev2.RemoteReactionWithExtraContent = (*SlackReaction)(nil)
)

type SlackMessage struct {
	*SlackEventMeta
	Data   *slack.MessageEvent
	Client *SlackClient
}

func (s *SlackMessage) GetTransactionID() networkid.TransactionID {
	if len(s.Data.Files) != 1 {
		return ""
	}
	return networkid.TransactionID(fmt.Sprintf("%s:%s", s.Data.User, s.Data.Files[0].ID))
}

var (
	_ bridgev2.RemoteMessage                  = (*SlackMessage)(nil)
	_ bridgev2.RemoteEdit                     = (*SlackMessage)(nil)
	_ bridgev2.RemoteMessageRemove            = (*SlackMessage)(nil)
	_ bridgev2.RemoteChatResync               = (*SlackMessage)(nil)
	_ bridgev2.RemoteMessageWithTransactionID = (*SlackMessage)(nil)
)

type SlackChatResync struct {
	*SlackEventMeta
	Client         *SlackClient
	LatestMessage  string
	PreFetchedInfo *slack.Channel
	ShouldSyncInfo bool
}

func (s *SlackChatResync) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	if s.PreFetchedInfo != nil {
		wrappedInfo, err := s.Client.wrapChatInfo(ctx, s.PreFetchedInfo, portal.MXID == "")
		if err != nil {
			return nil, fmt.Errorf("failed to wrap chat info: %w", err)
		}
		return wrappedInfo, nil
	} else if !s.ShouldSyncInfo {
		return nil, nil
	}
	return s.Client.GetChatInfo(ctx, portal)
}

func (s *SlackChatResync) CheckNeedsBackfill(ctx context.Context, latestBridgedMessage *database.Message) (bool, error) {
	if latestBridgedMessage == nil {
		return s.LatestMessage != "" && s.LatestMessage != "0000000000.000000", nil
	}
	_, _, latestBridgedID, _ := slackid.ParseMessageID(latestBridgedMessage.ID)
	return latestBridgedID < s.LatestMessage, nil
}

var (
	_ bridgev2.RemoteChatResyncBackfill = (*SlackChatResync)(nil)
)

func (s *SlackMessage) GetType() bridgev2.RemoteEventType {
	switch s.Data.SubType {
	case slack.MsgSubTypeMessageChanged:
		return bridgev2.RemoteEventEdit
	case slack.MsgSubTypeMessageDeleted:
		return bridgev2.RemoteEventMessageRemove
	case slack.MsgSubTypeChannelTopic, slack.MsgSubTypeChannelPurpose, slack.MsgSubTypeChannelName,
		slack.MsgSubTypeGroupTopic, slack.MsgSubTypeGroupPurpose, slack.MsgSubTypeGroupName:
		// TODO implement deltas instead of full resync
		return bridgev2.RemoteEventChatResync
	case slack.MsgSubTypeMessageReplied, slack.MsgSubTypeGroupJoin, slack.MsgSubTypeGroupLeave,
		slack.MsgSubTypeChannelJoin, slack.MsgSubTypeChannelLeave:
		return bridgev2.RemoteEventUnknown
	case "", slack.MsgSubTypeMeMessage, slack.MsgSubTypeBotMessage, slack.MsgSubTypeThreadBroadcast, "huddle_thread":
		// Known types
		return bridgev2.RemoteEventMessage
	default:
		// For unknown types with the hidden flag, don't bridge
		if s.Data.Hidden {
			return bridgev2.RemoteEventUnknown
		}
		return bridgev2.RemoteEventMessage
	}
}

func (s *SlackMessage) ConvertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI) (*bridgev2.ConvertedMessage, error) {
	return s.Client.Main.MsgConv.ToMatrix(ctx, portal, intent, s.Client.UserLogin, &s.Data.Msg), nil
}

func (s *SlackMessage) ConvertEdit(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message) (*bridgev2.ConvertedEdit, error) {
	meta := existing[0].Metadata.(*slackid.MessageMetadata)
	if meta.LastEditTS >= s.Data.SubMessage.Edited.Timestamp {
		return nil, fmt.Errorf(
			"%w: last bridged edit is same as or newer than this one (%s >= %s)",
			bridgev2.ErrIgnoringRemoteEvent, meta.LastEditTS, s.Data.SubMessage.Edited.Timestamp,
		)
	}
	return s.Client.Main.MsgConv.EditToMatrix(ctx, portal, intent, s.Client.UserLogin, s.Data.SubMessage, s.Data.PreviousMessage, existing), nil
}

func (s *SlackMessage) GetTimestamp() time.Time {
	switch s.Data.SubType {
	case slack.MsgSubTypeMessageChanged:
		return slackid.ParseSlackTimestamp(s.Data.EventTimestamp)
	default:
		return slackid.ParseSlackTimestamp(s.Data.Timestamp)
	}
}

func (s *SlackMessage) GetID() networkid.MessageID {
	return slackid.MakeMessageID(s.Client.TeamID, s.Data.Channel, s.Data.Timestamp)
}

func (s *SlackMessage) GetTargetMessage() networkid.MessageID {
	switch s.Data.SubType {
	case slack.MsgSubTypeMessageDeleted:
		return slackid.MakeMessageID(s.Client.TeamID, s.Data.Channel, s.Data.DeletedTimestamp)
	case slack.MsgSubTypeMessageChanged:
		// Socket mode events don't have the target timestamp at the top level
		// TODO always just use the submessage timestamp?
		if s.Data.EventTimestamp == s.Data.Timestamp {
			return slackid.MakeMessageID(s.Client.TeamID, s.Data.Channel, s.Data.SubMessage.Timestamp)
		}
		return s.GetID()
	default:
		return ""
	}
}
