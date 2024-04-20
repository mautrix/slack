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

package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"
	"go.mau.fi/util/exslices"
	"golang.org/x/exp/slices"
	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/maulogger/v2/maulogadapt"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-slack/config"
	"go.mau.fi/mautrix-slack/database"
	"go.mau.fi/mautrix-slack/msgconv"
	"go.mau.fi/mautrix-slack/msgconv/emoji"
)

type portalMatrixMessage struct {
	evt        *event.Event
	user       *User
	receivedAt time.Time
}

type portalSlackMessage struct {
	evt      any
	userTeam *UserTeam
}

type Portal struct {
	*database.Portal

	Team *Team

	bridge *SlackBridge
	zlog   zerolog.Logger
	// Deprecated
	log log.Logger

	roomCreateLock      sync.Mutex
	encryptLock         sync.Mutex
	backfillLock        sync.Mutex
	forwardBackfillLock sync.Mutex

	matrixMessages chan portalMatrixMessage
	slackMessages  chan portalSlackMessage

	currentlyTyping     []id.UserID
	currentlyTypingLock sync.Mutex

	MsgConv *msgconv.MessageConverter
}

var (
	_ bridge.Portal                    = (*Portal)(nil)
	_ bridge.ReadReceiptHandlingPortal = (*Portal)(nil)
	_ bridge.TypingPortal              = (*Portal)(nil)
	//_ bridge.MembershipHandlingPortal = (*Portal)(nil)
	//_ bridge.MetaHandlingPortal = (*Portal)(nil)
	//_ bridge.DisappearingPortal = (*Portal)(nil)
)

func (portal *Portal) IsEncrypted() bool {
	return portal.Encrypted
}

func (portal *Portal) MarkEncrypted() {
	portal.Encrypted = true
	portal.Update(context.TODO())
}

func (portal *Portal) IsNoteToSelf() bool {
	return portal.Type == database.ChannelTypeDM && portal.DMUserID != "" && portal.DMUserID == portal.Receiver
}

func (portal *Portal) shouldSetDMRoomMetadata() bool {
	if portal.Type == database.ChannelTypeDM {
		return portal.bridge.Config.Bridge.PrivateChatPortalMeta == "always" ||
			(portal.IsEncrypted() && portal.bridge.Config.Bridge.PrivateChatPortalMeta != "never") ||
			portal.IsNoteToSelf()
	} else if portal.Type == database.ChannelTypeGroupDM {
		return portal.bridge.Config.Bridge.PrivateChatPortalMeta != "never"
	} else {
		return true
	}
}

func (portal *Portal) ReceiveMatrixEvent(user bridge.User, evt *event.Event) {
	if user.GetPermissionLevel() >= bridgeconfig.PermissionLevelUser /*|| portal.HasRelaybot()*/ {
		portal.matrixMessages <- portalMatrixMessage{user: user.(*User), evt: evt, receivedAt: time.Now()}
	}
}

func (br *SlackBridge) loadPortal(ctx context.Context, dbPortal *database.Portal, key *database.PortalKey) *Portal {
	if dbPortal == nil {
		if key == nil {
			return nil
		}
		// Get team beforehand to ensure it exists in the database
		if br.GetTeamByID(key.TeamID) == nil {
			br.ZLog.Warn().Str("team_id", key.TeamID).Msg("Failed to get team by ID before inserting portal")
		}

		dbPortal = br.DB.Portal.New()
		dbPortal.PortalKey = *key
		err := dbPortal.Insert(ctx)
		if err != nil {
			br.ZLog.Err(err).Stringer("portal_key", dbPortal.PortalKey).Msg("Failed to insert new portal")
			return nil
		}
	}

	portal := br.newPortal(dbPortal)
	br.portalsByID[portal.PortalKey] = portal
	if portal.MXID != "" {
		br.portalsByMXID[portal.MXID] = portal
	}

	return portal
}

func (br *SlackBridge) newPortal(dbPortal *database.Portal) *Portal {
	portal := &Portal{
		Portal: dbPortal,
		bridge: br,

		matrixMessages: make(chan portalMatrixMessage, br.Config.Bridge.PortalMessageBuffer),
		slackMessages:  make(chan portalSlackMessage, br.Config.Bridge.PortalMessageBuffer),

		Team: br.GetTeamByID(dbPortal.TeamID),
	}
	portal.MsgConv = msgconv.New(portal, br.Config.Homeserver.Domain, int(br.MediaConfig.UploadSize))
	portal.updateLogger()
	go portal.messageLoop()
	go portal.slackRepeatTypingUpdater()

	return portal
}

func (portal *Portal) updateLogger() {
	logWith := portal.bridge.ZLog.With().Str("channel_id", portal.ChannelID).Str("team_id", portal.TeamID)
	if portal.MXID != "" {
		logWith = logWith.Stringer("mxid", portal.MXID)
	}
	portal.zlog = logWith.Logger()
	portal.log = maulogadapt.ZeroAsMau(&portal.zlog)
}

func (br *SlackBridge) GetPortalByMXID(mxid id.RoomID) *Portal {
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()

	portal, ok := br.portalsByMXID[mxid]
	if !ok {
		ctx := context.TODO()
		dbPortal, err := br.DB.Portal.GetByMXID(ctx, mxid)
		if err != nil {
			br.ZLog.Err(err).Stringer("mxid", mxid).Msg("Failed to get portal by MXID")
			return nil
		}
		return br.loadPortal(ctx, dbPortal, nil)
	}

	return portal
}

func (br *SlackBridge) GetPortalByID(key database.PortalKey) *Portal {
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()

	portal, ok := br.portalsByID[key]
	if !ok {
		ctx := context.TODO()
		dbPortal, err := br.DB.Portal.GetByID(ctx, key)
		if err != nil {
			br.ZLog.Err(err).Stringer("key", key).Msg("Failed to get portal by ID")
			return nil
		}
		return br.loadPortal(ctx, dbPortal, &key)
	}

	return portal
}

func (br *SlackBridge) GetAllPortals() []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.GetAll(context.TODO()))
}

func (br *SlackBridge) GetAllIPortals() (iportals []bridge.Portal) {
	portals := br.GetAllPortals()
	iportals = make([]bridge.Portal, len(portals))
	for i, portal := range portals {
		iportals[i] = portal
	}
	return iportals
}

func (br *SlackBridge) GetAllPortalsForUserTeam(utk database.UserTeamMXIDKey) []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.GetAllForUserTeam(context.TODO(), utk))
}

func (br *SlackBridge) GetDMPortalsWith(otherUserKey database.UserTeamKey) []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.FindPrivateChatsWith(context.TODO(), otherUserKey))
}

func (br *SlackBridge) dbPortalsToPortals(dbPortals []*database.Portal, err error) []*Portal {
	if err != nil {
		br.ZLog.Err(err).Msg("Failed to load portals")
		return nil
	}
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()

	output := make([]*Portal, len(dbPortals))
	for i, dbPortal := range dbPortals {
		portal, ok := br.portalsByID[dbPortal.PortalKey]
		if ok {
			output[i] = portal
		} else {
			output[i] = br.loadPortal(context.TODO(), dbPortal, nil)
		}
	}

	return output
}

func (portal *Portal) messageLoop() {
	for {
		select {
		case msg := <-portal.matrixMessages:
			portal.handleMatrixMessages(msg)
		case msg := <-portal.slackMessages:
			portal.handleSlackEvent(msg.userTeam, msg.evt)
		}
	}
}

func (portal *Portal) IsPrivateChat() bool {
	return portal.Type == database.ChannelTypeDM
}

func (portal *Portal) GetDMPuppet() *Puppet {
	if portal.IsPrivateChat() && portal.DMUserID != "" {
		return portal.Team.GetPuppetByID(portal.DMUserID)
	}
	return nil
}

func (portal *Portal) MainIntent() *appservice.IntentAPI {
	dmPuppet := portal.GetDMPuppet()
	if dmPuppet != nil {
		return dmPuppet.IntentFor(portal)
	}
	return portal.bridge.Bot
}

func (portal *Portal) GetEncryptionEventContent() (evt *event.EncryptionEventContent) {
	evt = &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}
	if rot := portal.bridge.Config.Bridge.Encryption.Rotation; rot.EnableCustom {
		evt.RotationPeriodMillis = rot.Milliseconds
		evt.RotationPeriodMessages = rot.Messages
	}
	return
}

func (portal *Portal) CreateMatrixRoom(ctx context.Context, source *UserTeam, channel *slack.Channel) error {
	portal.roomCreateLock.Lock()
	defer portal.roomCreateLock.Unlock()
	if portal.MXID != "" {
		return nil
	}

	var invite []id.UserID
	channel, invite = portal.UpdateInfo(ctx, source, channel, true)
	if channel == nil {
		return fmt.Errorf("didn't find channel metadata")
	} else if portal.Type == database.ChannelTypeUnknown {
		return fmt.Errorf("unknown channel type")
	} else if portal.Type == database.ChannelTypeGroupDM && len(channel.Members) == 0 {
		return fmt.Errorf("group DM has no members")
	} else if portal.Type == database.ChannelTypeDM && portal.DMUserID == "" {
		return fmt.Errorf("other user in DM not known")
	}
	// Clear invite list for private chats, we don't want to invite the room creator
	if portal.IsPrivateChat() {
		invite = []id.UserID{}
	}

	portal.zlog.Info().Msg("Creating Matrix room for channel")

	intent := portal.MainIntent()
	if err := intent.EnsureRegistered(ctx); err != nil {
		return err
	}

	bridgeInfoStateKey, bridgeInfo := portal.getBridgeInfo()
	initialState := []*event.Event{{
		Type:     event.StateBridge,
		Content:  event.Content{Parsed: bridgeInfo},
		StateKey: &bridgeInfoStateKey,
	}, {
		// TODO remove this once https://github.com/matrix-org/matrix-doc/pull/2346 is in spec
		Type:     event.StateHalfShotBridge,
		Content:  event.Content{Parsed: bridgeInfo},
		StateKey: &bridgeInfoStateKey,
	}, {
		Type: event.StateSpaceParent,
		Content: event.Content{Parsed: &event.SpaceParentEventContent{
			Via:       []string{portal.bridge.Config.Homeserver.Domain},
			Canonical: true,
		}},
		StateKey: (*string)(&portal.Team.MXID),
	}}

	creationContent := make(map[string]any)
	if !portal.bridge.Config.Bridge.FederateRooms {
		creationContent["m.federate"] = false
	}
	avatarSet := false
	if !portal.AvatarMXC.IsEmpty() && portal.shouldSetDMRoomMetadata() {
		avatarSet = true
		initialState = append(initialState, &event.Event{
			Type:    event.StateRoomAvatar,
			Content: event.Content{Parsed: &event.RoomAvatarEventContent{URL: portal.AvatarMXC}},
		})
	}

	if portal.bridge.Config.Bridge.Encryption.Default {
		initialState = append(initialState, &event.Event{
			Type: event.StateEncryption,
			Content: event.Content{
				Parsed: portal.GetEncryptionEventContent(),
			},
		})
		portal.Encrypted = true

		if portal.IsPrivateChat() {
			invite = append(invite, portal.bridge.Bot.UserID)
		}
	}

	autoJoinInvites := portal.bridge.SpecVersions.Supports(mautrix.BeeperFeatureAutojoinInvites)
	if autoJoinInvites {
		portal.zlog.Debug().Msg("Hungryserv mode: adding all group members in create request")
		if !slices.Contains(invite, source.UserMXID) {
			invite = append(invite, source.UserMXID)
		}
	}
	req := &mautrix.ReqCreateRoom{
		Visibility:            "private",
		Name:                  portal.Name,
		Topic:                 portal.Topic,
		Invite:                invite,
		Preset:                "private_chat",
		IsDirect:              portal.IsPrivateChat(),
		InitialState:          initialState,
		CreationContent:       creationContent,
		BeeperAutoJoinInvites: autoJoinInvites,
	}
	if !portal.shouldSetDMRoomMetadata() {
		req.Name = ""
	}
	resp, err := intent.CreateRoom(ctx, req)
	if err != nil {
		portal.log.Warnln("Failed to create room:", err)
		return err
	}
	portal.forwardBackfillLock.Lock()

	portal.NameSet = req.Name != ""
	portal.AvatarSet = avatarSet
	portal.TopicSet = true
	portal.MXID = resp.RoomID
	portal.MoreToBackfill = true
	portal.bridge.portalsLock.Lock()
	portal.bridge.portalsByMXID[portal.MXID] = portal
	portal.bridge.portalsLock.Unlock()
	portal.updateLogger()
	portal.zlog.Info().Msg("Matrix room created")
	portal.Team.AddPortalToSpace(ctx, portal)

	err = portal.Update(ctx)
	if err != nil {
		portal.zlog.Err(err).Msg("Failed to save portal after creating Matrix room")
	}

	if portal.Encrypted && portal.IsPrivateChat() {
		err = portal.bridge.Bot.EnsureJoined(ctx, portal.MXID, appservice.EnsureJoinedParams{BotOverride: portal.MainIntent().Client})
		if err != nil {
			portal.zlog.Err(err).Msg("Failed to ensure bridge bot is joined to private chat portal")
		}
	}

	// We set the memberships beforehand to make sure the encryption key exchange in initial backfill knows the users are here.
	inviteMembership := event.MembershipInvite
	if autoJoinInvites {
		inviteMembership = event.MembershipJoin
	}
	for _, userID := range invite {
		portal.bridge.StateStore.SetMembership(ctx, portal.MXID, userID, inviteMembership)
	}

	if !autoJoinInvites {
		portal.ensureUserInvited(ctx, source.User)
		for _, mxid := range invite {
			puppet := portal.bridge.GetPuppetByMXID(mxid)
			if puppet != nil {
				err = puppet.IntentFor(portal).EnsureJoined(ctx, portal.MXID)
				if err != nil {
					portal.zlog.Err(err).Stringer("puppet_mxid", mxid).Msg("Failed to ensure puppet is joined to portal")
				}
			}
		}
	}
	if portal.Type == database.ChannelTypeChannel {
		source.User.updateChatMute(ctx, portal, true)
	}

	go portal.PostCreateForwardBackfill(ctx, source)
	/*if portal.bridge.Config.Bridge.Backfill.Enable {
		portal.log.Debugln("Performing initial backfill batch")
		initialMessages, err := source.Client.GetConversationHistory(&slack.GetConversationHistoryParameters{
			ChannelID: portal.ChannelID,
			Inclusive: true,
			Limit:     portal.bridge.Config.Bridge.Backfill.ImmediateMessages,
		})
		backfillState := portal.bridge.DB.Backfill.New()
		backfillState.Portal = portal.PortalKey
		if err != nil {
			portal.log.Errorfln("Error fetching initial backfill messages: %v", err)
			backfillState.BackfillComplete = true
		} else {
			resp, err := portal.backfill(source, initialMessages.Messages, true)
			if err != nil {
				portal.log.Errorfln("Error sending initial backfill batch: %v", err)
			}
			if resp != nil {
				backfillState.ImmediateComplete = true
				backfillState.MessageCount += len(initialMessages.Messages)
			} else {
				backfillState.BackfillComplete = true
			}
		}
		portal.log.Debugln("Enqueueing backfill")
		backfillState.Upsert(ctx)
		portal.bridge.BackfillQueue.ReCheck()
	}*/

	return nil
}

func (portal *Portal) getChannelMembers(source *UserTeam, limit int) (output []string) {
	var cursor string
	for limit > 0 {
		chunkLimit := limit
		if chunkLimit > 200 {
			chunkLimit = 100
		}
		membersChunk, nextCursor, err := source.Client.GetUsersInConversation(&slack.GetUsersInConversationParameters{
			ChannelID: portal.ChannelID,
			Limit:     limit,
			Cursor:    cursor,
		})
		if err != nil {
			portal.zlog.Err(err).Msg("Failed to get channel members")
			break
		}
		output = append(output, membersChunk...)
		cursor = nextCursor
		limit -= len(membersChunk)
		if nextCursor == "" || len(membersChunk) < chunkLimit {
			break
		}
	}
	return
}

func (portal *Portal) ensureUserInvited(ctx context.Context, user *User) bool {
	return user.ensureInvited(ctx, portal.MainIntent(), portal.MXID, portal.IsPrivateChat())
}

func (portal *Portal) encrypt(ctx context.Context, intent *appservice.IntentAPI, content *event.Content, eventType event.Type) (event.Type, error) {
	if !portal.Encrypted || portal.bridge.Crypto == nil {
		return eventType, nil
	}
	intent.AddDoublePuppetValue(content)
	// TODO maybe the locking should be inside mautrix-go?
	portal.encryptLock.Lock()
	err := portal.bridge.Crypto.Encrypt(ctx, portal.MXID, eventType, content)
	portal.encryptLock.Unlock()
	if err != nil {
		return eventType, fmt.Errorf("failed to encrypt event: %w", err)
	}
	return event.EventEncrypted, nil
}

func (portal *Portal) sendMatrixMessage(ctx context.Context, intent *appservice.IntentAPI, eventType event.Type, content *event.MessageEventContent, extraContent map[string]interface{}, timestamp int64) (*mautrix.RespSendEvent, error) {
	wrappedContent := event.Content{Parsed: content, Raw: extraContent}
	var err error
	eventType, err = portal.encrypt(ctx, intent, &wrappedContent, eventType)
	if err != nil {
		return nil, err
	}

	_, _ = intent.UserTyping(ctx, portal.MXID, false, 0)
	return intent.SendMassagedMessageEvent(ctx, portal.MXID, eventType, &wrappedContent, timestamp)
}

func (portal *Portal) handleMatrixMessages(msg portalMatrixMessage) {
	evtTS := time.UnixMilli(msg.evt.Timestamp)
	timings := messageTimings{
		initReceive:  msg.evt.Mautrix.ReceivedAt.Sub(evtTS),
		decrypt:      msg.evt.Mautrix.DecryptionDuration,
		portalQueue:  time.Since(msg.receivedAt),
		totalReceive: time.Since(evtTS),
	}
	ms := metricSender{portal: portal, timings: &timings}
	log := portal.zlog.With().
		Str("action", "handle matrix event").
		Stringer("sender", msg.evt.Sender).
		Str("event_type", msg.evt.Type.Type).
		Logger()
	ctx := log.WithContext(context.TODO())
	ut := msg.user.GetTeam(portal.TeamID)
	if ut == nil || ut.Token == "" || ut.Client == nil {
		portal.log.Warnfln("User %s not logged into team %s", msg.user.MXID, portal.TeamID)
		go ms.sendMessageMetrics(ctx, msg.evt, errUserNotLoggedIn, "Ignoring", true)
		return
	}
	log.UpdateContext(func(c zerolog.Context) zerolog.Context {
		return c.Str("sender_slack_id", ut.UserID)
	})

	switch msg.evt.Type {
	case event.EventMessage, event.EventSticker:
		portal.handleMatrixMessage(ctx, ut, msg.evt, &ms)
	case event.EventRedaction:
		portal.handleMatrixRedaction(ctx, ut, msg.evt)
	case event.EventReaction:
		portal.handleMatrixReaction(ctx, ut, msg.evt, &ms)
	default:
		portal.log.Debugln("unknown event type", msg.evt.Type)
	}
}

func (portal *Portal) handleMatrixMessage(ctx context.Context, sender *UserTeam, evt *event.Event, ms *metricSender) {
	log := zerolog.Ctx(ctx)
	ctx = context.WithValue(ctx, convertContextKeySource, sender)
	start := time.Now()
	sendOpts, fileUpload, threadID, editTarget, err := portal.MsgConv.ToSlack(ctx, evt)
	ms.timings.convert = time.Since(start)

	start = time.Now()
	var timestamp string
	if err != nil {
		go ms.sendMessageMetrics(ctx, evt, err, "Error converting", true)
		return
	} else if sendOpts != nil {
		log.Debug().Msg("Sending message to Slack")
		_, timestamp, err = sender.Client.PostMessageContext(
			ctx,
			portal.ChannelID,
			slack.MsgOptionAsUser(true),
			sendOpts)
		if err != nil {
			go ms.sendMessageMetrics(ctx, evt, err, "Error sending", true)
			return
		}
	} else if fileUpload != nil {
		log.Debug().Msg("Uploading attachment to Slack")
		file, err := sender.Client.UploadFileContext(ctx, *fileUpload)
		if err != nil {
			log.Err(err).Msg("Failed to upload attachment to Slack")
			go ms.sendMessageMetrics(ctx, evt, errMediaSlackUploadFailed, "Error uploading", true)
			return
		}
		var shareInfo slack.ShareFileInfo
		// Slack puts the channel message info after uploading a file in either file.shares.private or file.shares.public
		if info, found := file.Shares.Private[portal.ChannelID]; found && len(info) > 0 {
			shareInfo = info[0]
		} else if info, found = file.Shares.Public[portal.ChannelID]; found && len(info) > 0 {
			shareInfo = info[0]
		} else {
			go ms.sendMessageMetrics(ctx, evt, errMediaSlackUploadFailed, "Error uploading", true)
			return
		}
		timestamp = shareInfo.Ts
	}
	ms.timings.totalSend = time.Since(start)
	go ms.sendMessageMetrics(ctx, evt, err, "Error sending", true)
	if timestamp != "" && editTarget == nil {
		dbMsg := portal.bridge.DB.Message.New()
		dbMsg.PortalKey = portal.PortalKey
		dbMsg.MessageID = timestamp
		dbMsg.MXID = evt.ID
		dbMsg.AuthorID = sender.UserID
		dbMsg.ThreadID = threadID
		err = dbMsg.Insert(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to insert message to database")
		}
	}
}

func (portal *Portal) handleMatrixReaction(ctx context.Context, sender *UserTeam, evt *event.Event, ms *metricSender) {
	log := zerolog.Ctx(ctx)
	reaction := evt.Content.AsReaction()
	if reaction.RelatesTo.Type != event.RelAnnotation {
		log.Warn().Msg("Ignoring reaction due to unknown m.relates_to data")
		ms.sendMessageMetrics(ctx, evt, errUnexpectedRelatesTo, "Error sending", true)
		return
	}

	var emojiID string
	if strings.HasPrefix(reaction.RelatesTo.Key, "mxc://") {
		uri, err := id.ParseContentURI(reaction.RelatesTo.Key)
		if err == nil {
			customEmoji, err := portal.bridge.DB.Emoji.GetByMXC(ctx, uri)
			if err != nil {
				log.Err(err).Msg("Failed to get custom emoji from database")
			} else if customEmoji != nil {
				emojiID = customEmoji.EmojiID
			}
		}
	} else {
		emojiID = emoji.UnicodeToShortcodeMap[reaction.RelatesTo.Key]
	}

	if emojiID == "" {
		log.Warn().Str("reaction_key", reaction.RelatesTo.Key).Msg("Couldn't find shortcode for reaction emoji")
		ms.sendMessageMetrics(ctx, evt, errEmojiShortcodeNotFound, "Error sending", true)
		return
	}

	msg, err := portal.bridge.DB.Message.GetByMXID(ctx, reaction.RelatesTo.EventID)
	if err != nil {
		log.Err(err).Msg("Failed to get reaction target message from database")
		// TODO log and metrics
		return
	} else if msg == nil || msg.PortalKey != portal.PortalKey {
		log.Warn().Msg("Reaction target message not found")
		ms.sendMessageMetrics(ctx, evt, errReactionTargetNotFound, "Error sending", true)
		return
	}
	log.UpdateContext(func(c zerolog.Context) zerolog.Context {
		return c.Str("target_message_id", msg.MessageID)
	})

	existingReaction, err := portal.bridge.DB.Reaction.GetBySlackID(ctx, portal.PortalKey, msg.MessageID, sender.UserID, emojiID)
	if err != nil {
		log.Err(err).Msg("Failed to check if reaction already exists")
	} else if existingReaction != nil {
		log.Debug().Msg("Ignoring duplicate reaction")
		ms.sendMessageMetrics(ctx, evt, errDuplicateReaction, "Ignoring", true)
		return
	}

	err = sender.Client.AddReactionContext(ctx, emojiID, slack.ItemRef{
		Channel:   msg.ChannelID,
		Timestamp: msg.MessageID,
	})
	ms.sendMessageMetrics(ctx, evt, err, "Error sending", true)
	if err != nil {
		log.Err(err).Msg("Failed to send reaction")
		return
	}

	dbReaction := portal.bridge.DB.Reaction.New()
	dbReaction.PortalKey = portal.PortalKey
	dbReaction.MXID = evt.ID
	dbReaction.MessageID = msg.MessageID
	dbReaction.MessageFirstPart = msg.Part
	dbReaction.AuthorID = sender.UserID
	dbReaction.EmojiID = emojiID
	err = dbReaction.Insert(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to insert reaction into database")
	}
}

func (portal *Portal) handleMatrixRedaction(ctx context.Context, sender *UserTeam, evt *event.Event) {
	portal.log.Debugfln("Received redaction %s from %s", evt.ID, evt.Sender)

	// First look if we're redacting a message
	message, err := portal.bridge.DB.Message.GetByMXID(ctx, evt.Redacts)
	if err != nil {
		// TODO log and metrics
		return
	} else if message != nil {
		if message.PortalKey != portal.PortalKey {
			// TODO log and metrics
			return
		}
		_, _, err := sender.Client.DeleteMessageContext(ctx, message.ChannelID, message.MessageID)
		if err != nil {
			portal.log.Debugfln("Failed to delete slack message %s: %v", message.ChannelID, err)
		} else {
			message.Delete(ctx)
		}
		go portal.sendMessageMetrics(ctx, evt, err, "Error sending", nil)
		return
	}

	// Now check if it's a reaction.
	reaction, err := portal.bridge.DB.Reaction.GetByMXID(ctx, evt.Redacts)
	if err != nil {
		// TODO log and metrics
		return
	} else if reaction != nil {
		if reaction.PortalKey != portal.PortalKey {
			// TODO log and metrics
			return
		}
		err = sender.Client.RemoveReactionContext(ctx, reaction.EmojiID, slack.ItemRef{
			Channel:   portal.ChannelID,
			Timestamp: reaction.MessageID,
		})
		if err != nil && err.Error() != "no_reaction" {
			portal.log.Debugfln("Failed to delete reaction %s for message %s: %v", reaction.EmojiID, reaction.MessageID, err)
		} else if err != nil && err.Error() == "no_reaction" {
			portal.log.Warnfln("Didn't delete Slack reaction %s for message %s: reaction doesn't exist on Slack", reaction.EmojiID, reaction.MessageID)
			reaction.Delete(ctx)
			err = nil // not reporting an error for this
		} else {
			reaction.Delete(ctx)
		}
		go portal.sendMessageMetrics(ctx, evt, err, "Error sending", nil)
		return
	}

	portal.log.Warnfln("Failed to redact %s@%s: no event found", portal.PortalKey, evt.Redacts)
	go portal.sendMessageMetrics(ctx, evt, errReactionTargetNotFound, "Error sending", nil)
}

func (portal *Portal) HandleMatrixReadReceipt(sender bridge.User, eventID id.EventID, receipt event.ReadReceipt) {
	portal.handleMatrixReadReceipt(sender.(*User).GetTeam(portal.TeamID), eventID)
}

func (portal *Portal) handleMatrixReadReceipt(user *UserTeam, eventID id.EventID) {
	if user == nil || user.Client == nil {
		// TODO log
		return
	}
	ctx := context.TODO()

	message, err := portal.bridge.DB.Message.GetByMXID(ctx, eventID)
	if err != nil {
		// TODO log
		return
	} else if message == nil {
		// TODO log
		return
	}

	err = user.Client.MarkConversationContext(ctx, portal.ChannelID, message.MessageID)
	// TODO log errors and successes
}

func typingDiff(prev, new []id.UserID) (started []id.UserID) {
OuterNew:
	for _, userID := range new {
		for _, previousUserID := range prev {
			if userID == previousUserID {
				continue OuterNew
			}
		}
		started = append(started, userID)
	}
	return
}

func (portal *Portal) HandleMatrixTyping(newTyping []id.UserID) {
	portal.currentlyTypingLock.Lock()
	defer portal.currentlyTypingLock.Unlock()
	startedTyping := typingDiff(portal.currentlyTyping, newTyping)
	portal.currentlyTyping = newTyping
	for _, userID := range startedTyping {
		userTeam := portal.Team.GetCachedUserByMXID(userID)
		if userTeam != nil && userTeam.RTM != nil {
			userTeam.RTM.SendMessage(userTeam.RTM.NewTypingMessage(portal.ChannelID))
		}
	}
}

func (portal *Portal) slackRepeatTypingUpdater() {
	for {
		time.Sleep(3 * time.Second)
		portal.sendSlackRepeatTyping()
	}
}

func (portal *Portal) sendSlackRepeatTyping() {
	portal.currentlyTypingLock.Lock()
	defer portal.currentlyTypingLock.Unlock()

	for _, userID := range portal.currentlyTyping {
		userTeam := portal.Team.GetCachedUserByMXID(userID)
		if userTeam != nil && userTeam.RTM != nil {
			userTeam.RTM.SendMessage(userTeam.RTM.NewTypingMessage(portal.ChannelID))
		}
	}
}

func (portal *Portal) HandleMatrixLeave(brSender bridge.User) {
	portal.log.Debugln("User left private chat portal, cleaning up and deleting...")
	portal.CleanupIfEmpty(portal.zlog.WithContext(context.TODO()))
}

func (portal *Portal) DeleteUser(ctx context.Context, userTeam *UserTeam) {
	err := portal.Portal.DeleteUser(ctx, userTeam.UserTeamMXIDKey)
	if err != nil {
		portal.zlog.Err(err).Object("user_team_key", userTeam.UserTeamMXIDKey).
			Msg("Failed to delete user portal row from database")
	}

	if portal.MXID == "" {
		return
	}

	puppet := portal.bridge.GetPuppetByID(userTeam.UserTeamKey)
	if userTeam.User.DoublePuppetIntent != nil {
		_, err = userTeam.User.DoublePuppetIntent.LeaveRoom(ctx, portal.MXID, &mautrix.ReqLeave{
			Reason: "User left channel",
		})
		if err != nil {
			portal.zlog.Err(err).Stringer("user_mxid", userTeam.UserMXID).
				Msg("Failed to leave room with double puppet")
		}
	} else {
		_, err = portal.MainIntent().KickUser(ctx, portal.MXID, &mautrix.ReqKickUser{
			Reason: "User left channel",
			UserID: userTeam.UserMXID,
		})
		if err != nil {
			portal.zlog.Err(err).Stringer("user_mxid", userTeam.UserMXID).
				Msg("Failed to kick user")
		}

		_, err = puppet.DefaultIntent().LeaveRoom(ctx, portal.MXID, &mautrix.ReqLeave{
			Reason: "User left channel",
		})
		if err != nil {
			portal.zlog.Err(err).Stringer("ghost_mxid", puppet.MXID).
				Msg("Failed to leave room with ghost")
		}
	}

	portal.CleanupIfEmpty(ctx)
}

func (portal *Portal) Delete(ctx context.Context) {
	err := portal.Portal.Delete(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to delete portal from database")
	}
	portal.bridge.portalsLock.Lock()
	delete(portal.bridge.portalsByID, portal.PortalKey)
	if portal.MXID != "" {
		delete(portal.bridge.portalsByMXID, portal.MXID)
	}
	portal.bridge.portalsLock.Unlock()
}

func (portal *Portal) CleanupIfEmpty(ctx context.Context) {
	users, err := portal.getMatrixUsers(ctx)
	if err != nil && !errors.Is(err, mautrix.MForbidden) {
		portal.log.Errorfln("Failed to get Matrix user list to determine if portal needs to be cleaned up: %v", err)
		return
	}

	if len(users) == 0 {
		portal.log.Infoln("Room seems to be empty, cleaning up...")
		portal.Delete(ctx)
		portal.Cleanup(ctx)
	}
}

func (portal *Portal) Cleanup(ctx context.Context) {
	portal.bridge.cleanupRoom(ctx, portal.MainIntent(), portal.MXID, false)
}

func (br *SlackBridge) cleanupRoom(ctx context.Context, intent *appservice.IntentAPI, mxid id.RoomID, puppetsOnly bool) {
	log := zerolog.Ctx(ctx)
	if br.SpecVersions.Supports(mautrix.BeeperFeatureRoomYeeting) {
		err := intent.BeeperDeleteRoom(ctx, mxid)
		if err == nil || errors.Is(err, mautrix.MNotFound) {
			return
		}
		log.Err(err).Msg("Failed to delete room using hungryserv yeet endpoint, falling back to normal behavior")
	}

	members, err := intent.JoinedMembers(ctx, mxid)
	if err != nil {
		log.Err(err).Msg("Failed to get portal members for cleanup")
		return
	}

	for member := range members.Joined {
		if member == intent.UserID {
			continue
		}

		puppet := br.GetPuppetByMXID(member)
		if puppet != nil {
			_, err = puppet.DefaultIntent().LeaveRoom(ctx, mxid)
			if err != nil {
				log.Err(err).Stringer("ghost_mxid", mxid).Msg("Failed to leave room with ghost while cleaning up portal")
			}
		} else if !puppetsOnly {
			_, err = intent.KickUser(ctx, mxid, &mautrix.ReqKickUser{UserID: member, Reason: "Deleting portal"})
			if err != nil {
				log.Err(err).Stringer("user_mxid", mxid).Msg("Failed to kick user while cleaning up portal")
			}
		}
	}

	_, err = intent.LeaveRoom(ctx, mxid)
	if err != nil {
		log.Err(err).Msg("Failed to leave room with main intent while cleaning up portal")
	}
}

func (portal *Portal) getMatrixUsers(ctx context.Context) ([]id.UserID, error) {
	members, err := portal.MainIntent().JoinedMembers(ctx, portal.MXID)
	if err != nil {
		return nil, fmt.Errorf("failed to get member list: %w", err)
	}

	var users []id.UserID
	for userID := range members.Joined {
		_, isPuppet := portal.bridge.ParsePuppetMXID(userID)
		if !isPuppet && userID != portal.bridge.Bot.UserID {
			users = append(users, userID)
		}
	}

	return users, nil
}

func parseSlackTimestamp(timestamp string) time.Time {
	parts := strings.Split(timestamp, ".")

	seconds, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Now().UTC()
	}

	var nanoSeconds int64
	if len(parts) > 1 {
		nsec, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			nanoSeconds = 0
		} else {
			nanoSeconds = nsec
		}
	}

	return time.Unix(seconds, nanoSeconds)
}

func (portal *Portal) getBridgeInfoStateKey() string {
	return fmt.Sprintf("fi.mau.slack://slackgo/%s/%s", portal.TeamID, portal.ChannelID)
}

type CustomBridgeInfoContent struct {
	event.BridgeEventContent
	RoomType string `json:"com.beeper.room_type,omitempty"`
}

func init() {
	event.TypeMap[event.StateBridge] = reflect.TypeOf(CustomBridgeInfoContent{})
	event.TypeMap[event.StateHalfShotBridge] = reflect.TypeOf(CustomBridgeInfoContent{})
}

func (portal *Portal) getBridgeInfo() (string, CustomBridgeInfoContent) {
	bridgeInfo := CustomBridgeInfoContent{
		BridgeEventContent: event.BridgeEventContent{
			BridgeBot: portal.bridge.Bot.UserID,
			Creator:   portal.MainIntent().UserID,
			Protocol: event.BridgeInfoSection{
				ID:          "slackgo",
				DisplayName: "Slack",
				AvatarURL:   portal.bridge.Config.AppService.Bot.ParsedAvatar.CUString(),
				ExternalURL: "https://slack.com/",
			},
			Network: &event.BridgeInfoSection{
				ID:          portal.TeamID,
				DisplayName: portal.Team.Name,
				ExternalURL: portal.Team.URL,
				AvatarURL:   portal.Team.AvatarMXC.CUString(),
			},
			Channel: event.BridgeInfoSection{
				ID:          portal.ChannelID,
				DisplayName: portal.Name,
				ExternalURL: fmt.Sprintf("https://app.slack.com/client/%s/%s", portal.TeamID, portal.ChannelID),
			},
		},
	}
	if portal.Type == database.ChannelTypeDM || portal.Type == database.ChannelTypeGroupDM {
		bridgeInfo.RoomType = "dm"
	}
	return portal.getBridgeInfoStateKey(), bridgeInfo
}

func (portal *Portal) UpdateBridgeInfo(ctx context.Context) {
	if len(portal.MXID) == 0 {
		portal.log.Debugln("Not updating bridge info: no Matrix room created")
		return
	}
	portal.log.Debugln("Updating bridge info...")
	stateKey, content := portal.getBridgeInfo()
	_, err := portal.MainIntent().SendStateEvent(ctx, portal.MXID, event.StateBridge, stateKey, content)
	if err != nil {
		portal.log.Warnln("Failed to update m.bridge:", err)
	}
	// TODO remove this once https://github.com/matrix-org/matrix-doc/pull/2346 is in spec
	_, err = portal.MainIntent().SendStateEvent(ctx, portal.MXID, event.StateHalfShotBridge, stateKey, content)
	if err != nil {
		portal.log.Warnln("Failed to update uk.half-shot.bridge:", err)
	}
}

func (portal *Portal) updateChannelType(channel *slack.Channel) bool {
	var newType database.ChannelType
	if channel.IsMpIM {
		newType = database.ChannelTypeGroupDM
	} else if channel.IsIM {
		newType = database.ChannelTypeDM
	} else if channel.Name != "" {
		newType = database.ChannelTypeChannel
	} else {
		portal.zlog.Warn().Msg("Channel type couldn't be determined")
		return false
	}
	if portal.Type == database.ChannelTypeUnknown {
		portal.zlog.Debug().Stringer("channel_type", newType).Msg("Found channel type")
		portal.Type = newType
	} else if portal.Type != newType {
		portal.zlog.Warn().Stringer("old_type", portal.Type).Stringer("channel_type", newType).Msg("Channel type changed")
		portal.Type = newType
	} else {
		return false
	}
	return true
}

func compareStringFold(a, b string) int {
	for {
		if a == "" {
			if b == "" {
				return 0
			}
			return -1
		} else if b == "" {
			return 1
		}
		aRune, aSize := utf8.DecodeRuneInString(a)
		bRune, bSize := utf8.DecodeRuneInString(b)

		aLower := unicode.ToLower(aRune)
		bLower := unicode.ToLower(bRune)
		if aLower < bLower {
			return -1
		} else if bLower > aLower {
			return 1
		}
		a = a[aSize:]
		b = b[bSize:]
	}
}

func (portal *Portal) GetPlainName(meta *slack.Channel) string {
	switch portal.Type {
	case database.ChannelTypeDM:
		return portal.GetDMPuppet().Name
	case database.ChannelTypeGroupDM:
		puppetNames := make([]string, 0, len(meta.Members))
		for _, member := range meta.Members {
			if member == portal.Receiver {
				continue
			}
			puppet := portal.Team.GetPuppetByID(member)
			puppetNames = append(puppetNames, puppet.Name)
		}
		slices.SortFunc(puppetNames, compareStringFold)
		return strings.Join(puppetNames, ", ")
	default:
		return meta.Name
	}
}

func (portal *Portal) UpdateNameDirect(ctx context.Context, name string) bool {
	if portal.Name == name && (portal.NameSet || portal.MXID == "" || !portal.shouldSetDMRoomMetadata()) {
		return false
	}
	portal.zlog.Debug().Str("old_name", portal.Name).Str("new_name", name).Msg("Updating room name")
	portal.Name = name
	portal.NameSet = false
	if portal.MXID != "" && portal.shouldSetDMRoomMetadata() {
		_, err := portal.MainIntent().SetRoomName(ctx, portal.MXID, portal.Name)
		if err != nil {
			portal.zlog.Err(err).Msg("Failed to update room name")
		} else {
			portal.NameSet = true
		}
	}
	return true
}

func (portal *Portal) UpdateName(ctx context.Context, meta *slack.Channel) bool {
	if (portal.Type == database.ChannelTypeChannel && meta.Name == "") || !portal.shouldSetDMRoomMetadata() {
		return false
	}
	meta.Name = portal.GetPlainName(meta)
	plainNameChanged := portal.PlainName != meta.Name
	portal.PlainName = meta.Name
	formattedName := portal.bridge.Config.Bridge.FormatChannelName(config.ChannelNameParams{
		Channel:      meta,
		Type:         portal.Type,
		TeamName:     portal.Team.Name,
		TeamDomain:   portal.Team.Domain,
		IsNoteToSelf: portal.IsNoteToSelf(),
	})

	return portal.UpdateNameDirect(ctx, formattedName) || plainNameChanged
}

func (portal *Portal) updateRoomAvatar(ctx context.Context) {
	if portal.MXID == "" || !portal.shouldSetDMRoomMetadata() {
		return
	}
	_, err := portal.MainIntent().SetRoomAvatar(ctx, portal.MXID, portal.AvatarMXC)
	if err != nil {
		portal.zlog.Err(err).Msg("Failed to update room avatar")
	} else {
		portal.AvatarSet = true
	}
}

func (portal *Portal) UpdateNameFromPuppet(ctx context.Context, puppet *Puppet) {
	if portal.UpdateNameDirect(ctx, puppet.Name) {
		err := portal.Update(ctx)
		if err != nil {
			portal.zlog.Err(err).Msg("Failed to save portal after updating name")
		}
		portal.UpdateBridgeInfo(ctx)
	}
}

func (portal *Portal) UpdateAvatarFromPuppet(ctx context.Context, puppet *Puppet) {
	if portal.Avatar == puppet.Avatar && portal.AvatarMXC == puppet.AvatarMXC && (portal.AvatarSet || portal.MXID == "" || !portal.shouldSetDMRoomMetadata()) {
		return
	}

	portal.zlog.Debug().Msg("Updating avatar from puppet")
	portal.Avatar = puppet.Avatar
	portal.AvatarMXC = puppet.AvatarMXC
	portal.AvatarSet = false
	portal.updateRoomAvatar(ctx)
	err := portal.Update(ctx)
	if err != nil {
		portal.zlog.Err(err).Msg("Failed to save portal after updating avatar")
	}
	portal.UpdateBridgeInfo(ctx)
}

func (portal *Portal) getTopic(meta *slack.Channel) string {
	switch portal.Type {
	case database.ChannelTypeDM, database.ChannelTypeGroupDM:
		return ""
	case database.ChannelTypeChannel:
		topicParts := make([]string, 0, 2)
		if meta.Topic.Value != "" {
			topicParts = append(topicParts, meta.Topic.Value)
		}
		if meta.Purpose.Value != "" {
			topicParts = append(topicParts, meta.Purpose.Value)
		}
		return strings.Join(topicParts, "\n---\n")
	default:
		return ""
	}
}

func (portal *Portal) UpdateTopic(ctx context.Context, meta *slack.Channel) bool {
	newTopic := portal.getTopic(meta)
	if portal.Topic == newTopic && (portal.TopicSet || portal.MXID == "") {
		return false
	}
	portal.zlog.Debug().Str("old_topic", portal.Topic).Str("new_topic", newTopic).Msg("Updating room topic")
	portal.Topic = newTopic
	portal.TopicSet = false
	if portal.MXID != "" {
		_, err := portal.MainIntent().SetRoomTopic(ctx, portal.MXID, portal.Topic)
		if err != nil {
			portal.zlog.Err(err).Msg("Failed to update room topic")
		} else {
			portal.TopicSet = true
		}
	}
	return true
}

func (portal *Portal) syncParticipants(ctx context.Context, source *UserTeam, participants []string) []id.UserID {
	infosMap := make(map[string]*slack.User)

	for _, participantChunk := range exslices.Chunk(participants, 100) {
		infos, err := source.Client.GetUsersInfoContext(ctx, participantChunk...)
		if err != nil {
			portal.zlog.Err(err).Msg("Failed to get info of participants")
			return nil
		}
		for _, info := range *infos {
			infoCopy := info
			infosMap[info.ID] = &infoCopy
		}
	}

	userIDs := make([]id.UserID, 0, len(participants)+1)
	for _, participant := range participants {
		puppet := portal.Team.GetPuppetByID(participant)
		puppet.UpdateInfo(ctx, source, infosMap[participant], nil)

		user := portal.Team.GetCachedUserByID(participant)
		inviteGhost := false
		if user != nil {
			userIDs = append(userIDs, user.UserMXID)
		}
		if user == nil || user.User.DoublePuppetIntent == nil {
			inviteGhost = true
			userIDs = append(userIDs, puppet.MXID)
		}

		if portal.MXID != "" {
			if user != nil {
				portal.ensureUserInvited(ctx, user.User)
			}
			if inviteGhost {
				if err := puppet.DefaultIntent().EnsureJoined(ctx, portal.MXID); err != nil {
					portal.zlog.Err(err).Str("user_id", participant).Msg("Failed to make ghost of user join portal room")
				}
			}
		}
	}
	return userIDs
}

func (portal *Portal) UpdateInfo(ctx context.Context, source *UserTeam, meta *slack.Channel, syncChannelParticipants bool) (*slack.Channel, []id.UserID) {
	if meta == nil {
		portal.zlog.Debug().Object("via_user_id", source.UserTeamMXIDKey).Msg("Fetching channel meta from server")
		var err error
		meta, err = source.Client.GetConversationInfo(&slack.GetConversationInfoInput{
			ChannelID:         portal.ChannelID,
			IncludeLocale:     true,
			IncludeNumMembers: true,
		})
		if err != nil {
			portal.zlog.Err(err).Msg("Failed to fetch channel meta")
			return nil, nil
		}
	}

	portal.zlog.Trace().Any("channel_info", meta).Msg("Syncing channel")

	changed := portal.updateChannelType(meta)

	if portal.DMUserID == "" && portal.IsPrivateChat() {
		portal.DMUserID = meta.User
		portal.zlog.Info().Str("other_user_id", portal.DMUserID).Msg("Found other user ID")
		changed = true
	}
	if portal.Receiver == "" && ((portal.Type == database.ChannelTypeDM && meta.User == portal.DMUserID) || portal.Type == database.ChannelTypeGroupDM) {
		portal.Receiver = source.UserID
		changed = true
	}

	var memberMXIDs []id.UserID
	switch portal.Type {
	case database.ChannelTypeDM:
		memberMXIDs = portal.syncParticipants(ctx, source, []string{meta.User})
	case database.ChannelTypeGroupDM:
		memberMXIDs = portal.syncParticipants(ctx, source, meta.Members)
	case database.ChannelTypeChannel:
		if syncChannelParticipants && (portal.MXID == "" || !portal.bridge.Config.Bridge.ParticipantSyncOnlyOnCreate) {
			members := portal.getChannelMembers(source, portal.bridge.Config.Bridge.ParticipantSyncCount)
			memberMXIDs = portal.syncParticipants(ctx, source, members)
		}
	default:
	}

	changed = portal.UpdateName(ctx, meta) || changed
	changed = portal.UpdateTopic(ctx, meta) || changed
	changed = portal.Team.AddPortalToSpace(ctx, portal) || changed

	if changed {
		portal.UpdateBridgeInfo(ctx)
		err := portal.Update(ctx)
		if err != nil {
			portal.zlog.Err(err).Msg("Failed to save portal after updating info")
		}
	}

	err := portal.InsertUser(ctx, source.UserTeamMXIDKey, !portal.MoreToBackfill)
	if err != nil {
		portal.zlog.Err(err).Object("user_team_key", source.UserTeamMXIDKey).
			Msg("Failed to insert user portal row")
	}
	if portal.MXID != "" {
		portal.ensureUserInvited(ctx, source.User)
		if meta.Latest != nil {
			go portal.MissedForwardBackfill(ctx, source, meta.Latest.Timestamp)
		}
	}

	return meta, memberMXIDs
}

func (portal *Portal) handleSlackEvent(source *UserTeam, rawEvt any) {
	log := portal.zlog.With().
		Object("source_key", source.UserTeamMXIDKey).
		Type("event_type", rawEvt).
		Logger()
	ctx := log.WithContext(context.TODO())
	switch evt := rawEvt.(type) {
	case *slack.ChannelJoinedEvent, *slack.GroupJoinedEvent:
		var ch *slack.Channel
		if joinedEvt, ok := evt.(*slack.ChannelJoinedEvent); ok {
			ch = &joinedEvt.Channel
		} else {
			ch = &evt.(*slack.GroupJoinedEvent).Channel
		}
		if portal.MXID == "" {
			log.Debug().Msg("Creating Matrix room from joined channel")
			if err := portal.CreateMatrixRoom(ctx, source, ch); err != nil {
				log.Err(err).Msg("Failed to create portal room after join event")
			}
		} else {
			log.Debug().Msg("Syncing Matrix room from joined channel event")
			portal.UpdateInfo(ctx, source, ch, true)
		}
	case *slack.ChannelLeftEvent, *slack.GroupLeftEvent:
		portal.DeleteUser(ctx, source)
	case *slack.ChannelUpdateEvent:
		portal.UpdateInfo(ctx, source, nil, true)
	case *slack.MemberLeftChannelEvent:
		// TODO
	case *slack.MemberJoinedChannelEvent:
		// TODO
	case *slack.UserTypingEvent:
		if portal.MXID == "" {
			log.Warn().Msg("Ignoring typing notification in channel with no portal room")
			return
		}
		log.UpdateContext(func(c zerolog.Context) zerolog.Context {
			return c.Str("user_id", evt.User)
		})
		portal.HandleSlackTyping(ctx, source, evt)
	case *slack.ChannelMarkedEvent:
		if portal.MXID == "" {
			log.Warn().Msg("Ignoring read receipt in channel with no portal room")
			return
		}
		log.UpdateContext(func(c zerolog.Context) zerolog.Context {
			return c.Str("message_id", evt.Timestamp).Str("sender_id", evt.User)
		})
		portal.HandleSlackChannelMarked(ctx, source, evt)
	case *slack.ReactionAddedEvent:
		if portal.MXID == "" {
			log.Warn().Msg("Ignoring reaction removal in channel with no portal room")
			return
		}
		log.UpdateContext(func(c zerolog.Context) zerolog.Context {
			return c.Str("target_message_id", evt.Item.Timestamp).Str("sender_id", evt.User)
		})
		portal.HandleSlackReaction(ctx, source, evt)
	case *slack.ReactionRemovedEvent:
		if portal.MXID == "" {
			log.Warn().Msg("Ignoring reaction removal in channel with no portal room")
			return
		}
		log.UpdateContext(func(c zerolog.Context) zerolog.Context {
			return c.Str("target_message_id", evt.Item.Timestamp).Str("sender_id", evt.User)
		})
		portal.HandleSlackReactionRemoved(ctx, source, evt)
	case *slack.MessageEvent:
		log.UpdateContext(func(c zerolog.Context) zerolog.Context {
			if evt.ThreadTimestamp != "" {
				c = c.Str("thread_id", evt.ThreadTimestamp)
			}
			return c.
				Str("message_id", evt.Timestamp).
				Str("sender_id", evt.User).
				Str("subtype", evt.SubType)
		})
		if portal.MXID == "" {
			log.Warn().Msg("Received message in channel with no portal room creating portal")
			err := portal.CreateMatrixRoom(ctx, source, nil)
			if err != nil {
				log.Err(err).Msg("Failed to create room for portal")
				return
			}
		}
		// Ensure that messages aren't bridged if they come in between the portal being created and forward backfill happening.
		portal.forwardBackfillLock.Lock()
		defer portal.forwardBackfillLock.Unlock()
		portal.HandleSlackMessage(ctx, source, evt)
	}
}

type ConvertedSlackFile struct {
	Event       *event.MessageEventContent
	SlackFileID string
}

type ConvertedSlackMessage struct {
	FileAttachments []ConvertedSlackFile
	Event           *event.MessageEventContent
	SlackTimestamp  string
	SlackThreadTs   string
	SlackAuthor     string
	SlackReactions  []slack.ItemReaction
	SlackThread     []slack.Message
}

func (portal *Portal) HandleSlackMessage(ctx context.Context, source *UserTeam, msg *slack.MessageEvent) {
	log := zerolog.Ctx(ctx)
	if msg.Type != slack.TYPE_MESSAGE {
		log.Warn().Str("message_type", msg.Type).Msg("Ignoring message with unexpected top-level type")
		return
	}
	if msg.IsEphemeral {
		log.Debug().Msg("Ignoring ephemeral message")
		return
	}

	existing, err := portal.bridge.DB.Message.GetBySlackID(ctx, portal.PortalKey, msg.Timestamp)
	if err != nil {
		log.Err(err).Msg("Failed to check if message was already bridged")
		return
	} else if existing != nil && msg.SubType != slack.MsgSubTypeMessageChanged {
		log.Debug().Msg("Dropping duplicate message")
		return
	} else if existing == nil && msg.SubType == slack.MsgSubTypeMessageChanged {
		log.Debug().Msg("Dropping edit for unknown message")
		return
	}
	slackAuthor := msg.User
	if slackAuthor == "" {
		slackAuthor = msg.BotID
	}
	if slackAuthor == "" && msg.SubMessage != nil {
		slackAuthor = msg.SubMessage.User
	}
	var sender *Puppet
	if slackAuthor != "" {
		sender = portal.Team.GetPuppetByID(slackAuthor)
		sender.UpdateInfoIfNecessary(ctx, source)
	}

	log.Debug().Msg("Starting handling of Slack message")

	switch msg.SubType {
	default:
		if sender == nil {
			log.Warn().Msg("Ignoring message from unknown sender")
			return
		}
		switch msg.SubType {
		case "", slack.MsgSubTypeMeMessage, slack.MsgSubTypeBotMessage, slack.MsgSubTypeThreadBroadcast, "huddle_thread":
			// known types
		default:
			log.Warn().Msg("Received unknown message subtype")
		}
		portal.HandleSlackNormalMessage(ctx, source, sender, &msg.Msg)
	case slack.MsgSubTypeMessageChanged:
		if sender == nil {
			log.Warn().Msg("Ignoring edit from unknown sender")
			return
		} else if msg.SubMessage.SubType == "huddle_thread" {
			log.Debug().Msg("Ignoring huddle thread edit")
			return
		}
		portal.HandleSlackEditMessage(ctx, source, sender, msg.SubMessage, msg.PreviousMessage, existing)
	case slack.MsgSubTypeMessageDeleted:
		portal.HandleSlackDelete(ctx, &msg.Msg)
	case slack.MsgSubTypeChannelTopic, slack.MsgSubTypeChannelPurpose, slack.MsgSubTypeChannelName,
		slack.MsgSubTypeGroupTopic, slack.MsgSubTypeGroupPurpose, slack.MsgSubTypeGroupName:
		log.Debug().Msg("Resyncing channel info due to update message")
		portal.UpdateInfo(ctx, source, nil, false)
	case slack.MsgSubTypeMessageReplied, slack.MsgSubTypeGroupJoin, slack.MsgSubTypeGroupLeave,
		slack.MsgSubTypeChannelJoin, slack.MsgSubTypeChannelLeave:
		// These subtypes are simply ignored, because they're handled elsewhere/in other ways (Slack sends multiple info of these events)
		log.Debug().Msg("Ignoring unnecessary message")
	}
}

func (portal *Portal) getThreadMetadata(ctx context.Context, threadTs string) (threadRootID, lastMessageID id.EventID) {
	// fetch thread metadata and add to message
	if threadTs == "" {
		return
	}
	rootThreadMessage, err := portal.bridge.DB.Message.GetFirstPartBySlackID(ctx, portal.PortalKey, threadTs)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get thread root message from database")
	}
	latestThreadMessage, err := portal.bridge.DB.Message.GetLastInThread(ctx, portal.PortalKey, threadTs)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get last message in thread from database")
	}

	if latestThreadMessage != nil && rootThreadMessage != nil {
		threadRootID = rootThreadMessage.MXID
		lastMessageID = latestThreadMessage.MXID
	} else if latestThreadMessage != nil {
		threadRootID = latestThreadMessage.MXID
		lastMessageID = latestThreadMessage.MXID
	} else if rootThreadMessage != nil {
		threadRootID = rootThreadMessage.MXID
		lastMessageID = rootThreadMessage.MXID
	}
	return
}

func (portal *Portal) HandleSlackEditMessage(ctx context.Context, source *UserTeam, sender *Puppet, msg, oldMsg *slack.Msg, editTarget []*database.Message) {
	log := zerolog.Ctx(ctx)
	intent := sender.IntentFor(portal)

	ts := parseSlackTimestamp(msg.Timestamp)
	ctx = context.WithValue(ctx, convertContextKeySource, source)
	ctx = context.WithValue(ctx, convertContextKeyIntent, intent)
	// TODO avoid reuploading files when editing messages
	converted := portal.MsgConv.ToMatrix(ctx, msg)
	// Don't merge caption if there's more than one part in the database
	// (because it means the original didn't have a merged caption)
	if len(editTarget) == 1 && portal.bridge.Config.Bridge.CaptionInMessage {
		converted.MergeCaption()
	}

	editTargetPartMap := make(map[database.PartID]*database.Message, len(editTarget))
	for _, editTargetPart := range editTarget {
		editTargetPartMap[editTargetPart.Part] = editTargetPart
	}
	for _, part := range converted.Parts {
		editTargetPart, ok := editTargetPartMap[part.PartID]
		if !ok {
			log.Warn().Stringer("part_id", &part.PartID).Msg("Failed to find part to edit")
			continue
		}
		delete(editTargetPartMap, part.PartID)
		part.Content.SetEdit(editTargetPart.MXID)
		// Never actually ping users in edits, only update the list in the edited content
		part.Content.Mentions = nil
		if part.Extra != nil {
			part.Extra = map[string]any{
				"m.new_content": part.Extra,
			}
		}
		resp, err := portal.sendMatrixMessage(ctx, intent, part.Type, part.Content, part.Extra, ts.UnixMilli())
		if err != nil {
			log.Err(err).
				Stringer("part_id", &part.PartID).
				Stringer("part_mxid", editTargetPart.MXID).
				Msg("Failed to edit message part")
		} else {
			log.Debug().
				Stringer("part_id", &part.PartID).
				Stringer("part_mxid", editTargetPart.MXID).
				Stringer("edit_mxid", resp.EventID).
				Msg("Edited message part")
		}
	}
	for _, deletedPart := range editTargetPartMap {
		resp, err := portal.MainIntent().RedactEvent(ctx, portal.MXID, deletedPart.MXID, mautrix.ReqRedact{Reason: "Part removed in edit"})
		if err != nil {
			log.Err(err).
				Stringer("part_id", &deletedPart.Part).
				Stringer("part_mxid", deletedPart.MXID).
				Msg("Failed to redact message part deleted in edit")
		} else if err = deletedPart.Delete(ctx); err != nil {
			log.Err(err).
				Stringer("part_id", &deletedPart.Part).
				Msg("Failed to delete message part from database")
		} else {
			log.Debug().
				Stringer("part_id", &deletedPart.Part).
				Stringer("part_mxid", deletedPart.MXID).
				Stringer("redaction_mxid", resp.EventID).
				Msg("Redacted message part that was deleted in edit")
		}
	}
}

func (portal *Portal) HandleSlackDelete(ctx context.Context, msg *slack.Msg) {
	log := zerolog.Ctx(ctx)
	log.UpdateContext(func(c zerolog.Context) zerolog.Context {
		return c.Str("deleted_message_id", msg.DeletedTimestamp)
	})
	messageParts, err := portal.bridge.DB.Message.GetBySlackID(ctx, portal.PortalKey, msg.DeletedTimestamp)
	if err != nil {
		log.Err(err).Msg("Failed to get delete target message")
	} else if messageParts == nil {
		log.Warn().Msg("Received message deletion event for unknown message")
	} else {
		for _, part := range messageParts {
			if _, err = portal.MainIntent().RedactEvent(ctx, portal.MXID, part.MXID); err != nil {
				log.Err(err).
					Stringer("part_mxid", part.MXID).
					Stringer("part_id", &part.Part).
					Msg("Failed to redact deleted message part")
			} else if err = part.Delete(ctx); err != nil {
				log.Err(err).
					Stringer("part_mxid", part.MXID).
					Stringer("part_id", &part.Part).
					Msg("Failed to delete deleted message part from database")
			}
		}
	}
}

func (portal *Portal) HandleSlackNormalMessage(ctx context.Context, source *UserTeam, sender *Puppet, msg *slack.Msg) {
	intent := sender.IntentFor(portal)

	hasThread := msg.ThreadTimestamp != ""
	threadRootID, threadLastMessageID := portal.getThreadMetadata(ctx, msg.ThreadTimestamp)

	ts := parseSlackTimestamp(msg.Timestamp)
	ctx = context.WithValue(ctx, convertContextKeySource, source)
	ctx = context.WithValue(ctx, convertContextKeyIntent, intent)
	converted := portal.MsgConv.ToMatrix(ctx, msg)
	if portal.bridge.Config.Bridge.CaptionInMessage {
		converted.MergeCaption()
	}

	var lastEventID id.EventID
	for _, part := range converted.Parts {
		if threadRootID != "" {
			part.Content.GetRelatesTo().SetThread(threadRootID, threadLastMessageID)
		}
		resp, err := portal.sendMatrixMessage(ctx, intent, part.Type, part.Content, part.Extra, ts.UnixMilli())
		if err != nil {
			portal.log.Warnfln("Failed to send media message %s to matrix: %v", ts, err)
			continue
		}
		dbMessage := portal.bridge.DB.Message.New()
		dbMessage.PortalKey = portal.PortalKey
		dbMessage.MessageID = msg.Timestamp
		dbMessage.Part = part.PartID
		dbMessage.ThreadID = msg.ThreadTimestamp
		dbMessage.AuthorID = sender.UserID
		dbMessage.MXID = resp.EventID
		err = dbMessage.Insert(ctx)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).
				Stringer("part_id", &part.PartID).
				Msg("Failed to insert message part to database")
		}

		if hasThread {
			threadLastMessageID = resp.EventID
			if threadRootID == "" {
				threadRootID = resp.EventID
			}
		}
		lastEventID = resp.EventID
	}
	go portal.sendDeliveryReceipt(ctx, lastEventID)
}

func (portal *Portal) HandleSlackReaction(ctx context.Context, source *UserTeam, msg *slack.ReactionAddedEvent) {
	puppet := portal.Team.GetPuppetByID(msg.User)
	if puppet == nil {
		return
	}
	puppet.UpdateInfoIfNecessary(ctx, source)

	log := zerolog.Ctx(ctx)
	existing, err := portal.bridge.DB.Reaction.GetBySlackID(ctx, portal.PortalKey, msg.Item.Timestamp, msg.User, msg.Reaction)
	if err != nil {
		log.Err(err).Msg("Failed to check if reaction is duplicate")
		return
	} else if existing != nil {
		log.Debug().Msg("Dropping duplicate reaction")
		return
	}
	intent := puppet.IntentFor(portal)

	targetMessage, err := portal.bridge.DB.Message.GetFirstPartBySlackID(ctx, portal.PortalKey, msg.Item.Timestamp)
	if err != nil {
		log.Err(err).Msg("Failed to get reaction target from database")
		return
	} else if targetMessage == nil {
		log.Warn().Msg("Dropping reaction to unknown message")
		return
	}

	key := source.GetEmoji(ctx, msg.Reaction)

	var content event.ReactionEventContent
	content.RelatesTo = event.RelatesTo{
		Type:    event.RelAnnotation,
		EventID: targetMessage.MXID,
		Key:     key,
	}
	extraContent := map[string]any{}
	if strings.HasPrefix(key, "mxc://") {
		extraContent["fi.mau.slack.reaction"] = map[string]any{
			"name": msg.Reaction,
			"mxc":  key,
		}
		extraContent["com.beeper.reaction.shortcode"] = fmt.Sprintf(":%s:", msg.Reaction)
		if !portal.bridge.Config.Bridge.CustomEmojiReactions {
			content.RelatesTo.Key = msg.Reaction
		}
	}

	resp, err := intent.SendMassagedMessageEvent(ctx, portal.MXID, event.EventReaction, &event.Content{
		Parsed: &content,
		Raw:    extraContent,
	}, parseSlackTimestamp(msg.EventTimestamp).UnixMilli())
	if err != nil {
		log.Err(err).Msg("Failed to send Slack reaction to Matrix")
		return
	}

	dbReaction := portal.bridge.DB.Reaction.New()
	dbReaction.PortalKey = portal.PortalKey
	dbReaction.MessageID = msg.Item.Timestamp
	dbReaction.MessageFirstPart = targetMessage.Part
	dbReaction.MXID = resp.EventID
	dbReaction.AuthorID = msg.User
	dbReaction.EmojiID = msg.Reaction
	err = dbReaction.Insert(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to save reaction to database")
	}
}

func (portal *Portal) HandleSlackReactionRemoved(ctx context.Context, source *UserTeam, msg *slack.ReactionRemovedEvent) {
	puppet := portal.Team.GetPuppetByID(msg.User)
	if puppet == nil {
		return
	}
	puppet.UpdateInfoIfNecessary(ctx, source)

	log := zerolog.Ctx(ctx)
	dbReaction, err := portal.bridge.DB.Reaction.GetBySlackID(ctx, portal.PortalKey, msg.Item.Timestamp, msg.User, msg.Reaction)
	if err != nil {
		log.Err(err).Msg("Failed to get removed reaction info from database")
		return
	} else if dbReaction == nil {
		log.Warn().Msg("Ignoring removal of unknown reaction")
		return
	}

	_, err = puppet.IntentFor(portal).RedactEvent(ctx, portal.MXID, dbReaction.MXID)
	if err != nil {
		log.Err(err).Msg("Failed to redact reaction")
	}
	err = dbReaction.Delete(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to remove reaction from database")
	}
}

func (portal *Portal) HandleSlackTyping(ctx context.Context, source *UserTeam, msg *slack.UserTypingEvent) {
	puppet := portal.Team.GetPuppetByID(msg.User)
	if puppet == nil {
		return
	}
	puppet.UpdateInfoIfNecessary(ctx, source)
	log := zerolog.Ctx(ctx)
	intent := puppet.IntentFor(portal)
	err := intent.EnsureJoined(ctx, portal.MXID)
	if err != nil {
		log.Err(err).Msg("Failed to ensure ghost is joined to room to bridge typing notification")
	}
	_, err = intent.UserTyping(ctx, portal.MXID, true, 5*time.Second)
	if err != nil {
		log.Err(err).Msg("Failed to bridge typing notification to Matrix")
	}
}

func (portal *Portal) HandleSlackChannelMarked(ctx context.Context, source *UserTeam, msg *slack.ChannelMarkedEvent) {
	if source.User.DoublePuppetIntent == nil {
		return
	}
	log := zerolog.Ctx(ctx)
	message, err := portal.bridge.DB.Message.GetLastPartBySlackID(ctx, portal.PortalKey, msg.Timestamp)
	if err != nil {
		log.Err(err).Msg("Failed to get read receipt target message")
		return
	} else if message == nil {
		log.Debug().Msg("Ignoring read receipt for unknown message")
		return
	}
	log.UpdateContext(func(c zerolog.Context) zerolog.Context {
		return c.Stringer("message_mxid", message.MXID)
	})
	err = source.User.DoublePuppetIntent.MarkRead(ctx, portal.MXID, message.MXID)
	if err != nil {
		log.Err(err).Msg("Failed to mark message as read on Matrix after Slack read receipt")
	} else {
		log.Debug().Msg("Marked message as read on Matrix after Slack read receipt")
	}
}
