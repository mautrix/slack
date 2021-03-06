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

	"github.com/mautrix/slack/config"
	"github.com/mautrix/slack/database"
)

type portalMatrixMessage struct {
	evt  *event.Event
	user *User
}

type Portal struct {
	*database.Portal

	bridge *SlackBridge
	log    log.Logger

	roomCreateLock sync.Mutex
	encryptLock    sync.Mutex

	matrixMessages chan portalMatrixMessage
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
		portal.matrixMessages <- portalMatrixMessage{user: user.(*User), evt: evt}
	}
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

func (br *SlackBridge) GetAllPortalsByID(teamID, userID string) []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.GetAllByID(teamID, userID))
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
	// if portal.IsPrivateChat() && portal.DMUser != "" {
	// 	return portal.bridge.GetPuppetByID(portal.DMUser).DefaultIntent()
	// }

	return portal.bridge.Bot
}

func (portal *Portal) CreateMatrixRoom(user *User, userTeam *database.UserTeam, channel *slack.Channel) error {
	portal.roomCreateLock.Lock()
	defer portal.roomCreateLock.Unlock()

	// If we have a matrix id the room should exist so we have nothing to do.
	if portal.MXID != "" {
		return nil
	}

	channel = portal.UpdateInfo(user, userTeam, channel)
	if channel == nil {
		return fmt.Errorf("didn't find channel metadata")
	}

	intent := portal.MainIntent()
	if err := intent.EnsureRegistered(); err != nil {
		return err
	}

	// TODO: bridge state stuff

	portal.Topic = channel.Topic.Value

	// TODO: get avatars figured out
	// portal.Avatar = puppet.Avatar
	// portal.AvatarURL = puppet.AvatarURL

	portal.log.Infoln("Creating Matrix room for channel:", portal.Portal.Key.ChannelID)

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

	portal.log.Infoln("name:", portal.Name, "; topic:", portal.Topic, "; invite:",
		invite, "; initialState:", initialState, "; creationContent:", creationContent)

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

	portal.MXID = resp.RoomID
	portal.Update()
	portal.bridge.portalsLock.Lock()
	portal.bridge.portalsByMXID[portal.MXID] = portal
	portal.bridge.portalsLock.Unlock()

	portal.ensureUserInvited(user)
	user.syncChatDoublePuppetDetails(portal, true)

	// TODO: portal.syncParticipants(user, channel.Recipients)

	// if portal.IsPrivateChat() {
	// 	puppet := user.bridge.GetPuppetByID(portal.Key.Receiver)

	// 	chats := map[id.UserID][]id.RoomID{puppet.MXID: {portal.MXID}}
	// 	user.updateDirectChats(chats)
	// }

	firstEventResp, err := portal.MainIntent().SendMessageEvent(portal.MXID, portalCreationDummyEvent, struct{}{})
	if err != nil {
		portal.log.Errorln("Failed to send dummy event to mark portal creation:", err)
	} else {
		portal.FirstEventID = firstEventResp.EventID
		portal.Update()
	}

	return nil
}

func (portal *Portal) ensureUserInvited(user *User) bool {
	// TODO
	// return user.ensureInvited(portal.MainIntent(), portal.MXID, portal.IsPrivateChat())
	return false
}

func (portal *Portal) markMessageHandled(msg *database.Message, slackID string, mxid id.EventID, authorID string, timestamp time.Time) *database.Message {
	if msg == nil {
		msg := portal.bridge.DB.Message.New()
		msg.Channel = portal.Key
		msg.SlackID = slackID
		msg.MatrixID = mxid
		msg.AuthorID = authorID
		msg.Timestamp = timestamp
		msg.Insert()
	} else {
		msg.UpdateMatrixID(mxid)
	}

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

func (portal *Portal) encrypt(content *event.Content, eventType event.Type) (event.Type, error) {
	if portal.Encrypted && portal.bridge.Crypto != nil {
		// TODO maybe the locking should be inside mautrix-go?
		portal.encryptLock.Lock()
		encrypted, err := portal.bridge.Crypto.Encrypt(portal.MXID, eventType, *content)
		portal.encryptLock.Unlock()
		if err != nil {
			return eventType, fmt.Errorf("failed to encrypt event: %w", err)
		}
		eventType = event.EventEncrypted
		content.Parsed = encrypted
	}
	return eventType, nil
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
	eventType, err = portal.encrypt(&wrappedContent, eventType)
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
	switch msg.evt.Type {
	case event.EventMessage:
		portal.handleMatrixMessage(msg.user, msg.evt)
	case event.EventRedaction:
		portal.handleMatrixRedaction(msg.user, msg.evt)
	case event.EventReaction:
		portal.handleMatrixReaction(msg.user, msg.evt)
	default:
		portal.log.Debugln("unknown event type", msg.evt.Type)
	}
}

func (portal *Portal) handleMatrixMessage(sender *User, evt *event.Event) {
	// if portal.IsPrivateChat() && sender.ID != portal.Key.Receiver {
	// 	return
	// }

	existing := portal.bridge.DB.Message.GetByMatrixID(portal.Key, evt.ID)
	if existing != nil {
		portal.log.Debugln("not handling duplicate message", evt.ID)

		return
	}

	_, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		portal.log.Debugfln("Failed to handle event %s: unexpected parsed content type %T", evt.ID, evt.Content.Parsed)

		return
	}

	// TODO
	// if content.RelatesTo != nil && content.RelatesTo.Type == event.RelReplace {
	// 	existing := portal.bridge.DB.Message.GetByMatrixID(portal.Key, content.RelatesTo.EventID)

	// 	if existing != nil && existing.SlackID != "" {
	// 		// we don't have anything to save for the update message right now
	// 		// as we're not tracking edited timestamps.
	// 		_, err := sender.Client.ChannelMessageEdit(portal.Key.ChannelID,
	// 			existing.SlackID, content.NewContent.Body)
	// 		if err != nil {
	// 			portal.log.Errorln("Failed to update message %s: %v", existing.SlackID, err)

	// 			return
	// 		}
	// 	}

	// 	return
	// }

	var err error

	// switch content.MsgType {
	// case event.MsgText, event.MsgEmote, event.MsgNotice:
	// 	sent := false

	// 	if content.RelatesTo != nil && content.RelatesTo.Type == event.RelReply {
	// 		existing := portal.bridge.DB.Message.GetByMatrixID(
	// 			portal.Key,
	// 			content.RelatesTo.EventID,
	// 		)

	// 		if existing != nil && existing.SlackID != "" {
	// 			msg, err = sender.Client.ChannelMessageSendReply(
	// 				portal.Key.ChannelID,
	// 				content.Body,
	// 				&discordgo.MessageReference{
	// 					ChannelID: portal.Key.ChannelID,
	// 					MessageID: existing.SlackID,
	// 				},
	// 			)
	// 			if err == nil {
	// 				sent = true
	// 			}
	// 		}
	// 	}
	// 	if !sent {
	// 		msg, err = sender.Client.ChannelMessageSend(portal.Key.ChannelID, content.Body)
	// 	}
	// case event.MsgAudio, event.MsgFile, event.MsgImage, event.MsgVideo:
	// 	data, err := portal.downloadMatrixAttachment(evt.ID, content)
	// 	if err != nil {
	// 		portal.log.Errorfln("Failed to download matrix attachment: %v", err)

	// 		return
	// 	}

	// 	msgSend := &discordgo.MessageSend{
	// 		Files: []*discordgo.File{
	// 			&discordgo.File{
	// 				Name:        content.Body,
	// 				ContentType: content.Info.MimeType,
	// 				Reader:      bytes.NewReader(data),
	// 			},
	// 		},
	// 	}

	// 	msg, err = sender.Client.ChannelMessageSendComplex(portal.Key.ChannelID, msgSend)
	// default:
	// 	portal.log.Warnln("unknown message type:", content.MsgType)
	// 	return
	// }

	if err != nil {
		portal.log.Errorfln("Failed to send message: %v", err)

		return
	}

	// if msg != nil {
	// 	dbMsg := portal.bridge.DB.Message.New()
	// 	dbMsg.Channel = portal.Key
	// 	dbMsg.SlackID = msg.ID
	// 	dbMsg.MatrixID = evt.ID
	// 	dbMsg.AuthorID = sender.ID
	// 	dbMsg.Timestamp = time.Now()
	// 	dbMsg.Insert()
	// }
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
		if portal != nil {
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

func (portal *Portal) handleMatrixReaction(user *User, evt *event.Event) {
	// if user.ID != portal.Key.Receiver {
	// 	return
	// }

	reaction := evt.Content.AsReaction()
	if reaction.RelatesTo.Type != event.RelAnnotation {
		portal.log.Errorfln("Ignoring reaction %s due to unknown m.relates_to data", evt.ID)

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
		slackID = attachment.SlackMessageID
	} else {
		if msg.SlackID == "" {
			portal.log.Debugf("Message %s has not yet been sent to slack", reaction.RelatesTo.EventID)

			return
		}

		slackID = msg.SlackID
	}

	// Figure out if this is a custom emoji or not.
	emojiID := ""
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

	// err := user.Session.MessageReactionAdd(portal.Key.ChannelID, discordID, emojiID)
	// if err != nil {
	// 	portal.log.Debugf("Failed to send reaction %s id:%s: %v", portal.Key, discordID, err)

	// 	return
	// }

	dbReaction := portal.bridge.DB.Reaction.New()
	dbReaction.Channel.TeamID = portal.Key.TeamID
	dbReaction.Channel.UserID = portal.Key.UserID
	dbReaction.Channel.ChannelID = portal.Key.ChannelID
	dbReaction.MatrixEventID = evt.ID
	dbReaction.SlackMessageID = slackID
	// dbReaction.AuthorID = user.ID
	dbReaction.MatrixName = reaction.RelatesTo.Key
	dbReaction.SlackID = emojiID
	dbReaction.Insert()
}

func (portal *Portal) handleMatrixRedaction(user *User, evt *event.Event) {
	// if user.ID != portal.Key.Receiver {
	// 	return
	// }

	// First look if we're redacting a message
	message := portal.bridge.DB.Message.GetByMatrixID(portal.Key, evt.Redacts)
	if message != nil {
		// if message.SlackID != "" {
		// 	err := user.Session.ChannelMessageDelete(portal.Key.ChannelID, message.SlackID)
		// 	if err != nil {
		// 		portal.log.Debugfln("Failed to delete discord message %s: %v", message.SlackID, err)
		// 	} else {
		// 		message.Delete()
		// 	}
		// }

		return
	}

	// Now check if it's a reaction.
	reaction := portal.bridge.DB.Reaction.GetByMatrixID(portal.Key, evt.Redacts)
	if reaction != nil {
		// if reaction.SlackID != "" {
		// 	err := user.Session.MessageReactionRemove(portal.Key.ChannelID, reaction.SlackMessageID, reaction.SlackID, reaction.AuthorID)
		// 	if err != nil {
		// 		portal.log.Debugfln("Failed to delete reaction %s for message %s: %v", reaction.SlackID, reaction.SlackMessageID, err)
		// 	} else {
		// 		reaction.Delete()
		// 	}
		// }

		return
	}

	portal.log.Warnfln("Failed to redact %s@%s: no event found", portal.Key, evt.Redacts)
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

func (portal *Portal) getBridgeInfo() (string, event.BridgeEventContent) {
	bridgeInfo := event.BridgeEventContent{
		BridgeBot: portal.bridge.Bot.UserID,
		Creator:   portal.MainIntent().UserID,
		Protocol: event.BridgeInfoSection{
			ID:          "slack",
			DisplayName: "Slack",
			AvatarURL:   portal.bridge.Config.AppService.Bot.ParsedAvatar.CUString(),
			ExternalURL: "https://slack.com/",
		},
		Channel: event.BridgeInfoSection{
			ID:          portal.Key.ChannelID,
			DisplayName: portal.Name,
		},
	}
	var bridgeInfoStateKey string
	// if portal.GuildID == "" {
	// 	bridgeInfoStateKey = fmt.Sprintf("fi.mau.discord://discord/dm/%s", portal.Key.ChannelID)
	// 	bridgeInfo.Channel.ExternalURL = fmt.Sprintf("https://discord.com/channels/@me/%s", portal.Key.ChannelID)
	// } else {
	// 	bridgeInfo.Network = &event.BridgeInfoSection{
	// 		ID: portal.GuildID,
	// 	}
	// 	if portal.Guild != nil {
	// 		bridgeInfo.Network.DisplayName = portal.Guild.Name
	// 		bridgeInfo.Network.AvatarURL = portal.Guild.AvatarURL.CUString()
	// 		// TODO is it possible to find the URL?
	// 	}
	// 	bridgeInfoStateKey = fmt.Sprintf("fi.mau.discord://discord/%s/%s", portal.GuildID, portal.Key.ChannelID)
	// 	bridgeInfo.Channel.ExternalURL = fmt.Sprintf("https://discord.com/channels/%s/%s", portal.GuildID, portal.Key.ChannelID)
	// }
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

	// Slack Conversations structures are weird... and we need to figure out
	// what group dm's look like.
	if channel.IsChannel {
		return database.ChannelTypeChannel
	} else if channel.GroupConversation.Conversation.IsIM {
		return database.ChannelTypeDM
	}

	return database.ChannelTypeUnknown
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
	if portal.MXID != "" {
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
	plainName := meta.Name

	if channelType == database.ChannelTypeDM {
		user, err := sourceTeam.Client.GetUserInfo(meta.User)
		if err != nil {
			portal.log.Warnfln("failed to lookup user %s: %w", user, err)
		} else {
			portal.log.Errorfln("user: %#v", user)
		}
	}

	plainNameChanged := portal.PlainName != plainName
	portal.PlainName = plainName

	return portal.UpdateNameDirect(portal.bridge.Config.Bridge.FormatChannelName(config.ChannelNameParams{
		Name: plainName,
		Type: channelType,
	})) || plainNameChanged
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

func (portal *Portal) UpdateTopic(topic string) bool {
	if portal.Topic == topic && (portal.TopicSet || portal.MXID == "") {
		return false
	}

	portal.log.Debugfln("Updating topic %q -> %q", portal.Topic, topic)
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

func (portal *Portal) UpdateInfo(source *User, sourceTeam *database.UserTeam, meta *slack.Channel) *slack.Channel {
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

	switch portal.Type {
	case database.ChannelTypeDM:
		if portal.DMUserID != "" {
			puppet := portal.bridge.GetPuppetByID(sourceTeam.Key.TeamID, portal.DMUserID)
			puppet.UpdateInfo(source, portal.Key.UserID, nil)
			changed = portal.UpdateAvatarFromPuppet(puppet) || changed
			changed = portal.UpdateNameDirect(puppet.Name) || changed
		}
	default:
		changed = portal.UpdateName(meta, sourceTeam) || changed
	}

	changed = portal.UpdateTopic(meta.Topic.Value) || changed

	if changed {
		portal.UpdateBridgeInfo()
		portal.Update()
	}

	return meta
}

func (portal *Portal) HandleSlackMessage(user *User, userTeam *database.UserTeam, msg *slack.MessageEvent) {
	if msg.Msg.Type != "message" {
		portal.log.Warnln("ignoring unknown message type:", msg.Msg.Type)
		return
	}

	if portal.MXID == "" {
		channel, err := userTeam.Client.GetConversationInfo(msg.Channel, true)
		if err != nil {
			portal.log.Errorln("failed to lookup channel info:", err)
			return
		}

		portal.log.Debugln("Creating Matrix room from incoming message")
		if err := portal.CreateMatrixRoom(user, userTeam, channel); err != nil {
			portal.log.Errorln("Failed to create portal room:", err)
			return
		}
	}

	existing := portal.bridge.DB.Message.GetBySlackID(portal.Key, msg.Msg.ClientMsgID)
	if existing != nil {
		portal.log.Debugln("Dropping duplicate message:", msg.Msg.ClientMsgID)
		return
	}

	portal.log.Debugfln("Starting handling of %s by %s", msg.Msg.ClientMsgID, msg.Msg.User)

	puppet := portal.bridge.GetPuppetByID(portal.Key.TeamID, msg.Msg.User)
	puppet.UpdateInfo(user, portal.Key.UserID, nil)
	intent := puppet.IntentFor(portal)

	content := portal.renderSlackMarkdown(msg.Msg.Text)
	ts := portal.parseTimestamp(msg.Msg.Timestamp)

	resp, err := portal.sendMatrixMessage(intent, event.EventMessage, &content, nil, ts.UnixMilli())
	if err != nil {
		portal.log.Warnfln("Failed to send message %s to matrix: %v", msg.Msg.ClientMsgID, err)
		return
	}

	go portal.sendDeliveryReceipt(resp.EventID)
}

func (portal *Portal) sendDeliveryReceipt(eventID id.EventID) {
	if portal.bridge.Config.Bridge.DeliveryReceipts {
		err := portal.bridge.Bot.MarkRead(portal.MXID, eventID)
		if err != nil {
			portal.log.Debugfln("Failed to send delivery receipt for %s: %v", eventID, err)
		}
	}
}
