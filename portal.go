// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	log "maunium.net/go/maulogger/v2"

	"github.com/slack-go/slack"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-slack/config"
	"go.mau.fi/mautrix-slack/database"
)

type portalMatrixMessage struct {
	evt        *event.Event
	user       *User
	receivedAt time.Time
}

type Portal struct {
	*database.Portal

	bridge *SlackBridge
	log    log.Logger

	roomCreateLock sync.Mutex
	encryptLock    sync.Mutex

	matrixMessages chan portalMatrixMessage

	slackMessageLock sync.Mutex

	currentlyTyping     []id.UserID
	currentlyTypingLock sync.Mutex
}

func (portal *Portal) IsEncrypted() bool {
	return portal.Encrypted
}

func (portal *Portal) MarkEncrypted() {
	portal.Encrypted = true
	portal.Update()
}

func (portal *Portal) ReceiveMatrixEvent(user bridge.User, evt *event.Event) {
	if user.GetPermissionLevel() >= bridgeconfig.PermissionLevelUser /*|| portal.HasRelaybot()*/ {
		portal.matrixMessages <- portalMatrixMessage{user: user.(*User), evt: evt, receivedAt: time.Now()}
	}
}

func (portal *Portal) HandleMatrixReadReceipt(sender bridge.User, eventID id.EventID, receiptTimestamp time.Time) {
	//portal.handleMatrixReadReceipt(sender.(*User), eventID, receiptTimestamp, true)
	userTeam := sender.(*User).GetUserTeam(portal.Key.TeamID)

	portal.markSlackRead(sender.(*User), userTeam, eventID)
}

func (portal *Portal) markSlackRead(user *User, userTeam *database.UserTeam, eventID id.EventID) {
	if !userTeam.IsConnected() {
		portal.log.Debugfln("Not marking Slack conversation %s as read by %s: not connected to Slack", portal.Key, user.MXID)
		return
	}

	message := portal.bridge.DB.Message.GetByMatrixID(portal.Key, eventID)
	if message == nil {
		portal.log.Debugfln("Not marking Slack channel for portal %s as read: unknown message", portal.Key)
		return
	}

	userTeam.Client.MarkConversation(portal.Key.ChannelID, message.SlackID)
	portal.log.Debugfln("Marked message %s as read by %s in portal %s", message.SlackID, user.MXID, portal.Key)
}

var _ bridge.Portal = (*Portal)(nil)

var (
	portalCreationDummyEvent = event.Type{Type: "fi.mau.dummy.portal_created", Class: event.MessageEventType}
)

func (br *SlackBridge) loadPortal(dbPortal *database.Portal, key *database.PortalKey) *Portal {
	// If we weren't given a portal we'll attempt to create it if a key was
	// provided.
	if dbPortal == nil {
		if key == nil {
			return nil
		}

		dbPortal = br.DB.Portal.New()
		dbPortal.Key = *key
		dbPortal.Insert()
	}

	portal := br.NewPortal(dbPortal)

	// No need to lock, it is assumed that our callers have already acquired
	// the lock.
	br.portalsByID[portal.Key] = portal
	if portal.MXID != "" {
		br.portalsByMXID[portal.MXID] = portal
	}

	return portal
}

func (br *SlackBridge) GetPortalByMXID(mxid id.RoomID) *Portal {
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()

	portal, ok := br.portalsByMXID[mxid]
	if !ok {
		return br.loadPortal(br.DB.Portal.GetByMXID(mxid), nil)
	}

	return portal
}

func (br *SlackBridge) GetPortalByID(key database.PortalKey) *Portal {
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()

	portal, ok := br.portalsByID[key]
	if !ok {
		return br.loadPortal(br.DB.Portal.GetByID(key), &key)
	}

	return portal
}

func (br *SlackBridge) GetAllPortals() []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.GetAll())
}

func (br *SlackBridge) GetAllIPortals() (iportals []bridge.Portal) {
	portals := br.GetAllPortals()
	iportals = make([]bridge.Portal, len(portals))
	for i, portal := range portals {
		iportals[i] = portal
	}
	return iportals
}

func (br *SlackBridge) GetAllPortalsByID(teamID, userID string) []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.GetAllByID(teamID, userID))
}

func (br *SlackBridge) GetDMPortalsWith(otherUserID string) []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.FindPrivateChatsWith(otherUserID))
}

func (br *SlackBridge) dbPortalsToPortals(dbPortals []*database.Portal) []*Portal {
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()

	output := make([]*Portal, len(dbPortals))
	for index, dbPortal := range dbPortals {
		if dbPortal == nil {
			continue
		}

		portal, ok := br.portalsByID[dbPortal.Key]
		if !ok {
			portal = br.loadPortal(dbPortal, nil)
		}

		output[index] = portal
	}

	return output
}

func (br *SlackBridge) NewPortal(dbPortal *database.Portal) *Portal {
	portal := &Portal{
		Portal: dbPortal,
		bridge: br,
		log:    br.Log.Sub(fmt.Sprintf("Portal/%s", dbPortal.Key)),

		matrixMessages: make(chan portalMatrixMessage, br.Config.Bridge.PortalMessageBuffer),
	}

	go portal.messageLoop()
	go portal.slackRepeatTypingUpdater()

	return portal
}

func (portal *Portal) messageLoop() {
	for {
		select {
		case msg := <-portal.matrixMessages:
			portal.handleMatrixMessages(msg)
		}
	}
}

func (portal *Portal) IsPrivateChat() bool {
	return portal.Type == database.ChannelTypeDM
}

func (portal *Portal) MainIntent() *appservice.IntentAPI {
	if portal.IsPrivateChat() && portal.DMUserID != "" {
		return portal.bridge.GetPuppetByID(portal.Key.TeamID, portal.DMUserID).DefaultIntent()
	}

	return portal.bridge.Bot
}

func (portal *Portal) syncParticipants(source *User, sourceTeam *database.UserTeam, participants []string) {
	for _, participant := range participants {
		puppet := portal.bridge.GetPuppetByID(sourceTeam.Key.TeamID, participant)

		puppet.UpdateInfo(sourceTeam, nil)

		user := portal.bridge.GetUserByID(sourceTeam.Key.TeamID, participant)
		if user != nil {
			portal.ensureUserInvited(user)
		}

		if user == nil || !puppet.IntentFor(portal).IsCustomPuppet {
			if err := puppet.IntentFor(portal).EnsureJoined(portal.MXID); err != nil {
				portal.log.Warnfln("Failed to make puppet of %s join %s: %v", participant, portal.MXID, err)
			}
		}
	}
}

func (portal *Portal) CreateMatrixRoom(user *User, userTeam *database.UserTeam, channel *slack.Channel) error {
	portal.roomCreateLock.Lock()
	defer portal.roomCreateLock.Unlock()

	// If we have a matrix id the room should exist so we have nothing to do.
	if portal.MXID != "" {
		return nil
	}

	portal.log.Infoln("Creating Matrix room for channel:", portal.Portal.Key.ChannelID)

	channel = portal.UpdateInfo(user, userTeam, channel, false)
	if channel == nil {
		return fmt.Errorf("didn't find channel metadata")
	}

	intent := portal.MainIntent()
	if err := intent.EnsureRegistered(); err != nil {
		return err
	}

	// TODO: bridge state stuff

	plainName, err := portal.GetPlainName(channel, userTeam)
	if err == nil && plainName != "" {
		formattedName := portal.bridge.Config.Bridge.FormatChannelName(config.ChannelNameParams{
			Name: plainName,
			Type: portal.getChannelType(channel),
		})
		portal.PlainName = plainName
		portal.Name = formattedName
	}

	portal.Topic = portal.getTopic(channel, userTeam)

	// TODO: get avatars figured out
	// portal.Avatar = puppet.Avatar
	// portal.AvatarURL = puppet.AvatarURL

	initialState := []*event.Event{}

	creationContent := make(map[string]interface{})
	creationContent["m.federate"] = false

	var invite []id.UserID

	if portal.bridge.Config.Bridge.Encryption.Default {
		initialState = append(initialState, &event.Event{
			Type: event.StateEncryption,
			Content: event.Content{
				Parsed: event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1},
			},
		})
		portal.Encrypted = true

		if portal.IsPrivateChat() {
			invite = append(invite, portal.bridge.Bot.UserID)
			portal.log.Infoln("added the bot because this portal is encrypted")
		}
	}

	resp, err := intent.CreateRoom(&mautrix.ReqCreateRoom{
		Visibility:      "private",
		Name:            portal.Name,
		Topic:           portal.Topic,
		Invite:          invite,
		Preset:          "private_chat",
		IsDirect:        portal.IsPrivateChat(),
		InitialState:    initialState,
		CreationContent: creationContent,
	})
	if err != nil {
		portal.log.Warnln("Failed to create room:", err)
		return err
	}

	portal.NameSet = portal.Name != ""
	portal.TopicSet = true
	portal.MXID = resp.RoomID
	portal.bridge.portalsLock.Lock()
	portal.bridge.portalsByMXID[portal.MXID] = portal
	portal.bridge.portalsLock.Unlock()
	portal.Update()
	portal.log.Infoln("Matrix room created:", portal.MXID)

	if portal.Encrypted && portal.IsPrivateChat() {
		err = portal.bridge.Bot.EnsureJoined(portal.MXID, appservice.EnsureJoinedParams{BotOverride: portal.MainIntent().Client})
		if err != nil {
			portal.log.Errorfln("Failed to ensure bridge bot is joined to private chat portal: %v", err)
		}
	}

	portal.ensureUserInvited(user)
	user.syncChatDoublePuppetDetails(portal, true)

	channelType := portal.getChannelType(channel)
	var members []string
	// no members are included in channels, only in group DMs
	switch channelType {
	case database.ChannelTypeChannel:
		members = portal.getChannelMembers(userTeam, 3) // TODO: this just fetches 3 members so channels don't have to look like DMs
	case database.ChannelTypeDM:
		members = []string{channel.User, portal.Key.UserID}
	case database.ChannelTypeGroupDM:
		members = channel.Members
	}
	portal.syncParticipants(user, userTeam, members)

	// if portal.IsPrivateChat() {
	// 	puppet := user.bridge.GetPuppetByID(userTeam.Key.TeamID, portal.Key.UserID)

	// 	chats := map[id.UserID][]id.RoomID{puppet.MXID: {portal.MXID}}
	// 	user.updateDirectChats(chats)
	// }

	firstEventResp, err := portal.MainIntent().SendMessageEvent(portal.MXID, portalCreationDummyEvent, struct{}{})
	if err != nil {
		portal.log.Errorln("Failed to send dummy event to mark portal creation:", err)
	} else {
		portal.FirstEventID = firstEventResp.EventID
		portal.UpdateBridgeInfo()
		portal.Update()
	}

	portal.FillMessages(user, userTeam, 1, "")

	return nil
}

func (portal *Portal) getChannelMembers(userTeam *database.UserTeam, limit int) []string {
	members, _, err := userTeam.Client.GetUsersInConversation(&slack.GetUsersInConversationParameters{
		ChannelID: portal.Key.ChannelID,
		Limit:     limit,
	})
	if err != nil {
		portal.log.Errorfln("Error fetching channel members for %v: %v", portal.Key, err)
		return nil
	}
	return members

}

func (portal *Portal) FillMessages(user *User, userteam *database.UserTeam, limit int, latest string) error {
	messagesHandled := 0
	earliestHandled := latest

	// keep fetching `limit` messages from Slack and handling the message type events until `limit` messages have been handled
	for {
		messages, err := userteam.Client.GetConversationHistory(&slack.GetConversationHistoryParameters{
			ChannelID: portal.Key.ChannelID,
			Limit:     limit,
			Latest:    earliestHandled,
			Inclusive: false,
		})
		if err != nil {
			portal.log.Warnfln("Unable to fill %d message: %v", limit, err)
			return err
		}
		if len(messages.Messages) == 0 {
			portal.log.Warnfln("Not enough messages in Slack to fill %d messages", limit)
			return nil
		}
		portal.log.Debugfln("Received %d messages from Slack when filling", len(messages.Messages))
		for _, message := range messages.Messages {
			if message.Type == "message" {
				portal.log.Debugfln("Filling in message %s", message.Timestamp)
				messageEvent := slack.MessageEvent(message)
				messageFilled := portal.HandleSlackMessage(user, userteam, &messageEvent)
				if portal.parseTimestamp(message.Timestamp).Before(portal.parseTimestamp(earliestHandled)) {
					earliestHandled = message.Timestamp
				}
				if messageFilled {
					messagesHandled += 1
				}
				if messagesHandled >= limit {
					return nil
				}
			}
		}
	}
}

func (portal *Portal) ensureUserInvited(user *User) bool {
	return user.ensureInvited(portal.MainIntent(), portal.MXID, portal.IsPrivateChat())
}

func (portal *Portal) markMessageHandled(slackID string, slackThreadID string, mxid id.EventID, authorID string) *database.Message {
	msg := portal.bridge.DB.Message.New()
	msg.Channel = portal.Key
	msg.SlackID = slackID
	msg.MatrixID = mxid
	msg.AuthorID = authorID
	msg.SlackThreadID = slackThreadID
	msg.Insert()

	return msg
}

func (portal *Portal) sendMediaFailedMessage(intent *appservice.IntentAPI, bridgeErr error) {
	content := &event.MessageEventContent{
		Body:    fmt.Sprintf("Failed to bridge media: %v", bridgeErr),
		MsgType: event.MsgNotice,
	}

	_, err := portal.sendMatrixMessage(intent, event.EventMessage, content, nil, time.Now().UTC().UnixMilli())
	if err != nil {
		portal.log.Warnfln("failed to send error message to matrix: %v", err)
	}
}

func (portal *Portal) encrypt(intent *appservice.IntentAPI, content *event.Content, eventType event.Type) (event.Type, error) {
	if !portal.Encrypted || portal.bridge.Crypto == nil {
		return eventType, nil
	}
	intent.AddDoublePuppetValue(content)
	// TODO maybe the locking should be inside mautrix-go?
	portal.encryptLock.Lock()
	err := portal.bridge.Crypto.Encrypt(portal.MXID, eventType, content)
	portal.encryptLock.Unlock()
	if err != nil {
		return eventType, fmt.Errorf("failed to encrypt event: %w", err)
	}
	return event.EventEncrypted, nil
}

const doublePuppetKey = "fi.mau.double_puppet_source"
const doublePuppetValue = "mautrix-slack"

func (portal *Portal) sendMatrixMessage(intent *appservice.IntentAPI, eventType event.Type, content *event.MessageEventContent, extraContent map[string]interface{}, timestamp int64) (*mautrix.RespSendEvent, error) {
	wrappedContent := event.Content{Parsed: content, Raw: extraContent}
	if timestamp != 0 && intent.IsCustomPuppet {
		if wrappedContent.Raw == nil {
			wrappedContent.Raw = map[string]interface{}{}
		}
		if intent.IsCustomPuppet {
			wrappedContent.Raw[doublePuppetKey] = doublePuppetValue
		}
	}
	var err error
	eventType, err = portal.encrypt(intent, &wrappedContent, eventType)
	if err != nil {
		return nil, err
	}

	if eventType == event.EventEncrypted {
		// Clear other custom keys if the event was encrypted, but keep the double puppet identifier
		if intent.IsCustomPuppet {
			wrappedContent.Raw = map[string]interface{}{doublePuppetKey: doublePuppetValue}
		} else {
			wrappedContent.Raw = nil
		}
	}

	_, _ = intent.UserTyping(portal.MXID, false, 0)
	if timestamp == 0 {
		return intent.SendMessageEvent(portal.MXID, eventType, &wrappedContent)
	} else {
		return intent.SendMassagedMessageEvent(portal.MXID, eventType, &wrappedContent, timestamp)
	}
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

	switch msg.evt.Type {
	case event.EventMessage:
		portal.handleMatrixMessage(msg.user, msg.evt, &ms)
	case event.EventRedaction:
		portal.handleMatrixRedaction(msg.user, msg.evt)
	case event.EventReaction:
		portal.handleMatrixReaction(msg.user, msg.evt, &ms)
	default:
		portal.log.Debugln("unknown event type", msg.evt.Type)
	}
}

func (portal *Portal) handleMatrixMessage(sender *User, evt *event.Event, ms *metricSender) {
	portal.slackMessageLock.Lock()
	defer portal.slackMessageLock.Unlock()

	start := time.Now()

	userTeam := sender.GetUserTeam(portal.Key.TeamID)
	if userTeam == nil {
		go ms.sendMessageMetrics(evt, errUserNotLoggedIn, "Ignoring", true)
		return
	}
	if userTeam.Client == nil {
		portal.log.Errorfln("Client for userteam %s is nil!", userTeam.Key)
		return
	}

	existing := portal.bridge.DB.Message.GetByMatrixID(portal.Key, evt.ID)
	if existing != nil {
		portal.log.Debugln("not handling duplicate message", evt.ID)
		go ms.sendMessageMetrics(evt, nil, "", true)
		return
	}

	messageAge := ms.timings.totalReceive
	errorAfter := portal.bridge.Config.Bridge.MessageHandlingTimeout.ErrorAfter
	deadline := portal.bridge.Config.Bridge.MessageHandlingTimeout.Deadline
	isScheduled, _ := evt.Content.Raw["com.beeper.scheduled"].(bool)
	if isScheduled {
		portal.log.Debugfln("%s is a scheduled message, extending handling timeouts", evt.ID)
		errorAfter *= 10
		deadline *= 10
	}

	if errorAfter > 0 {
		remainingTime := errorAfter - messageAge
		if remainingTime < 0 {
			go ms.sendMessageMetrics(evt, errTimeoutBeforeHandling, "Timeout handling", true)
			return
		} else if remainingTime < 1*time.Second {
			portal.log.Warnfln("Message %s was delayed before reaching the bridge, only have %s (of %s timeout) until delay warning", evt.ID, remainingTime, errorAfter)
		}
		go func() {
			time.Sleep(remainingTime)
			ms.sendMessageMetrics(evt, errMessageTakingLong, "Timeout handling", false)
		}()
	}

	ctx := context.Background()
	if deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, deadline)
		defer cancel()
	}
	ms.timings.preproc = time.Since(start)

	start = time.Now()
	options, fileUpload, threadTs, err := portal.convertMatrixMessage(ctx, sender, userTeam, evt)
	ms.timings.convert = time.Since(start)

	start = time.Now()
	var timestamp string
	if options == nil && fileUpload == nil {
		go ms.sendMessageMetrics(evt, err, "Error converting", true)
		return
	} else if options != nil {
		portal.log.Debugfln("Sending message %s to Slack %s %s", evt.ID, portal.Key.TeamID, portal.Key.ChannelID)
		_, timestamp, err = userTeam.Client.PostMessage(
			portal.Key.ChannelID,
			slack.MsgOptionAsUser(true),
			slack.MsgOptionCompose(options...))
		if err != nil {
			go ms.sendMessageMetrics(evt, err, "Error sending", true)
			return
		}
	} else if fileUpload != nil {
		portal.log.Debugfln("Uploading file from message %s to Slack %s %s", evt.ID, portal.Key.TeamID, portal.Key.ChannelID)
		file, err := userTeam.Client.UploadFile(*fileUpload)
		if err != nil {
			portal.log.Errorfln("Failed to upload slack attachment: %v", err)
			go ms.sendMessageMetrics(evt, errMediaSlackUploadFailed, "Error uploading", true)
			return
		}
		var shareInfo slack.ShareFileInfo
		// Slack puts the channel message info after uploading a file in either file.shares.private or file.shares.public
		if info, found := file.Shares.Private[portal.Key.ChannelID]; found && len(info) > 0 {
			shareInfo = info[0]
		} else if info, found := file.Shares.Public[portal.Key.ChannelID]; found && len(info) > 0 {
			shareInfo = info[0]
		} else {
			go ms.sendMessageMetrics(evt, errMediaSlackUploadFailed, "Error uploading", true)
			return
		}
		timestamp = shareInfo.Ts
	}
	ms.timings.totalSend = time.Since(start)
	go ms.sendMessageMetrics(evt, err, "Error sending", true)
	// TODO: store these timings in some way

	if timestamp != "" {
		dbMsg := portal.bridge.DB.Message.New()
		dbMsg.Channel = portal.Key
		dbMsg.SlackID = timestamp
		dbMsg.MatrixID = evt.ID
		dbMsg.AuthorID = userTeam.Key.SlackID
		dbMsg.SlackThreadID = threadTs
		dbMsg.Insert()
	}
}

func (portal *Portal) convertMatrixMessage(ctx context.Context, sender *User, userTeam *database.UserTeam, evt *event.Event) (options []slack.MsgOption, fileUpload *slack.FileUploadParameters, threadTs string, err error) {
	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		return nil, nil, "", errUnexpectedParsedContentType
	}

	contentBody := content.Body
	var existingTs string
	if content.RelatesTo != nil && content.RelatesTo.Type == event.RelReplace { // fetch the slack original TS for editing purposes
		existing := portal.bridge.DB.Message.GetByMatrixID(portal.Key, content.RelatesTo.EventID)
		if existing != nil && existing.SlackID != "" {
			existingTs = existing.SlackID
			contentBody = content.NewContent.Body
		} else {
			portal.log.Errorfln("Matrix message %s is an edit, but can't find the original Slack message ID", evt.ID)
			return nil, nil, "", errTargetNotFound
		}
	} else if content.RelatesTo != nil && content.RelatesTo.Type == event.RelThread { // fetch the thread root ID via Matrix thread
		rootMessage := portal.bridge.DB.Message.GetByMatrixID(portal.Key, content.RelatesTo.GetThreadParent())
		if rootMessage != nil {
			threadTs = rootMessage.SlackID
		}
	} else if threadTs == "" && content.RelatesTo != nil && content.RelatesTo.InReplyTo != nil { // if the first method failed, try via Matrix reply
		parentMessage := portal.bridge.DB.Message.GetByMatrixID(portal.Key, content.GetReplyTo())
		if parentMessage != nil {
			if parentMessage.SlackThreadID != "" {
				threadTs = parentMessage.SlackThreadID
			} else {
				threadTs = parentMessage.SlackID
			}
		}
	}

	switch content.MsgType {
	case event.MsgText, event.MsgEmote, event.MsgNotice:
		options = []slack.MsgOption{slack.MsgOptionText(contentBody, false)}
		if threadTs != "" {
			options = append(options, slack.MsgOptionTS(threadTs))
		}
		if existingTs != "" {
			options = append(options, slack.MsgOptionUpdate(existingTs))
		}
		if content.MsgType == event.MsgEmote {
			options = append(options, slack.MsgOptionMeMessage())
		}
		return options, nil, threadTs, nil
	case event.MsgAudio, event.MsgFile, event.MsgImage, event.MsgVideo:
		data, err := portal.downloadMatrixAttachment(content)
		if err != nil {
			portal.log.Errorfln("Failed to download matrix attachment: %v", err)
			return nil, nil, "", errMediaDownloadFailed
		}
		fileUpload = &slack.FileUploadParameters{
			Filename:        content.Body,
			Filetype:        content.Info.MimeType,
			Reader:          bytes.NewReader(data),
			Channels:        []string{portal.Key.ChannelID},
			ThreadTimestamp: threadTs,
		}
		return nil, fileUpload, threadTs, nil
	default:
		return nil, nil, "", errUnknownMsgType
	}
}

func (portal *Portal) handleMatrixReaction(sender *User, evt *event.Event, ms *metricSender) {
	portal.slackMessageLock.Lock()
	defer portal.slackMessageLock.Unlock()

	userTeam := sender.GetUserTeam(portal.Key.TeamID)
	if userTeam == nil {
		go ms.sendMessageMetrics(evt, errUserNotLoggedIn, "Ignoring", true)
		return
	}

	reaction := evt.Content.AsReaction()
	if reaction.RelatesTo.Type != event.RelAnnotation {
		portal.log.Errorfln("Ignoring reaction %s due to unknown m.relates_to data", evt.ID)
		ms.sendMessageMetrics(evt, errUnexpectedRelatesTo, "Error sending", true)
		return
	}

	var slackID string

	msg := portal.bridge.DB.Message.GetByMatrixID(portal.Key, reaction.RelatesTo.EventID)

	// Due to the differences in attachments between Slack and Matrix, if a
	// user reacts to a media message on discord our lookup above will fail
	// because the relation of matrix media messages to attachments in handled
	// in the attachments table instead of messages so we need to check that
	// before continuing.
	//
	// This also leads to interesting problems when a Slack message comes in
	// with multiple attachments. A user can react to each one individually on
	// Matrix, which will cause us to send it twice. Slack tends to ignore
	// this, but if the user removes one of them, discord removes it and now
	// they're out of sync. Perhaps we should add a counter to the reactions
	// table to keep them in sync and to avoid sending duplicates to Slack.
	if msg == nil {
		attachment := portal.bridge.DB.Attachment.GetByMatrixID(portal.Key, reaction.RelatesTo.EventID)
		if attachment != nil {
			slackID = attachment.SlackMessageID
		}
	} else {
		slackID = msg.SlackID
	}
	if msg.SlackID == "" {
		portal.log.Debugf("Message %s has not yet been sent to slack", reaction.RelatesTo.EventID)
		ms.sendMessageMetrics(evt, errReactionTargetNotFound, "Error sending", true)
		return
	}

	emojiID := emojiToShortcode(reaction.RelatesTo.Key)
	if emojiID == "" {
		portal.log.Errorfln("Couldn't find shortcode for emoji %s", reaction.RelatesTo.Key)
		ms.sendMessageMetrics(evt, errEmojiShortcodeNotFound, "Error sending", true)
		return
	}

	// TODO: Figure out if this is a custom emoji or not.
	// emojiID := reaction.RelatesTo.Key
	// if strings.HasPrefix(emojiID, "mxc://") {
	// 	uri, _ := id.ParseContentURI(emojiID)
	// 	emoji := portal.bridge.DB.Emoji.GetByMatrixURL(uri)
	// 	if emoji == nil {
	// 		portal.log.Errorfln("failed to find emoji for %s", emojiID)

	// 		return
	// 	}

	// 	emojiID = emoji.APIName()
	// }

	err := userTeam.Client.AddReaction(emojiID, slack.ItemRef{
		Channel:   portal.Key.ChannelID,
		Timestamp: slackID,
	})
	ms.sendMessageMetrics(evt, err, "Error sending", true)
	if err != nil {
		portal.log.Debugfln("Failed to send reaction %s id:%s: %v", portal.Key, slackID, err)
		return
	}

	dbReaction := portal.bridge.DB.Reaction.New()
	dbReaction.Channel.TeamID = portal.Key.TeamID
	dbReaction.Channel.UserID = portal.Key.UserID
	dbReaction.Channel.ChannelID = portal.Key.ChannelID
	dbReaction.MatrixEventID = evt.ID
	dbReaction.SlackMessageID = slackID
	dbReaction.AuthorID = userTeam.Key.SlackID
	dbReaction.MatrixName = reaction.RelatesTo.Key
	dbReaction.SlackName = emojiID
	dbReaction.Insert()
	portal.log.Debugfln("Inserted reaction %v %s %s %s %s into database", dbReaction.Channel, dbReaction.MatrixEventID, dbReaction.SlackMessageID, dbReaction.AuthorID, dbReaction.SlackName)
}

func (portal *Portal) handleMatrixRedaction(user *User, evt *event.Event) {
	portal.slackMessageLock.Lock()
	defer portal.slackMessageLock.Unlock()

	userTeam := user.GetUserTeam(portal.Key.TeamID)
	if userTeam == nil {
		go portal.sendMessageMetrics(evt, errUserNotLoggedIn, "Ignoring", nil)
		return
	}
	portal.log.Debugfln("Received redaction %s from %s", evt.ID, evt.Sender)

	// First look if we're redacting a message
	message := portal.bridge.DB.Message.GetByMatrixID(portal.Key, evt.Redacts)
	if message != nil {
		if message.SlackID != "" {
			_, _, err := userTeam.Client.DeleteMessage(portal.Key.ChannelID, message.SlackID)
			if err != nil {
				portal.log.Debugfln("Failed to delete slack message %s: %v", message.SlackID, err)
			} else {
				message.Delete()
			}
			go portal.sendMessageMetrics(evt, err, "Error sending", nil)
		} else {
			go portal.sendMessageMetrics(evt, errTargetNotFound, "Error sending", nil)
		}
		return
	}

	// Now check if it's a reaction.
	reaction := portal.bridge.DB.Reaction.GetByMatrixID(portal.Key, evt.Redacts)
	if reaction != nil {
		if reaction.SlackName != "" {
			err := userTeam.Client.RemoveReaction(reaction.SlackName, slack.ItemRef{
				Channel:   portal.Key.ChannelID,
				Timestamp: reaction.SlackMessageID,
			})
			if err != nil && err.Error() != "no_reaction" {
				portal.log.Debugfln("Failed to delete reaction %s for message %s: %v", reaction.SlackName, reaction.SlackMessageID, err)
			} else if err != nil && err.Error() == "no_reaction" {
				portal.log.Warnfln("Didn't delete Slack reaction %s for message %s: reaction doesn't exist on Slack", reaction.SlackName, reaction.SlackMessageID)
				reaction.Delete()
				err = nil // not reporting an error for this
			} else {
				reaction.Delete()
			}
			go portal.sendMessageMetrics(evt, err, "Error sending", nil)
		} else {
			go portal.sendMessageMetrics(evt, errUnknownEmoji, "Error sending", nil)
		}
		return
	}

	portal.log.Warnfln("Failed to redact %s@%s: no event found", portal.Key, evt.Redacts)
	go portal.sendMessageMetrics(evt, errReactionTargetNotFound, "Error sending", nil)
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
		user := portal.bridge.GetUserByMXID(userID)
		if user != nil {
			userTeam := user.GetUserTeam(portal.Key.TeamID)
			if userTeam != nil && userTeam.IsLoggedIn() {
				portal.sendSlackTyping(userTeam)
			}
		}
	}
}

func (portal *Portal) sendSlackTyping(userTeam *database.UserTeam) {
	if userTeam.RTM != nil {
		typing := userTeam.RTM.NewTypingMessage(portal.Key.ChannelID)
		userTeam.RTM.SendMessage(typing)
	} else {
		portal.log.Debugfln("RTM for userteam %s not connected!", userTeam.Key)
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
		user := portal.bridge.GetUserByMXID(userID)
		if user != nil {
			userTeam := user.GetUserTeam(portal.Key.TeamID)
			if userTeam != nil && userTeam.IsConnected() {
				portal.sendSlackTyping(userTeam)
			}
		}
	}
}

func (portal *Portal) HandleMatrixLeave(brSender bridge.User) {
	portal.log.Debugln("User left private chat portal, cleaning up and deleting...")
	portal.delete()
	portal.cleanup(false)

	// TODO: figure out how to close a dm from the API.

	portal.cleanupIfEmpty()
}

func (portal *Portal) leave(sender *User) {
	if portal.MXID == "" {
		return
	}

	panic("not implemented")

	// intent := portal.bridge.GetPuppetByID(sender.ID).IntentFor(portal)
	// intent.LeaveRoom(portal.MXID)
}

func (portal *Portal) delete() {
	portal.Portal.Delete()
	portal.bridge.portalsLock.Lock()
	delete(portal.bridge.portalsByID, portal.Key)

	if portal.MXID != "" {
		delete(portal.bridge.portalsByMXID, portal.MXID)
	}

	portal.bridge.portalsLock.Unlock()
}

func (portal *Portal) cleanupIfEmpty() {
	users, err := portal.getMatrixUsers()
	if err != nil {
		portal.log.Errorfln("Failed to get Matrix user list to determine if portal needs to be cleaned up: %v", err)

		return
	}

	if len(users) == 0 {
		portal.log.Infoln("Room seems to be empty, cleaning up...")
		portal.delete()
		portal.cleanup(false)
	}
}

func (portal *Portal) cleanup(puppetsOnly bool) {
	if portal.MXID != "" {
		return
	}

	if portal.IsPrivateChat() {
		_, err := portal.MainIntent().LeaveRoom(portal.MXID)
		if err != nil {
			portal.log.Warnln("Failed to leave private chat portal with main intent:", err)
		}

		return
	}

	intent := portal.MainIntent()
	members, err := intent.JoinedMembers(portal.MXID)
	if err != nil {
		portal.log.Errorln("Failed to get portal members for cleanup:", err)

		return
	}

	for member := range members.Joined {
		if member == intent.UserID {
			continue
		}

		puppet := portal.bridge.GetPuppetByMXID(member)
		if puppet != nil {
			_, err = puppet.DefaultIntent().LeaveRoom(portal.MXID)
			if err != nil {
				portal.log.Errorln("Error leaving as puppet while cleaning up portal:", err)
			}
		} else if !puppetsOnly {
			_, err = intent.KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: member, Reason: "Deleting portal"})
			if err != nil {
				portal.log.Errorln("Error kicking user while cleaning up portal:", err)
			}
		}
	}

	_, err = intent.LeaveRoom(portal.MXID)
	if err != nil {
		portal.log.Errorln("Error leaving with main intent while cleaning up portal:", err)
	}
}

func (portal *Portal) getMatrixUsers() ([]id.UserID, error) {
	members, err := portal.MainIntent().JoinedMembers(portal.MXID)
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

func (portal *Portal) parseTimestamp(timestamp string) time.Time {
	parts := strings.Split(timestamp, ".")

	seconds, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		portal.log.Warnfln("failed to parse timestamp %s, using Now()", timestamp)

		return time.Now().UTC()
	}

	var nanoSeconds int64
	if len(parts) > 1 {
		nsec, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			portal.log.Warnfln("failed to parse nanoseconds %s, using 0", parts[1])
			nanoSeconds = 0
		} else {
			nanoSeconds = nsec
		}
	}

	return time.Unix(seconds, nanoSeconds)
}

func (portal *Portal) getBridgeInfoStateKey() string {
	return fmt.Sprintf("fi.mau.slack://slackgo/%s/%s", portal.Key.TeamID, portal.Key.ChannelID)
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
			Channel: event.BridgeInfoSection{
				ID:          portal.Key.ChannelID,
				DisplayName: portal.Name,
			},
		},
	}

	if portal.Type == database.ChannelTypeDM || portal.Type == database.ChannelTypeGroupDM {
		bridgeInfo.RoomType = "dm"
	}

	teamInfo := portal.bridge.DB.TeamInfo.GetBySlackTeam(portal.Key.TeamID)
	if teamInfo != nil {
		bridgeInfo.Network = &event.BridgeInfoSection{
			ID:          portal.Key.TeamID,
			DisplayName: teamInfo.TeamName,
			ExternalURL: teamInfo.TeamUrl,
			AvatarURL:   teamInfo.AvatarUrl.CUString(),
		}
	}
	var bridgeInfoStateKey = portal.getBridgeInfoStateKey()

	return bridgeInfoStateKey, bridgeInfo
}

func (portal *Portal) UpdateBridgeInfo() {
	if len(portal.MXID) == 0 {
		portal.log.Debugln("Not updating bridge info: no Matrix room created")
		return
	}
	portal.log.Debugln("Updating bridge info...")
	stateKey, content := portal.getBridgeInfo()
	_, err := portal.MainIntent().SendStateEvent(portal.MXID, event.StateBridge, stateKey, content)
	if err != nil {
		portal.log.Warnln("Failed to update m.bridge:", err)
	}
	// TODO remove this once https://github.com/matrix-org/matrix-doc/pull/2346 is in spec
	_, err = portal.MainIntent().SendStateEvent(portal.MXID, event.StateHalfShotBridge, stateKey, content)
	if err != nil {
		portal.log.Warnln("Failed to update uk.half-shot.bridge:", err)
	}
}

func (portal *Portal) getChannelType(channel *slack.Channel) database.ChannelType {
	if channel == nil {
		portal.log.Errorln("can't get type of nil channel")
		return database.ChannelTypeUnknown
	}

	// Slack Conversations structures are weird
	if channel.GroupConversation.Conversation.IsMpIM {
		return database.ChannelTypeGroupDM
	} else if channel.GroupConversation.Conversation.IsIM {
		return database.ChannelTypeDM
	} else if channel.IsChannel {
		return database.ChannelTypeChannel
	}

	return database.ChannelTypeUnknown
}

func (portal *Portal) GetPlainName(meta *slack.Channel, sourceTeam *database.UserTeam) (string, error) {
	channelType := portal.getChannelType(meta)
	var plainName string

	if channelType == database.ChannelTypeDM || channelType == database.ChannelTypeGroupDM {
		return "", nil
	} else {
		plainName = meta.Name
	}

	return plainName, nil
}

func (portal *Portal) UpdateNameDirect(name string) bool {
	if portal.Name == name && (portal.NameSet || portal.MXID == "") {
		return false
	} else if !portal.Encrypted && !portal.bridge.Config.Bridge.PrivateChatPortalMeta && portal.IsPrivateChat() {
		return false
	}
	portal.log.Debugfln("Updating name %q -> %q", portal.Name, name)
	portal.Name = name
	portal.NameSet = false
	if portal.MXID != "" && portal.Name != "" {
		_, err := portal.MainIntent().SetRoomName(portal.MXID, portal.Name)
		if err != nil {
			portal.log.Warnln("Failed to update room name:", err)
		} else {
			portal.NameSet = true
		}
	}
	return true
}

func (portal *Portal) UpdateName(meta *slack.Channel, sourceTeam *database.UserTeam) bool {
	channelType := portal.getChannelType(meta)
	plainName, err := portal.GetPlainName(meta, sourceTeam)

	if err != nil {
		portal.log.Errorfln("Couldn't update portal name: %v", err)
		return false
	}

	plainNameChanged := portal.PlainName != plainName
	portal.PlainName = plainName

	formattedName := portal.bridge.Config.Bridge.FormatChannelName(config.ChannelNameParams{
		Name: plainName,
		Type: channelType,
	})

	return portal.UpdateNameDirect(formattedName) || plainNameChanged
}

func (portal *Portal) updateRoomAvatar() {
	if portal.MXID == "" {
		return
	}
	_, err := portal.MainIntent().SetRoomAvatar(portal.MXID, portal.AvatarURL)
	if err != nil {
		portal.log.Warnln("Failed to update room avatar:", err)
	} else {
		portal.AvatarSet = true
	}
}

func (portal *Portal) UpdateAvatarFromPuppet(puppet *Puppet) bool {
	if portal.Avatar == puppet.Avatar && portal.AvatarURL == puppet.AvatarURL && (portal.AvatarSet || portal.MXID == "") {
		return false
	}

	portal.log.Debugfln("Updating avatar from puppet %q -> %q", portal.Avatar, puppet.Avatar)
	portal.Avatar = puppet.Avatar
	portal.AvatarURL = puppet.AvatarURL
	portal.AvatarSet = false
	portal.updateRoomAvatar()

	return true
}

func (portal *Portal) UpdateTopicDirect(topic string) bool {
	if portal.Topic == topic && (portal.TopicSet || portal.MXID == "") {
		return false
	}

	portal.log.Debugfln("Updating topic for room %s", portal.Key.ChannelID)
	portal.Topic = topic
	portal.TopicSet = false
	if portal.MXID != "" {
		_, err := portal.MainIntent().SetRoomTopic(portal.MXID, portal.Topic)
		if err != nil {
			portal.log.Warnln("Failed to update room topic:", err)
		} else {
			portal.TopicSet = true
		}
	}

	return true
}

func (portal *Portal) getTopic(meta *slack.Channel, sourceTeam *database.UserTeam) string {
	switch portal.getChannelType(meta) {
	case database.ChannelTypeDM, database.ChannelTypeGroupDM:
		return ""
	case database.ChannelTypeChannel:
		plainTopic := meta.Topic.Value
		plainDescription := meta.Purpose.Value

		var topicParts []string

		if plainTopic != "" {
			topicParts = append(topicParts, fmt.Sprintf("Topic: %s", plainTopic))
		}
		if plainDescription != "" {
			topicParts = append(topicParts, fmt.Sprintf("Description: %s", plainDescription))
		}

		return strings.Join(topicParts, "\n")
	default:
		return ""
	}
}

func (portal *Portal) UpdateTopic(meta *slack.Channel, sourceTeam *database.UserTeam) bool {
	matrixTopic := portal.getTopic(meta, sourceTeam)

	changed := portal.Topic != matrixTopic
	return portal.UpdateTopicDirect(matrixTopic) || changed
}

func (portal *Portal) UpdateInfo(source *User, sourceTeam *database.UserTeam, meta *slack.Channel, force bool) *slack.Channel {
	portal.log.Debugfln("Updating info for portal %s", portal.Key)
	changed := false

	if meta == nil {
		portal.log.Debugfln("UpdateInfo called without metadata, fetching from server via %s", sourceTeam.Key.SlackID)
		var err error
		meta, err = sourceTeam.Client.GetConversationInfo(portal.Key.ChannelID, true)
		if err != nil {
			portal.log.Errorfln("Failed to fetch meta via %s: %v", sourceTeam.Key.SlackID, err)
			return nil
		}
	}

	metaType := portal.getChannelType(meta)
	if portal.Type != metaType {
		portal.log.Warnfln("Portal type changed from %s to %s", portal.Type, metaType)
		portal.Type = metaType
		changed = true
	}

	if portal.DMUserID == "" && portal.IsPrivateChat() {
		portal.DMUserID = meta.User
		portal.log.Infoln("Found other user ID:", portal.DMUserID)
		changed = true
	}

	if portal.Type == database.ChannelTypeChannel {
		changed = portal.UpdateName(meta, sourceTeam) || changed
	}

	changed = portal.UpdateTopic(meta, sourceTeam) || changed

	if changed || force {
		portal.UpdateBridgeInfo()
		portal.Update()
	}

	return meta
}

// Returns bool: whether or not this resulted in a Matrix message in the room
func (portal *Portal) HandleSlackMessage(user *User, userTeam *database.UserTeam, msg *slack.MessageEvent) bool {
	portal.slackMessageLock.Lock()
	defer portal.slackMessageLock.Unlock()

	if msg.Msg.Type != "message" {
		portal.log.Warnln("ignoring unknown message type:", msg.Msg.Type)
		return false
	}

	if portal.MXID == "" {
		channel, err := userTeam.Client.GetConversationInfo(msg.Channel, true)
		if err != nil {
			portal.log.Errorln("failed to lookup channel info:", err)
			return false
		}

		portal.log.Debugln("Creating Matrix room from incoming message")
		if err := portal.CreateMatrixRoom(user, userTeam, channel); err != nil {
			portal.log.Errorln("Failed to create portal room:", err)
			return false
		}
	}

	existing := portal.bridge.DB.Message.GetBySlackID(portal.Key, msg.Msg.Timestamp)
	if existing != nil && msg.Msg.SubType != "message_changed" { // Slack reuses the same message ID on message edits
		portal.log.Debugln("Dropping duplicate message:", msg.Msg.Timestamp)
		return false
	}

	if msg.Msg.User == "" {
		portal.log.Debugfln("Starting handling of %s (no sender), subtype %s", msg.Msg.Timestamp, msg.Msg.SubType)
	} else {
		portal.log.Debugfln("Starting handling of %s by %s, subtype %s", msg.Msg.Timestamp, msg.Msg.User, msg.Msg.SubType)
	}

	switch msg.Msg.SubType {
	case "", "me_message": // Regular messages and /me
		portal.HandleSlackFiles(user, userTeam, msg)
		return portal.HandleSlackTextMessage(user, userTeam, &msg.Msg, nil)
	case "message_changed":
		return portal.HandleSlackTextMessage(user, userTeam, msg.SubMessage, existing)
	case "channel_topic", "channel_purpose":
		portal.UpdateInfo(user, userTeam, nil, false)
		portal.log.Debugfln("Received %s update, updating portal topic", msg.Msg.SubType)
	case "message_deleted":
		// Slack doesn't tell us who deleted a message, so there is no intent here
		message := portal.bridge.DB.Message.GetBySlackID(portal.Key, msg.Msg.DeletedTimestamp)
		if message == nil {
			portal.log.Warnfln("Failed to redact %s: Matrix event not known", msg.Msg.DeletedTimestamp)
		} else {
			_, err := portal.MainIntent().RedactEvent(portal.MXID, message.MatrixID)
			if err != nil {
				portal.log.Errorfln("Failed to redact %s: %v", message.MatrixID, err)
			} else {
				message.Delete()
			}
		}

		attachments := portal.bridge.DB.Attachment.GetAllBySlackMessageID(portal.Key, msg.Msg.DeletedTimestamp)
		for _, attachment := range attachments {
			_, err := portal.MainIntent().RedactEvent(portal.MXID, attachment.MatrixEventID)
			if err != nil {
				portal.log.Errorfln("Failed to redact %s: %v", attachment.MatrixEventID, err)
			} else {
				attachment.Delete()
			}
		}
	case "message_replied", "group_join", "group_leave", "channel_join", "channel_leave", "thread_broadcast": // Not yet an exhaustive list.
		// These subtypes are simply ignored, because they're handled elsewhere/in other ways (Slack sends multiple info of these events)
		portal.log.Debugfln("Received message subtype %s, which is ignored", msg.Msg.SubType)
	default:
		portal.log.Warnfln("Received unknown message subtype %s", msg.Msg.SubType)
	}
	return false
}

func (portal *Portal) addThreadMetadata(content *event.MessageEventContent, threadTs string) (hasThread bool, hasReply bool) {
	// fetch thread metadata and add to message
	if threadTs != "" {
		if content.RelatesTo == nil {
			content.RelatesTo = &event.RelatesTo{}
		}
		latestThreadMessage := portal.bridge.DB.Message.GetLastInThread(portal.Key, threadTs)
		rootThreadMessage := portal.bridge.DB.Message.GetBySlackID(portal.Key, threadTs)

		var latestThreadMessageID id.EventID
		if latestThreadMessage != nil {
			latestThreadMessageID = latestThreadMessage.MatrixID
		} else {
			latestThreadMessageID = ""
		}

		if rootThreadMessage != nil {
			content.RelatesTo.SetThread(rootThreadMessage.MatrixID, latestThreadMessageID)
			return true, true
		} else if latestThreadMessage != nil {
			content.RelatesTo.SetReplyTo(latestThreadMessage.MatrixID)
			return false, true
		}
	}
	return false, false
}

func (portal *Portal) HandleSlackFiles(user *User, userTeam *database.UserTeam, msg *slack.MessageEvent) {
	puppet := portal.bridge.GetPuppetByID(portal.Key.TeamID, msg.Msg.User)
	puppet.UpdateInfo(userTeam, nil)
	intent := puppet.IntentFor(portal)

	ts := portal.parseTimestamp(msg.Msg.Timestamp)

	for _, file := range msg.Msg.Files {
		content := portal.renderSlackFile(file)
		portal.addThreadMetadata(&content, msg.Msg.ThreadTimestamp)
		var data bytes.Buffer
		err := userTeam.Client.GetFile(file.URLPrivateDownload, &data)
		if err != nil {
			portal.log.Errorfln("Error downloading Slack file %s: %v", file.ID, err)
			continue
		}
		err = portal.uploadMedia(intent, data.Bytes(), &content)
		if err != nil {
			if errors.Is(err, mautrix.MTooLarge) {
				portal.log.Errorfln("File %s too large for Matrix server: %v", file.ID, err)
				continue
			} else if httpErr, ok := err.(mautrix.HTTPError); ok && httpErr.IsStatus(413) {
				portal.log.Errorfln("Proxy rejected too large file %s: %v", file.ID, err)
				continue
			} else {
				portal.log.Errorfln("Error uploading file %s to Matrix: %v", file.ID, err)
				continue
			}
		}
		resp, err := portal.sendMatrixMessage(intent, event.EventMessage, &content, nil, ts.UnixMilli())
		if err != nil {
			portal.log.Warnfln("Failed to send media message %s to matrix: %v", msg.Msg.Timestamp, err)
			continue
		}
		attachment := portal.bridge.DB.Attachment.New()
		attachment.Channel = portal.Key
		attachment.SlackFileID = file.ID
		attachment.SlackMessageID = msg.Timestamp
		attachment.MatrixEventID = resp.EventID
		attachment.Insert()
	}
}

func (portal *Portal) HandleSlackTextMessage(user *User, userTeam *database.UserTeam, msg *slack.Msg, editExisting *database.Message) bool {
	puppet := portal.bridge.GetPuppetByID(portal.Key.TeamID, msg.User)
	puppet.UpdateInfo(userTeam, nil)
	intent := puppet.IntentFor(portal)

	ts := portal.parseTimestamp(msg.Timestamp)

	// Slack adds an empty text field even in messages that don't have text
	if msg.Text != "" {
		content := portal.renderSlackMarkdown(msg.Text)

		// set m.emote if it's a /me message
		if msg.SubType == "me_message" {
			content.MsgType = event.MsgEmote
		}

		if editExisting != nil {
			content.SetEdit(editExisting.MatrixID)
		} else {
			portal.addThreadMetadata(&content, msg.ThreadTimestamp)
		}

		resp, err := portal.sendMatrixMessage(intent, event.EventMessage, &content, nil, ts.UnixMilli())
		if err != nil {
			portal.log.Warnfln("Failed to send message %s to matrix: %v", msg.Timestamp, err)
			return false
		}

		portal.markMessageHandled(msg.Timestamp, msg.ThreadTimestamp, resp.EventID, msg.User)
		go portal.sendDeliveryReceipt(resp.EventID)
		return true
	}
	return false
}

func (portal *Portal) HandleSlackReaction(user *User, userTeam *database.UserTeam, msg *slack.ReactionAddedEvent) {
	portal.slackMessageLock.Lock()
	defer portal.slackMessageLock.Unlock()

	if msg.Type != "reaction_added" {
		portal.log.Warnln("ignoring unknown message type:", msg.Type)
		return
	}

	portal.log.Debugfln("Handling Slack reaction: %v %s %s", portal.Key, msg.Item.Timestamp, msg.Reaction)

	if portal.MXID == "" {
		portal.log.Warnfln("No Matrix portal created for room %s %s, not bridging reaction")
	}

	existing := portal.bridge.DB.Reaction.GetBySlackID(portal.Key, msg.User, msg.Item.Timestamp, msg.Reaction)
	if existing != nil {
		portal.log.Warnfln("Dropping duplicate reaction: %s %s %s %s %s", portal.Key.TeamID, portal.Key.ChannelID, portal.Key.UserID, msg.Item.Timestamp, msg.Reaction)
		return
	}

	puppet := portal.bridge.GetPuppetByID(portal.Key.TeamID, msg.User)
	if puppet == nil {
		portal.log.Errorfln("Not sending reaction: can't find puppet for Slack user %s", msg.User)
		return
	}
	puppet.UpdateInfo(userTeam, nil)
	intent := puppet.IntentFor(portal)

	targetMessage := portal.bridge.DB.Message.GetBySlackID(portal.Key, msg.Item.Timestamp)
	if targetMessage == nil {
		portal.log.Errorfln("Not sending reaction: can't find Matrix message for %s %s %s", portal.Key.TeamID, portal.Key.ChannelID, msg.Item.Timestamp)
		return
	}

	emoji := shortcodeToEmoji(msg.Reaction)

	var content event.ReactionEventContent
	content.RelatesTo = event.RelatesTo{
		Type:    event.RelAnnotation,
		EventID: targetMessage.MatrixID,
		Key:     emoji,
	}
	resp, err := intent.SendMassagedMessageEvent(portal.MXID, event.EventReaction, &content, portal.parseTimestamp(msg.EventTimestamp).UnixMilli())
	if err != nil {
		portal.log.Errorfln("Failed to bridge reaction: %v", err)
		return
	}

	dbReaction := portal.bridge.DB.Reaction.New()
	dbReaction.Channel = portal.Key
	dbReaction.SlackMessageID = msg.Item.Timestamp
	dbReaction.MatrixEventID = resp.EventID
	dbReaction.AuthorID = msg.User
	dbReaction.MatrixName = emoji
	dbReaction.SlackName = msg.Reaction
	dbReaction.Insert()
}

func (portal *Portal) HandleSlackReactionRemoved(user *User, userTeam *database.UserTeam, msg *slack.ReactionRemovedEvent) {
	portal.slackMessageLock.Lock()
	defer portal.slackMessageLock.Unlock()

	if msg.Type != "reaction_removed" {
		portal.log.Warnln("ignoring unknown message type:", msg.Type)
		return
	}

	dbReaction := portal.bridge.DB.Reaction.GetBySlackID(portal.Key, msg.User, msg.Item.Timestamp, msg.Reaction)
	if dbReaction == nil {
		portal.log.Errorfln("Failed to redact reaction %v %s %s %s: reaction not found in database", portal.Key, msg.User, msg.Item.Timestamp, msg.Reaction)
		return
	}

	puppet := portal.bridge.GetPuppetByID(portal.Key.TeamID, msg.User)
	if puppet == nil {
		portal.log.Errorfln("Not redacting reaction: can't find puppet for Slack user %s %s", portal.Key.TeamID, msg.User)
		return
	}
	puppet.UpdateInfo(userTeam, nil)
	intent := puppet.IntentFor(portal)

	_, err := intent.RedactEvent(portal.MXID, dbReaction.MatrixEventID)
	if err != nil {
		portal.log.Errorfln("Failed to redact reaction %v %s %s %s: %v", portal.Key, msg.User, msg.Item.Timestamp, msg.Reaction, err)
		return
	}

	dbReaction.Delete()
}

func (portal *Portal) HandleSlackTyping(user *User, userTeam *database.UserTeam, msg *slack.UserTypingEvent) {
	puppet := portal.bridge.GetPuppetByID(portal.Key.TeamID, msg.User)
	if puppet == nil {
		portal.log.Errorfln("Not sending typing status: can't find puppet for Slack user %s", msg.User)
		return
	}
	puppet.UpdateInfo(userTeam, nil)
	intent := puppet.IntentFor(portal)

	_, err := intent.UserTyping(portal.MXID, true, time.Duration(time.Second*3))
	if err != nil {
		portal.log.Warnfln("Error sending typing status to Matrix: %v", err)
	}
}

func (portal *Portal) HandleSlackChannelMarked(user *User, userTeam *database.UserTeam, msg *slack.ChannelMarkedEvent) {
	puppet := portal.bridge.GetPuppetByCustomMXID(user.MXID)
	if puppet == nil {
		portal.log.Errorfln("Not sending typing status: can't find puppet for Slack user %s", msg.User)
		return
	}
	puppet.UpdateInfo(userTeam, nil)
	intent := puppet.IntentFor(portal)

	message := portal.bridge.DB.Message.GetBySlackID(portal.Key, msg.Timestamp)

	if message == nil {
		portal.log.Debugfln("Couldn't mark portal %s as read: no Matrix room", portal.Key)
		return
	}

	err := intent.MarkRead(portal.MXID, message.MatrixID)
	if err != nil {
		portal.log.Warnfln("Error marking Matrix room %s as read by %s: %v", portal.MXID, intent.UserID, err)
	}
}
