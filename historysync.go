// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2021, 2022 Tulir Asokan, Max Sandholm
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
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/slack-go/slack"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"

	"go.mau.fi/mautrix-slack/database"
)

// region history sync handling

func (bridge *SlackBridge) handleHistorySyncsLoop() {
	if !bridge.Config.Bridge.Backfill.Enable {
		return
	}

	// Backfills shouldn't be marked as dispatched during startup, this gives them a chance to retry
	bridge.DB.Backfill.UndispatchAll()

	bridge.HandleBackfillRequestsLoop()

}

func (bridge *SlackBridge) backfillInChunks(backfillState *database.BackfillState, portal *Portal) {
	portal.backfillLock.Lock()
	defer portal.backfillLock.Unlock()

	backfillState.SetDispatched(true)
	defer backfillState.SetDispatched(false)

	maxMessages := bridge.Config.Bridge.Backfill.Incremental.MaxMessages.GetMaxMessagesFor(portal.Type)

	if maxMessages > 0 && backfillState.MessageCount >= maxMessages {
		backfillState.BackfillComplete = true
		backfillState.Upsert()
		bridge.Log.Infofln("Backfilling complete for portal %s, not filling any more", portal.Key)
		return
	}

	slackReqParams := slack.GetConversationHistoryParameters{
		ChannelID: portal.Key.ChannelID,
		Inclusive: false,
	}
	var forwardPrevID id.EventID
	var isLatestEvents bool
	portal.latestEventBackfillLock.Lock()
	// TODO: forward backfill isn't a thing yet
	// if req.BackfillType == database.BackfillForward {
	// 	lastMessage := portal.bridge.DB.Message.GetLast(portal.Key)
	// 	if lastMessage == nil {
	// 		bridge.Log.Warnfln("Empty portal %s, can't fetch Slack history for forward backfilling", portal.Key)
	// 		return
	// 	}
	// 	slackReqParams.Oldest = lastMessage.SlackID
	// 	forwardPrevID = lastMessage.MatrixID
	// 	// Sending events at the end of the room (= latest events)
	// 	isLatestEvents = true
	// } else {
	slackReqParams.Latest = portal.FirstSlackID
	if portal.FirstSlackID == "" {
		// Portal is empty -> events are latest
		isLatestEvents = true
	}
	//}
	if !isLatestEvents {
		// We'll use normal batch sending, so no need to keep blocking new message processing
		portal.latestEventBackfillLock.Unlock()
	} else {
		// This might involve sending events at the end of the room as non-historical events,
		// make sure we don't process messages until this is done.
		defer portal.latestEventBackfillLock.Unlock()
	}

	userTeam := bridge.DB.UserTeam.GetFirstUserTeamForPortal(&portal.Key)
	if userTeam == nil {
		bridge.Log.Errorfln("Couldn't find logged in user with access to %s for backfilling!", portal.Key)
		backfillState.BackfillComplete = true
		backfillState.Upsert()
		return
	}
	if userTeam.CookieToken != "" {
		userTeam.Client = slack.New(userTeam.Token, slack.OptionCookie("d", userTeam.CookieToken))
	} else {
		userTeam.Client = slack.New(userTeam.Token)
	}

	// Fetch actual messages from Slack.
	resp, err := userTeam.Client.GetConversationHistory(&slackReqParams)
	if err != nil {
		bridge.Log.Errorfln("Error fetching Slack messages for backfilling %s: %v", portal.Key, err)
		backfillState.BackfillComplete = true
		backfillState.Upsert()
		return
	}
	allMsgs := resp.Messages

	if len(allMsgs) == 0 {
		bridge.Log.Debugfln("Not backfilling %s: no bridgeable messages found", portal.Key)
		backfillState.BackfillComplete = true
		backfillState.Upsert()
		return
	}

	// Update the backfill status here after the room has been created.
	portal.updateBackfillStatus(backfillState)

	var maxBatchEvents int
	if !backfillState.ImmediateComplete {
		maxBatchEvents = -1
	} else {
		maxBatchEvents = bridge.Config.Bridge.Backfill.Incremental.MessagesPerBatch
	}

	bridge.Log.Infofln("Backfilling %d messages in %s, %d messages at a time", len(allMsgs), portal.Key, maxBatchEvents)
	toBackfill := allMsgs[0:]
	for len(toBackfill) > 0 {
		var msgs []slack.Message
		if len(toBackfill) <= maxBatchEvents || maxBatchEvents < 0 {
			msgs = toBackfill
			toBackfill = nil
		} else {
			msgs = toBackfill[:maxBatchEvents]
			toBackfill = toBackfill[maxBatchEvents:]
		}

		if len(msgs) > 0 {
			time.Sleep(time.Duration(bridge.Config.Bridge.Backfill.Incremental.PostBatchDelay) * time.Second)
			bridge.Log.Debugfln("Backfilling %d messages in %s", len(msgs), portal.Key)
			resp := portal.backfill(userTeam, msgs, !backfillState.ImmediateComplete, forwardPrevID)
			if resp != nil && (resp.BaseInsertionEventID != "" || !isLatestEvents) {
				backfillState.MessageCount += len(msgs)
			} else if resp == nil {
				// the backfill function has already logged an error; just store state in DB and stop filling
				if len(allMsgs) != 0 {
					portal.FirstSlackID = allMsgs[len(msgs)-1].Timestamp
					portal.Update(nil)
				} else {
					backfillState.BackfillComplete = true
				}
				backfillState.Upsert()
				return
			}
		}
	}
	bridge.Log.Debugfln("Finished backfilling %d messages in %s", len(allMsgs), portal.Key)

	backfillState.MessageCount += len(allMsgs)

	if !backfillState.ImmediateComplete {
		backfillState.ImmediateComplete = true
	}

	if !resp.HasMore {
		// Slack said there's no more history to backfill.
		backfillState.BackfillComplete = true
		portal.updateBackfillStatus(backfillState)
	}

	backfillState.Upsert()

	// TODO: add these config options
	// if bridge.Config.Bridge.HistorySync.UnreadHoursThreshold > 0 && conv.LastMessageTimestamp.Before(time.Now().Add(time.Duration(-bridge.Config.Bridge.HistorySync.UnreadHoursThreshold)*time.Hour)) {
	// 	user.markSelfReadFull(portal)
	// }
}

func (portal *Portal) deterministicEventID(sender string, messageID string, partName string) id.EventID {
	data := fmt.Sprintf("%s/slack/%s/%s", portal.MXID, sender, messageID)
	if partName != "" {
		data += "/" + partName
	}
	sum := sha256.Sum256([]byte(data))
	return id.EventID(fmt.Sprintf("$%s:slack.com", base64.RawURLEncoding.EncodeToString(sum[:])))
}

var (
	PortalCreationDummyEvent = event.Type{Type: "fi.mau.dummy.portal_created", Class: event.MessageEventType}
	PreBackfillDummyEvent    = event.Type{Type: "fi.mau.dummy.pre_backfill", Class: event.MessageEventType}

	HistorySyncMarker = event.Type{Type: "org.matrix.msc2716.marker", Class: event.MessageEventType}

	BackfillStatusEvent = event.Type{Type: "com.beeper.backfill_status", Class: event.StateEventType}
)

type SlackThreadInfo struct {
	ThreadOrigin id.EventID
	ThreadLatest id.EventID
}

func (portal *Portal) getLastEventID(msg *ConvertedSlackMessage) *id.EventID {
	var eventID id.EventID
	if msg.Event != nil {
		eventID = portal.deterministicEventID(msg.SlackAuthor, msg.SlackTimestamp, "text")
	} else if len(msg.FileAttachments) != 0 {
		eventID = portal.deterministicEventID(msg.SlackAuthor, msg.SlackTimestamp, fmt.Sprintf("file%d", len(msg.FileAttachments)-1))
	}
	return &eventID
}

func (portal *Portal) makeBackfillEvent(intent *appservice.IntentAPI, msg *event.MessageEventContent, partName string, info *ConvertedSlackMessage, threadInfos *map[string]SlackThreadInfo) *event.Event {
	content := event.Content{
		Parsed: msg,
	}
	if portal.bridge.Config.Homeserver.Software == bridgeconfig.SoftwareHungry {
		if info.SlackThreadTs != info.SlackTimestamp {
			threadInfo, found := (*threadInfos)[info.SlackThreadTs]
			if found {
				content.Parsed.(*event.MessageEventContent).RelatesTo = &event.RelatesTo{}
				content.Parsed.(*event.MessageEventContent).RelatesTo.SetThread(threadInfo.ThreadOrigin, threadInfo.ThreadLatest)
				threadInfo.ThreadLatest = portal.deterministicEventID(info.SlackAuthor, info.SlackTimestamp, partName)
				(*threadInfos)[info.SlackThreadTs] = threadInfo
			}
		}
	}
	t, err := portal.encrypt(intent, &content, event.EventMessage)
	if err != nil {
		portal.log.Errorfln("Error encrypting message for batch fill: %v", err)
		return nil
	}
	if t != event.EventMessage {
		intent.AddDoublePuppetValue(&content)
	}
	e := event.Event{
		Sender:    intent.UserID,
		Type:      t,
		Timestamp: parseSlackTimestamp(info.SlackTimestamp).UnixMilli(),
		Content:   content,
	}
	if portal.bridge.Config.Homeserver.Software == bridgeconfig.SoftwareHungry {
		e.ID = portal.deterministicEventID(info.SlackAuthor, info.SlackTimestamp, partName)
	}
	return &e
}

func (portal *Portal) backfill(userTeam *database.UserTeam, messages []slack.Message, isForward bool, prevEventID id.EventID) *mautrix.RespBatchSend {
	req := mautrix.ReqBatchSend{
		Events:             []*event.Event{},
		StateEventsAtStart: []*event.Event{},
	}
	if !isForward {
		if portal.FirstEventID == "" {
			portal.log.Errorln("No first event ID saved while backfilling backwards! Can't backfill")
			return nil
		}
		req.PrevEventID = portal.FirstEventID
	}
	addedMembers := make(map[id.UserID]*Puppet)
	convertedMessages := []ConvertedSlackMessage{}
	earliestBridged := ""

	// used to track the reply chains for threads
	threadInfos := make(map[string]SlackThreadInfo)

	// Slack sends messages in the backwards order
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Type == "message" && (message.SubType == "" || message.SubType == "me_message" || message.SubType == "bot_message") {
			converted := portal.ConvertSlackMessage(userTeam, &message.Msg)
			converted.SlackReactions = message.Reactions
			if message.ReplyCount != 0 {
				var err error
				converted.SlackThread, _, _, err = userTeam.Client.GetConversationReplies(&slack.GetConversationRepliesParameters{
					ChannelID: portal.Key.ChannelID,
					Timestamp: converted.SlackTimestamp,
				})
				if err != nil {
					portal.log.Warnfln("Error when fetching thread for message %s: %v", converted.SlackTimestamp, err)
					converted.SlackThread = nil
				} else if len(converted.SlackThread) != 0 {
					// Slack includes the origin message in the thread, so skip it
					converted.SlackThread = converted.SlackThread[1:]
					threadInfos[converted.SlackTimestamp] = SlackThreadInfo{
						ThreadOrigin: *portal.getLastEventID(&converted),
						ThreadLatest: *portal.getLastEventID(&converted),
					}
				}
			}
			if converted.Event != nil || len(converted.FileAttachments) != 0 {
				convertedMessages = append(convertedMessages, converted)
				if portal.bridge.Config.Homeserver.Software == bridgeconfig.SoftwareHungry {
					// Add all thread replies to this message also to convertedMessages
					for _, reply := range converted.SlackThread {
						convertedMessages = append(convertedMessages, portal.ConvertSlackMessage(userTeam, &reply.Msg))
					}
				}
				if parseSlackTimestamp(converted.SlackTimestamp).Before(parseSlackTimestamp(earliestBridged)) {
					earliestBridged = converted.SlackTimestamp
				}
			}
		}
	}

	for _, converted := range convertedMessages {
		ts := parseSlackTimestamp(converted.SlackTimestamp).UnixMilli()
		puppet := portal.bridge.GetPuppetByID(portal.Key.TeamID, converted.SlackAuthor)
		puppet.UpdateInfo(userTeam, nil)
		if puppet == nil || puppet.GetCustomOrGhostMXID() == "" {
			portal.log.Warnfln("No puppet found for %s while batch filling!", converted.SlackAuthor)
			continue
		} else {
			addedMembers[puppet.GetCustomOrGhostMXID()] = puppet
		}
		intent := puppet.IntentFor(portal)
		for i, file := range converted.FileAttachments {
			e := portal.makeBackfillEvent(intent, file.Event, fmt.Sprintf("file%d", i), &converted, &threadInfos)
			req.Events = append(req.Events, e)
		}
		if converted.Event != nil {
			e := portal.makeBackfillEvent(intent, converted.Event, "text", &converted, &threadInfos)
			req.Events = append(req.Events, e)
		}
		// Sending reactions in the same batch requires deterministic event IDs, so only do it on hungryserv
		if portal.bridge.Config.Homeserver.Software == bridgeconfig.SoftwareHungry {
			for _, reaction := range converted.SlackReactions {
				emoji := convertSlackReaction(reaction.Name)
				originalEventID := portal.getLastEventID(&converted)
				if originalEventID == nil {
					portal.log.Errorln("No converted event to react to!")
					continue
				}
				for _, user := range reaction.Users {
					var content event.ReactionEventContent
					content.RelatesTo = event.RelatesTo{
						Type:    event.RelAnnotation,
						EventID: *originalEventID,
						Key:     emoji,
					}
					reactionPuppet := portal.bridge.GetPuppetByID(portal.Key.TeamID, user)
					if reactionPuppet == nil {
						portal.log.Errorfln("Not backfilling reaction: can't find puppet for Slack user %s", user)
						continue
					}
					reactionPuppet.UpdateInfo(userTeam, nil)
					eventContent := event.Content{
						Raw:    map[string]interface{}{},
						Parsed: content,
					}
					if reactionPuppet.CustomMXID != "" {
						eventContent.Raw[doublePuppetKey] = doublePuppetValue
					}
					req.Events = append(req.Events, &event.Event{
						Sender:    reactionPuppet.GetCustomOrGhostMXID(),
						Type:      event.EventReaction,
						Timestamp: ts,
						Content:   eventContent,
					})
				}
			}
		}
	}
	if len(req.Events) == 0 {
		portal.log.Warnln("No messages to send in batch!")
		return nil
	}

	beforeFirstMessageTimestampMillis := req.Events[0].Timestamp - 1

	for _, puppet := range addedMembers {
		if portal.bridge.Config.Homeserver.Software == bridgeconfig.SoftwareHungry {
			// Hungryserv doesn't need state_events_at_start, it can figure out memberships automatically
			continue
		}
		mxid := puppet.GetCustomOrGhostMXID().String()
		content := event.MemberEventContent{
			Membership:  event.MembershipJoin,
			Displayname: puppet.Name,
			AvatarURL:   puppet.AvatarURL.CUString(),
		}
		inviteContent := content
		inviteContent.Membership = event.MembershipInvite
		req.StateEventsAtStart = append(req.StateEventsAtStart, &event.Event{
			Type:      event.StateMember,
			Sender:    portal.MainIntent().UserID,
			StateKey:  &mxid,
			Timestamp: beforeFirstMessageTimestampMillis,
			Content:   event.Content{Parsed: &inviteContent},
		}, &event.Event{
			Type:      event.StateMember,
			Sender:    puppet.GetCustomOrGhostMXID(),
			StateKey:  &mxid,
			Timestamp: beforeFirstMessageTimestampMillis,
			Content:   event.Content{Parsed: &content},
		})
	}

	if len(req.Events) == 0 {
		return nil
	}

	if isForward {
		portal.log.Debugln("Sending a dummy event to avoid forward extremity errors with backfill")
		_, err := portal.MainIntent().SendMessageEvent(portal.MXID, PreBackfillDummyEvent, struct{}{})
		if err != nil {
			portal.log.Warnln("Error sending pre-backfill dummy event:", err)
		}
		conversationInfo, err := userTeam.Client.GetConversationInfo(&slack.GetConversationInfoInput{
			ChannelID: portal.Key.ChannelID,
		})
		if err != nil || conversationInfo.LastRead == convertedMessages[len(convertedMessages)-1].SlackTimestamp {
			req.BeeperNewMessages = false
		} else {
			req.BeeperNewMessages = true
		}
	}

	resp, err := portal.MainIntent().BatchSend(portal.MXID, &req)
	if err != nil {
		portal.log.Errorln("Error batch sending messages:", err)
		return nil
	} else {
		txn, err := portal.bridge.DB.Begin()
		if err != nil {
			portal.log.Errorln("Failed to start transaction to save batch messages:", err)
			return nil
		}

		// Do the following block in the transaction
		{
			portal.finishBatch(txn, resp.EventIDs, &convertedMessages)
			if earliestBridged != "" {
				portal.FirstSlackID = earliestBridged
			}
			portal.Update(txn)
		}

		err = txn.Commit()
		if err != nil {
			portal.log.Errorln("Failed to commit transaction to save batch messages:", err)
			return nil
		}
		return resp
	}
}

func (portal *Portal) finishBatch(txn dbutil.Transaction, eventIDs []id.EventID, convertedMessages *[]ConvertedSlackMessage) {
	var idx int

	for _, converted := range *convertedMessages {
		for _, file := range converted.FileAttachments {
			if idx >= len(eventIDs) {
				portal.log.Errorln("Server returned fewer event IDs than events in our batch!")
				return
			}
			attachment := portal.bridge.DB.Attachment.New()
			attachment.Channel = portal.Key
			attachment.SlackFileID = file.SlackFileID
			attachment.SlackMessageID = converted.SlackTimestamp
			attachment.MatrixEventID = eventIDs[idx]
			attachment.SlackThreadID = converted.SlackThreadTs
			attachment.Insert(txn)
			idx += 1
		}
		if converted.Event != nil {
			if idx >= len(eventIDs) {
				portal.log.Errorln("Server returned fewer event IDs than events in our batch!")
				return
			}
			portal.markMessageHandled(txn, converted.SlackTimestamp, "", eventIDs[idx], converted.SlackAuthor)
			idx += 1
		}
		if portal.bridge.Config.Homeserver.Software == bridgeconfig.SoftwareHungry {
			for _, reaction := range converted.SlackReactions {
				for _, user := range reaction.Users {
					dbReaction := portal.bridge.DB.Reaction.New()
					dbReaction.Channel = portal.Key
					dbReaction.SlackMessageID = converted.SlackTimestamp
					dbReaction.MatrixEventID = eventIDs[idx]
					dbReaction.AuthorID = user
					dbReaction.MatrixName = convertSlackReaction(reaction.Name)
					dbReaction.SlackName = reaction.Name
					dbReaction.Insert(txn)
					idx += 1
				}
			}
		}
	}
	portal.sendPostBackfillDummy(eventIDs[0])
	portal.log.Infofln("Successfully sent %d events", len(eventIDs))
}

func (portal *Portal) sendPostBackfillDummy(insertionEventId id.EventID) {
	_, err := portal.MainIntent().SendMessageEvent(portal.MXID, HistorySyncMarker, map[string]interface{}{
		"org.matrix.msc2716.marker.insertion": insertionEventId,
		//"m.marker.insertion":                  insertionEventId,
	})
	if err != nil {
		portal.log.Errorln("Error sending post-backfill dummy event:", err)
		return
	}
}

func (portal *Portal) updateBackfillStatus(backfillState *database.BackfillState) {
	backfillStatus := "backfilling"
	if backfillState.BackfillComplete {
		backfillStatus = "complete"
	}

	_, err := portal.MainIntent().SendStateEvent(portal.MXID, BackfillStatusEvent, "", map[string]interface{}{
		"status": backfillStatus,
	})
	if err != nil {
		portal.log.Errorln("Error sending backfill status event:", err)
	}
}

// endregion
