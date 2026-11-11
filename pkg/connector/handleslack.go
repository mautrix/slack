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
	"runtime/debug"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"go.mau.fi/util/exerrors"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-slack/pkg/slackid"
)

func (s *SlackClient) HandleSlackEvent(ctx context.Context, rawEvt any) {
	log := s.UserLogin.Log.With().
		Str("action", "handle slack event").
		Type("event_type", rawEvt).
		Logger()
	defer func() {
		if v := recover(); v != nil {
			log.Err(exerrors.RecoverToError(v)).
				Bytes(zerolog.ErrorStackFieldName, debug.Stack()).
				Msg("Panic while handling Slack event")
		}
	}()
	ctx = log.WithContext(ctx)
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
	case *slack.ConnectionErrorEvent:
		cli := s.Client
		if cli == nil || ctx.Err() != nil {
			break
		}
		err := evt.ErrorObj
		if _, authErr := cli.AuthTestContext(ctx); authErr != nil {
			err = fmt.Errorf("%w (auth check: %w)", err, authErr)
		}
		s.handleConnectError(ctx, err, evt.Attempt)
	case *slack.ConnectedEvent:
		log.Debug().Msg("Connected to websocket, waiting for hello event")
	case *slack.DisconnectedEvent:
		omitBridgeState := s.UserLogin.BridgeState.GetPrevUnsent().StateEvent == status.StateBadCredentials
		if evt.Intentional {
			log.Debug().Bool("intentional", evt.Intentional).Err(evt.Cause).Msg("Disconnected from Slack")
		} else {
			log.Warn().Bool("intentional", evt.Intentional).Err(evt.Cause).Msg("Disconnected from Slack")
			if !omitBridgeState {
				s.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateTransientDisconnect, Error: "slack-rtm-disconnected"})
			}
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
		reconnected := s.rtmHelloReceived.Swap(true)
		log.Debug().
			Bool("reconnected", reconnected).
			Bool("resume", evt.Resume).
			Msg("Received hello event from websocket (now really connected)")
		s.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
		if reconnected && !evt.Resume {
			go s.catchUpAfterReconnect(ctx)
		}
	case *slack.InvalidAuthEvent:
		s.invalidateSession(ctx, status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      "slack-invalid-auth",
		})
	case *slack.FatalConnectionErrorEvent:
		s.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateUnknownError, Error: "slack-rtm-fatal-error"})
	case *slack.RTMError:
		log.Err(evt).Msg("Got RTM error")
		s.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateUnknownError,
			Error:      "slack-rtm-error",
			Info:       map[string]any{"go_error": evt.Error()},
		})
	case *slack.MessageEvent, *slack.ReactionAddedEvent, *slack.ReactionRemovedEvent, *slack.UserTypingEvent,
		*slack.ChannelMarkedEvent, *slack.IMMarkedEvent, *slack.GroupMarkedEvent, *slack.ThreadMarkedEvent,
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
	case *slack.FileCreatedEvent:
		if evt.UserID == s.UserID {
			ch, _ := s.fileCreatedListeners.Swap(evt.FileID, nil)
			if ch != nil {
				log.Debug().Str("file_id", evt.FileID).Msg("Got file_created event from Slack, notifying listener")
				close(ch)
			} else {
				log.Debug().Str("file_id", evt.FileID).Msg("Got file_created event from Slack without listener")
				go func() {
					time.Sleep(30 * time.Second)
					s.fileCreatedListeners.Delete(evt.FileID)
				}()
			}
		}
	case *slack.IMCreatedEvent:
		if s.Main.Config.DMOnly {
			s.addDMChannelIDUnchecked(evt.Channel.ID)
		}
	case *slack.FileSharedEvent, *slack.FilePublicEvent, *slack.FilePrivateEvent,
		*slack.FileChangeEvent, *slack.FileDeletedEvent,
		*slack.DesktopNotificationEvent, *slack.ReconnectUrlEvent, *slack.LatencyReport,
		*slack.SHRoomJoinEvent, *slack.SHRoomLeaveEvent, *slack.SHRoomUpdateEvent, *slack.UserHuddleChangedEvent:
		// ignored intentionally, these are duplicates or do not contain useful information
	case *slack.UserChangeEvent:
		go s.handleUserChange(ctx, &evt.User)
	case *slack.UserProfileChangedEvent:
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

func (s *SlackClient) HandleSocketModeEvent(ctx context.Context, evt socketmode.Event) {
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
			s.HandleSlackEvent(ctx, eaEvt.InnerEvent.Data)
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

func isChannelInfoChangeSubtype(subType string) bool {
	switch subType {
	case slack.MsgSubTypeChannelTopic, slack.MsgSubTypeChannelPurpose, slack.MsgSubTypeChannelName,
		slack.MsgSubTypeGroupTopic, slack.MsgSubTypeGroupPurpose, slack.MsgSubTypeGroupName:
		return true
	default:
		return false
	}
}

func (s *SlackClient) wrapEvent(ctx context.Context, rawEvt any) (bridgev2.RemoteEvent, error) {
	if s.Main.Config.DMOnly {
		switch evt := rawEvt.(type) {
		case *slack.ChannelJoinedEvent:
			s.addDMChannel(&evt.Channel)
		case *slack.GroupJoinedEvent:
			s.addDMChannel(&evt.Channel)
		}
		if channelID := slackEventChannelID(rawEvt); channelID != "" && !s.isDMChannel(channelID) {
			return nil, nil
		}
	}
	var meta simplevent.EventMeta
	var metaErr error
	var wrapped bridgev2.RemoteEvent
	switch evt := rawEvt.(type) {
	case *slack.MessageEvent:
		if evt.SubType == slack.MsgSubTypeMessageChanged && evt.SubMessage.SubType == "huddle_thread" {
			room := evt.SubMessage.Room
			if room == nil || !room.HasEnded || room.DateEnd == 0 || time.Since(time.Unix(room.DateEnd, 0)) > huddleEndMaxAge {
				return nil, nil
			}
			meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, room.CreatedBy, fmt.Sprintf("%d.000000", room.DateEnd))
			return &simplevent.PreConvertedMessage{
				EventMeta: meta.WithType(bridgev2.RemoteEventMessage),
				ID:        slackid.MakeMessageID(s.TeamID, evt.Channel, fmt.Sprintf("%d.000000", room.DateEnd)),
				Data: &bridgev2.ConvertedMessage{
					Parts: []*bridgev2.ConvertedMessagePart{{
						Type: event.EventMessage,
						Content: &event.MessageEventContent{
							MsgType: event.MsgNotice,
							Body:    "Huddle ended",
						},
					}},
				},
			}, metaErr
		}
		switch evt.SubType {
		case slack.MsgSubTypeChannelName, slack.MsgSubTypeGroupName,
			slack.MsgSubTypeChannelTopic, slack.MsgSubTypeGroupTopic,
			slack.MsgSubTypeChannelPurpose, slack.MsgSubTypeGroupPurpose:
			// The room name is formatted from the channel type/privacy, which
			// the rename message doesn't include, so resync the full info.
			// Drop the cache first so GetChatInfo fetches the new name.
			s.invalidateChatInfoCache(evt.Channel)
			meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, "", evt.Timestamp)
			return s.wrapChatResync(meta, "", nil, true), metaErr
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
		if evt.Room != nil && evt.Room.CreatedBy != "" {
			sender = evt.Room.CreatedBy
		}
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, sender, "")
		meta.CreatePortal = true
		meta = meta.WithMoreLogContext(func(c zerolog.Context) zerolog.Context {
			c = c.
				Str("message_ts", evt.Timestamp).
				Str("message_id", string(slackid.MakeMessageID(s.TeamID, evt.Channel, evt.Timestamp))).
				Str("message_sender", sender).
				Str("subtype", evt.SubType).
				Bool("hidden", evt.Hidden)
			if evt.SubMessage != nil {
				c = c.
					Str("sub_message_user", evt.SubMessage.User).
					Str("sub_message_bot_id", evt.SubMessage.BotID).
					Str("sub_message_subtype", evt.SubMessage.SubType)
			}
			return c
		})
		wrapped = s.wrapMessage(meta, evt)

	case *slack.ReactionAddedEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Item.Channel, nil, evt.User, evt.EventTimestamp)
		wrapped = s.wrapReaction(ctx, meta, evt.Reaction, true, evt.Item)
	case *slack.ReactionRemovedEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Item.Channel, nil, evt.User, evt.EventTimestamp)
		wrapped = s.wrapReaction(ctx, meta, evt.Reaction, false, evt.Item)

	case *slack.UserTypingEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, evt.User, "")
		wrapped = &simplevent.Typing{
			EventMeta: meta.WithType(bridgev2.RemoteEventTyping),
			Timeout:   5 * time.Second,
		}

	case *slack.ChannelMarkedEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, s.UserID, evt.Timestamp)
		s.setLastReadCache(evt.Channel, evt.Timestamp)
		wrapped = s.wrapReadReceipt(meta, evt.Channel, evt.Timestamp)
	case *slack.ThreadMarkedEvent:
		if evt.Subscription.LastRead == "" {
			return nil, nil
		}
		meta, metaErr = s.makeEventMeta(ctx, evt.Subscription.Channel, nil, s.UserID, evt.EventTimestamp)
		wrappedReceipt := s.wrapReadReceipt(meta, evt.Subscription.Channel, evt.Subscription.LastRead)
		wrappedReceipt.ThreadRoot = slackid.MakeMessageID(s.TeamID, evt.Subscription.Channel, evt.Subscription.ThreadTimestamp)
		wrapped = wrappedReceipt
	case *slack.IMMarkedEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, s.UserID, evt.Timestamp)
		s.setLastReadCache(evt.Channel, evt.Timestamp)
		wrapped = s.wrapReadReceipt(meta, evt.Channel, evt.Timestamp)
	case *slack.GroupMarkedEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, s.UserID, evt.Timestamp)
		s.setLastReadCache(evt.Channel, evt.Timestamp)
		wrapped = s.wrapReadReceipt(meta, evt.Channel, evt.Timestamp)

	case *slack.ChannelJoinedEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel.ID, &evt.Channel, s.UserID, "")
		meta.CreatePortal = true
		wrapped = wrapMemberChange(meta, meta.Sender, event.MembershipJoin, "")
	case *slack.ChannelLeftEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, s.UserID, evt.Timestamp)
		wrapped = wrapMemberChange(meta, meta.Sender, event.MembershipLeave, event.MembershipJoin)
	case *slack.GroupJoinedEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel.ID, &evt.Channel, s.UserID, "")
		meta.CreatePortal = true
		wrapped = wrapMemberChange(meta, meta.Sender, event.MembershipJoin, "")
	case *slack.GroupLeftEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, s.UserID, evt.Timestamp)
		wrapped = wrapMemberChange(meta, meta.Sender, event.MembershipLeave, event.MembershipJoin)
	case *slack.MemberJoinedChannelEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, evt.User, evt.EventTimestamp)
		wrapped = wrapMemberChange(meta, meta.Sender, event.MembershipJoin, "")
	case *slack.MemberLeftChannelEvent:
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, evt.User, evt.EventTimestamp)
		wrapped = wrapMemberChange(meta, meta.Sender, event.MembershipLeave, event.MembershipJoin)

	case *slack.ChannelUpdateEvent:
		s.invalidateChatInfoCache(evt.Channel)
		meta, metaErr = s.makeEventMeta(ctx, evt.Channel, nil, "", evt.Timestamp)
		wrapped = s.wrapChatResync(meta, "", nil, true)
	}
	return wrapped, metaErr
}

func slackEventChannelID(rawEvt any) string {
	switch evt := rawEvt.(type) {
	case *slack.MessageEvent:
		return evt.Channel
	case *slack.ReactionAddedEvent:
		return evt.Item.Channel
	case *slack.ReactionRemovedEvent:
		return evt.Item.Channel
	case *slack.UserTypingEvent:
		return evt.Channel
	case *slack.ChannelMarkedEvent:
		return evt.Channel
	case *slack.IMMarkedEvent:
		return evt.Channel
	case *slack.GroupMarkedEvent:
		return evt.Channel
	case *slack.ChannelJoinedEvent:
		return evt.Channel.ID
	case *slack.ChannelLeftEvent:
		return evt.Channel
	case *slack.GroupJoinedEvent:
		return evt.Channel.ID
	case *slack.GroupLeftEvent:
		return evt.Channel
	case *slack.MemberJoinedChannelEvent:
		return evt.Channel
	case *slack.MemberLeftChannelEvent:
		return evt.Channel
	case *slack.ChannelUpdateEvent:
		return evt.Channel
	default:
		return ""
	}
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

func (s *SlackClient) wrapReaction(ctx context.Context, meta simplevent.EventMeta, reaction string, add bool, target slack.ReactionItem) *simplevent.Reaction {
	if add {
		meta.Type = bridgev2.RemoteEventReaction
	} else {
		meta.Type = bridgev2.RemoteEventReactionRemove
	}
	emoji, extraContent := s.getReactionInfo(ctx, reaction)
	return &simplevent.Reaction{
		EventMeta:     meta,
		Emoji:         emoji,
		EmojiID:       networkid.EmojiID(reaction),
		ExtraContent:  extraContent,
		TargetMessage: slackid.MakeMessageID(s.TeamID, target.Channel, target.Timestamp),
	}
}

func (s *SlackClient) wrapReadReceipt(meta simplevent.EventMeta, channelID, timestamp string) *simplevent.Receipt {
	var target networkid.MessageID
	if timestamp != "" {
		target = slackid.MakeMessageID(s.TeamID, channelID, timestamp)
	}
	return &simplevent.Receipt{
		EventMeta:           meta.WithType(bridgev2.RemoteEventReadReceipt),
		LastTarget:          target,
		ReadUpTo:            meta.Timestamp,
		ReadUpToStreamOrder: meta.StreamOrder,
	}
}

func wrapMemberChange(meta simplevent.EventMeta, sender bridgev2.EventSender, newMembership, prevMembership event.Membership) *simplevent.ChatInfoChange {
	meta.Type = bridgev2.RemoteEventChatInfoChange
	meta = meta.WithMoreLogContext(func(c zerolog.Context) zerolog.Context {
		return c.
			Any("member_change_target", sender).
			Str("prev_membership", string(prevMembership)).
			Str("new_membership", string(newMembership))
	})
	return &simplevent.ChatInfoChange{
		EventMeta: meta,
		ChatInfoChange: &bridgev2.ChatInfoChange{
			MemberChanges: &bridgev2.ChatMemberList{
				MemberMap: bridgev2.ChatMemberMap{}.Set(bridgev2.ChatMember{
					EventSender:    sender,
					Membership:     newMembership,
					PrevMembership: prevMembership,
				}),
			},
		},
	}
}

func (s *SlackClient) makeEventMeta(ctx context.Context, channelID string, channel *slack.Channel, senderID, timestamp string) (meta simplevent.EventMeta, err error) {
	if channel != nil {
		meta.PortalKey = s.makePortalKey(channel)
	} else if s.Main.br.Config.SplitPortals {
		meta.PortalKey = slackid.MakePortalKey(s.TeamID, channelID, s.UserLogin.ID, true)
	} else {
		meta.PortalKey, err = s.UserLogin.Bridge.FindPortalReceiver(ctx, slackid.MakePortalID(s.TeamID, channelID), s.UserLogin.ID)
		if err != nil {
			err = fmt.Errorf("failed to find portal receiver: %w", err)
			return
		} else if meta.PortalKey.IsEmpty() {
			var ch *slack.Channel
			ch, err = s.fetchChatInfoWithCache(ctx, channelID, false)
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
		meta.Timestamp = slackid.ParseSlackTimestamp(timestamp)
		meta.StreamOrder = meta.Timestamp.UnixMicro()
	}
	meta.LogContext = func(c zerolog.Context) zerolog.Context { return c.Str("event_ts", timestamp) }
	return
}

func (s *SlackClient) wrapMessage(meta simplevent.EventMeta, evt *slack.MessageEvent) bridgev2.RemoteEvent {
	meta.Type = getMessageEventType(&evt.Msg)
	if evt.SubType == slack.MsgSubTypeMessageChanged {
		meta.Timestamp = slackid.ParseSlackTimestamp(evt.EventTimestamp)
	} else {
		meta.Timestamp = slackid.ParseSlackTimestamp(evt.Timestamp)
	}
	meta.StreamOrder = meta.Timestamp.UnixMicro()
	if meta.Type == bridgev2.RemoteEventMessageRemove {
		return &simplevent.MessageRemove{
			EventMeta:     meta,
			TargetMessage: slackid.MakeMessageID(s.TeamID, evt.Channel, evt.DeletedTimestamp),
		}
	}
	msg := &simplevent.Message[*slack.MessageEvent]{
		EventMeta:          meta,
		Data:               evt,
		ID:                 slackid.MakeMessageID(s.TeamID, evt.Channel, evt.Timestamp),
		ConvertMessageFunc: s.convertMessage,
		ConvertEditFunc:    s.convertEdit,
	}
	if len(evt.Files) == 1 {
		msg.TransactionID = networkid.TransactionID(fmt.Sprintf("%s:%s", evt.User, evt.Files[0].ID))
	}
	if meta.Type == bridgev2.RemoteEventEdit {
		msg.TargetMessage = msg.ID
		if evt.EventTimestamp == evt.Timestamp {
			msg.TargetMessage = slackid.MakeMessageID(s.TeamID, evt.Channel, evt.SubMessage.Timestamp)
		}
	}
	return msg
}

func (s *SlackClient) convertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, evt *slack.MessageEvent) (*bridgev2.ConvertedMessage, error) {
	return s.Main.MsgConv.ToMatrix(ctx, portal, intent, s.UserLogin, &evt.Msg), nil
}

func (s *SlackClient) convertEdit(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, evt *slack.MessageEvent) (*bridgev2.ConvertedEdit, error) {
	return s.Main.MsgConv.EditToMatrix(ctx, portal, intent, s.UserLogin, evt.SubMessage, evt.PreviousMessage, existing)
}

const huddleEndMaxAge = 15 * time.Minute

func (s *SlackClient) wrapChatResync(meta simplevent.EventMeta, latestMessage string, preFetchedInfo *slack.Channel, shouldSyncInfo bool) *simplevent.ChatResync {
	return &simplevent.ChatResync{
		EventMeta: meta.WithType(bridgev2.RemoteEventChatResync),
		GetChatInfoFunc: func(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
			if s.Client == nil {
				return nil, bridgev2.ErrNotLoggedIn
			} else if preFetchedInfo != nil {
				wrappedInfo, err := s.wrapChatInfo(ctx, preFetchedInfo, portal.MXID == "")
				if err != nil {
					return nil, fmt.Errorf("failed to wrap chat info: %w", err)
				}
				return wrappedInfo, nil
			} else if !shouldSyncInfo {
				return nil, nil
			}
			return s.GetChatInfo(ctx, portal)
		},
		CheckNeedsBackfillFunc: func(ctx context.Context, latestBridgedMessage *database.Message) (bool, error) {
			if latestBridgedMessage == nil {
				return latestMessage != "" && latestMessage != "0000000000.000000", nil
			}
			_, _, latestBridgedID, _ := slackid.ParseMessageID(latestBridgedMessage.ID)
			return latestBridgedID < latestMessage, nil
		},
	}
}

func getMessageEventType(msg *slack.Msg) bridgev2.RemoteEventType {
	if isChannelInfoChangeSubtype(msg.SubType) {
		return bridgev2.RemoteEventUnknown
	}
	switch msg.SubType {
	case slack.MsgSubTypeMessageChanged:
		return bridgev2.RemoteEventEdit
	case slack.MsgSubTypeMessageDeleted:
		return bridgev2.RemoteEventMessageRemove
	case slack.MsgSubTypeMessageReplied, slack.MsgSubTypeGroupJoin, slack.MsgSubTypeGroupLeave,
		slack.MsgSubTypeChannelJoin, slack.MsgSubTypeChannelLeave:
		return bridgev2.RemoteEventUnknown
	case "", slack.MsgSubTypeMeMessage, slack.MsgSubTypeBotMessage, slack.MsgSubTypeThreadBroadcast, "huddle_thread":
		// Known types
		return bridgev2.RemoteEventMessage
	default:
		// For unknown types with the hidden flag, don't bridge
		if msg.Hidden {
			return bridgev2.RemoteEventUnknown
		}
		return bridgev2.RemoteEventMessage
	}
}
