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
	"time"

	"github.com/slack-go/slack"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"

	"go.mau.fi/mautrix-slack/database"
)

// region history sync handling

func (bridge *SlackBridge) handleHistorySyncsLoop() {
	if !bridge.Config.Bridge.HistorySync.Backfill {
		return
	}

	// Start the backfill queue.
	bridge.BackfillQueue = &BackfillQueue{
		BackfillQuery:   bridge.DB.Backfill,
		reCheckChannels: []chan bool{},
		log:             bridge.Log.Sub("BackfillQueue"),
	}

	forwardAndImmediate := []database.BackfillType{database.BackfillImmediate, database.BackfillForward}

	// Immediate backfills can be done in parallel
	// for i := 0; i < bridge.Config.Bridge.HistorySync.Immediate.WorkerCount; i++ {
	bridge.BackfillQueue.log.Debugln("Handling backfill requests for forward and immediate types")
	bridge.HandleBackfillRequestsLoop(forwardAndImmediate, []database.BackfillType{})
	// }

	// Deferred backfills should be handled synchronously so as not to
	// overload the homeserver. Users can configure their backfill stages
	// to be more or less aggressive with backfilling at this stage.
	go bridge.HandleBackfillRequestsLoop([]database.BackfillType{database.BackfillDeferred}, forwardAndImmediate)

}

func (bridge *SlackBridge) backfillInChunks(req *database.Backfill, portal *Portal) {
	portal.backfillLock.Lock()
	defer portal.backfillLock.Unlock()

	backfillState := bridge.DB.Backfill.GetBackfillState(&portal.Key)
	if backfillState == nil {
		backfillState = bridge.DB.Backfill.NewBackfillState(&portal.Key)
	}
	backfillState.SetProcessingBatch(true)
	defer backfillState.SetProcessingBatch(false)

	slackReqParams := slack.GetConversationHistoryParameters{
		ChannelID: req.Portal.ChannelID,
		Inclusive: false,
	}
	var forwardPrevID id.EventID
	var isLatestEvents bool
	portal.latestEventBackfillLock.Lock()
	if req.BackfillType == database.BackfillForward {
		lastMessage := portal.bridge.DB.Message.GetLast(portal.Key)
		if lastMessage == nil {
			bridge.Log.Warnfln("Empty portal %s, can't fetch Slack history for forward backfilling", portal.Key)
			return
		}
		slackReqParams.Oldest = lastMessage.SlackID
		forwardPrevID = lastMessage.MatrixID
		// Sending events at the end of the room (= latest events)
		isLatestEvents = true
	} else {
		slackReqParams.Latest = portal.FirstSlackID
		if portal.FirstSlackID == "" {
			// Portal is empty -> events are latest
			isLatestEvents = true
		}
	}
	if !isLatestEvents {
		// We'll use normal batch sending, so no need to keep blocking new message processing
		portal.latestEventBackfillLock.Unlock()
	} else {
		// This might involve sending events at the end of the room as non-historical events,
		// make sure we don't process messages until this is done.
		defer portal.latestEventBackfillLock.Unlock()
	}

	userTeam := bridge.DB.UserTeam.GetFirstUserTeamForPortal(req.Portal)
	if userTeam == nil {
		bridge.Log.Errorfln("Couldn't find logged in user with access to %s for backfilling!", req.Portal)
		return
	}
	bridge.Log.Debugfln("Got userteam %s with credentials %s %s", userTeam.Key, userTeam.Token, userTeam.CookieToken)
	if userTeam.CookieToken != "" {
		userTeam.Client = slack.New(userTeam.Token, slack.OptionCookie("d", userTeam.CookieToken))
	} else {
		userTeam.Client = slack.New(userTeam.Token)
	}
	resp, err := userTeam.Client.GetConversationHistory(&slackReqParams)
	if err != nil {
		bridge.Log.Errorfln("Error fetching Slack messages for backfilling %s: %v", req.Portal, resp)
		return
	}
	allMsgs := resp.Messages

	if len(allMsgs) == 0 {
		bridge.Log.Debugfln("Not backfilling %s: no bridgeable messages found", req.Portal)
		return
	}

	// Update the backfill status here after the room has been created.
	portal.updateBackfillStatus(backfillState)

	bridge.Log.Infofln("Backfilling %d messages in %s, %d messages at a time (queue ID: %d)", len(allMsgs), portal.Key, req.MaxBatchEvents, req.QueueID)
	toBackfill := allMsgs[0:]
	var insertionEventIds []id.EventID
	for len(toBackfill) > 0 {
		var msgs []slack.Message
		if len(toBackfill) <= req.MaxBatchEvents || req.MaxBatchEvents < 0 {
			msgs = toBackfill
			toBackfill = nil
		} else {
			msgs = toBackfill[:req.MaxBatchEvents]
			toBackfill = toBackfill[req.MaxBatchEvents:]
		}

		if len(msgs) > 0 {
			time.Sleep(time.Duration(req.BatchDelay) * time.Second)
			bridge.Log.Debugfln("Backfilling %d messages in %s (queue ID: %d)", len(msgs), portal.Key, req.QueueID)
			resp := portal.backfill(userTeam, msgs, req.BackfillType == database.BackfillForward, isLatestEvents, forwardPrevID)
			if resp != nil && (resp.BaseInsertionEventID != "" || !isLatestEvents) {
				insertionEventIds = append(insertionEventIds, resp.BaseInsertionEventID)
			}
		}
	}
	bridge.Log.Debugfln("Finished backfilling %d messages in %s (queue ID: %d)", len(allMsgs), portal.Key, req.QueueID)
	if len(insertionEventIds) > 0 {
		portal.sendPostBackfillDummy(
			parseSlackTimestamp(allMsgs[len(allMsgs)-1].Timestamp),
			insertionEventIds[0])
	}

	if !resp.HasMore {
		// Slack said there's no more history to backfill.
		backfillState.BackfillComplete = true
		backfillState.Upsert()
		portal.updateBackfillStatus(backfillState)
	}

	// TODO: add these config options
	// if bridge.Config.Bridge.HistorySync.UnreadHoursThreshold > 0 && conv.LastMessageTimestamp.Before(time.Now().Add(time.Duration(-bridge.Config.Bridge.HistorySync.UnreadHoursThreshold)*time.Hour)) {
	// 	user.markSelfReadFull(portal)
	// } else if bridge.Config.Bridge.SyncManualMarkedUnread {
	// 	user.markUnread(portal, true)
	// }
}

// func (bridge *SlackBridge) handleHistorySync(backfillQueue *BackfillQueue, evt *waProto.HistorySync) {
// 	if evt == nil || evt.SyncType == nil || evt.GetSyncType() == waProto.HistorySync_INITIAL_STATUS_V3 || evt.GetSyncType() == waProto.HistorySync_PUSH_NAME {
// 		return
// 	}
// 	description := fmt.Sprintf("type %s, %d conversations, chunk order %d, progress: %d", evt.GetSyncType(), len(evt.GetConversations()), evt.GetChunkOrder(), evt.GetProgress())
// 	bridge.Log.Infoln("Storing history sync with", description)

// 	for _, conv := range evt.GetConversations() {
// 		jid, err := types.ParseJID(conv.GetId())
// 		if err != nil {
// 			bridge.Log.Warnfln("Failed to parse chat JID '%s' in history sync: %v", conv.GetId(), err)
// 			continue
// 		} else if jid.Server == types.BroadcastServer {
// 			bridge.Log.Debugfln("Skipping broadcast list %s in history sync", jid)
// 			continue
// 		}
// 		portal := bridge.GetPortalByID(jid)

// 		historySyncConversation := user.bridge.DB.HistorySync.NewConversationWithValues(
// 			&portal.Key)
// 		historySyncConversation.Upsert()

// 		for _, rawMsg := range conv.GetMessages() {
// 			// Don't store messages that will just be skipped.
// 			msgEvt, err := user.Client.ParseWebMessage(portal.Key.JID, rawMsg.GetMessage())
// 			if err != nil {
// 				user.log.Warnln("Dropping historical message due to info parse error:", err)
// 				continue
// 			}

// 			msgType := getMessageType(msgEvt.Message)
// 			if msgType == "unknown" || msgType == "ignore" || msgType == "unknown_protocol" {
// 				continue
// 			}

// 			// Don't store unsupported messages.
// 			if !containsSupportedMessage(msgEvt.Message) {
// 				continue
// 			}

// 			message, err := bridge.DB.HistorySync.NewMessageWithValues(user.MXID, conv.GetId(), msgEvt.Info.ID, rawMsg)
// 			if err != nil {
// 				bridge.Log.Warnfln("Failed to save message %s in %s. Error: %+v", msgEvt.Info.ID, conv.GetId(), err)
// 				continue
// 			}
// 			message.Insert()
// 		}
// 	}

// 	// If this was the initial bootstrap, enqueue immediate backfills for the
// 	// most recent portals. If it's the last history sync event, start
// 	// backfilling the rest of the history of the portals.
// 	if bridge.Config.Bridge.HistorySync.Backfill {
// 		if evt.GetSyncType() != waProto.HistorySync_INITIAL_BOOTSTRAP && evt.GetProgress() < 98 {
// 			return
// 		}

// 		nMostRecent := bridge.DB.HistorySync.GetNMostRecentConversations(user.MXID, bridge.Config.Bridge.HistorySync.MaxInitialConversations)
// 		if len(nMostRecent) > 0 {
// 			// Find the portals for all of the conversations.
// 			portals := []*Portal{}
// 			for _, conv := range nMostRecent {
// 				jid, err := types.ParseJID(conv.ConversationID)
// 				if err != nil {
// 					bridge.Log.Warnfln("Failed to parse chat JID '%s' in history sync: %v", conv.ConversationID, err)
// 					continue
// 				}
// 				portals = append(portals, bridge.GetPortalByID(jid))
// 			}

// 			switch evt.GetSyncType() {
// 			case waProto.HistorySync_INITIAL_BOOTSTRAP:
// 				// Enqueue immediate backfills for the most recent messages first.
// 				user.EnqueueImmedateBackfills(portals)
// 			case waProto.HistorySync_FULL, waProto.HistorySync_RECENT:
// 				user.EnqueueForwardBackfills(portals)
// 				// Enqueue deferred backfills as configured.
// 				user.EnqueueDeferredBackfills(portals)
// 			}

// 			// Tell the queue to check for new backfill requests.
// 			backfillQueue.ReCheck()
// 		}
// 	}
// }

func (bridge *SlackBridge) EnqueueImmedateBackfills(portals []*Portal) {
	for priority, portal := range portals {
		maxMessages := bridge.Config.Bridge.HistorySync.ImmediateEvents
		initialBackfill := bridge.DB.Backfill.NewWithValues(database.BackfillImmediate, priority, &portal.Key, maxMessages, maxMessages, 0)
		initialBackfill.Insert()
	}
}

func (bridge *SlackBridge) EnqueueDeferredBackfills(portals []*Portal) {
	numPortals := len(portals)
	for stageIdx, backfillStage := range bridge.Config.Bridge.HistorySync.Deferred {
		for portalIdx, portal := range portals {
			backfillMessages := bridge.DB.Backfill.NewWithValues(
				database.BackfillDeferred, stageIdx*numPortals+portalIdx, &portal.Key, backfillStage.MaxBatchEvents, -1, backfillStage.BatchDelay)
			backfillMessages.Insert()
		}
	}
}

func (bridge *SlackBridge) EnqueueForwardBackfills(portals []*Portal) {
	for priority, portal := range portals {
		lastMsg := bridge.DB.Message.GetLast(portal.Key)
		if lastMsg == nil {
			continue
		}
		backfill := bridge.DB.Backfill.NewWithValues(
			database.BackfillForward, priority, &portal.Key, -1, -1, 0)
		backfill.Insert()
	}
}

// endregion
// region Portal backfilling

// func (portal *Portal) deterministicEventID(sender string, messageID string, partName string) id.EventID {
// 	data := fmt.Sprintf("%s/slack/%s/%s", portal.MXID, sender, messageID)
// 	if partName != "" {
// 		data += "/" + partName
// 	}
// 	sum := sha256.Sum256([]byte(data))
// 	return id.EventID(fmt.Sprintf("$%s:slack.com", base64.RawURLEncoding.EncodeToString(sum[:])))
// }

var (
	PortalCreationDummyEvent = event.Type{Type: "fi.mau.dummy.portal_created", Class: event.MessageEventType}
	PreBackfillDummyEvent    = event.Type{Type: "fi.mau.dummy.pre_backfill", Class: event.MessageEventType}

	HistorySyncMarker = event.Type{Type: "org.matrix.msc2716.marker", Class: event.MessageEventType}

	BackfillStatusEvent = event.Type{Type: "com.beeper.backfill_status", Class: event.StateEventType}
)

func (portal *Portal) backfill(userTeam *database.UserTeam, messages []slack.Message, isForward, isLatest bool, prevEventID id.EventID) *mautrix.RespBatchSend {
	req := mautrix.ReqBatchSend{
		PrevEventID:        portal.FirstEventID,
		BatchID:            portal.NextBatchID,
		Events:             []*event.Event{},
		StateEventsAtStart: []*event.Event{},
	}
	addedMembers := make(map[id.UserID]*Puppet)
	convertedMessages := []ConvertedSlackMessage{}
	earliestBridged := ""

	// Slack sends messages in the backwards order
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Type == "message" && (message.SubType == "" || message.SubType == "me_message" || message.SubType == "bot_message") {
			converted := portal.ConvertSlackMessage(userTeam, &message.Msg)
			if converted.Event != nil || len(converted.FileAttachments) != 0 {
				convertedMessages = append(convertedMessages, converted)
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
		var puppetID id.UserID
		if puppet == nil || puppet.MXID == "" {
			portal.log.Warnfln("No puppet found for %s while batch filling!", converted.SlackAuthor)
			continue
		} else {
			addedMembers[puppet.MXID] = puppet
			puppetID = puppet.MXID
		}
		intent := puppet.IntentFor(portal)
		for _, file := range converted.FileAttachments {
			content := event.Content{
				Parsed: file.Event,
			}
			t, err := portal.encrypt(intent, &content, event.EventMessage)
			if err != nil {
				portal.log.Errorfln("Error encrypting message for batch fill: %v", err)
				continue
			}
			req.Events = append(req.Events, &event.Event{
				Sender:    puppetID,
				Type:      t,
				Timestamp: ts,
				Content:   content,
			})
		}
		if converted.Event != nil {
			content := event.Content{
				Parsed: converted.Event,
			}
			t, err := portal.encrypt(intent, &content, event.EventMessage)
			if err != nil {
				portal.log.Errorfln("Error encrypting message for batch fill: %v", err)
				continue
			}
			req.Events = append(req.Events, &event.Event{
				Sender:    puppetID,
				Type:      t,
				Timestamp: ts,
				Content:   content,
			})
		}
	}
	if len(req.Events) == 0 {
		portal.log.Warnln("No messages to send in batch!")
		return nil
	}
	if len(req.BatchID) == 0 {
		portal.log.Debugln("Sending a dummy event to avoid forward extremity errors with backfill")
		_, err := portal.MainIntent().SendMessageEvent(portal.MXID, event.Type{Type: "fi.mau.dummy.pre_backfill", Class: event.MessageEventType}, struct{}{})
		if err != nil {
			portal.log.Warnln("Error sending pre-backfill dummy event:", err)
		}
	}

	beforeFirstMessageTimestampMillis := req.Events[0].Timestamp - 1

	for _, puppet := range addedMembers {
		if portal.bridge.Config.Homeserver.Software == bridgeconfig.SoftwareHungry {
			// Hungryserv doesn't need state_events_at_start, it can figure out memberships automatically
			continue
		}
		mxid := puppet.MXID.String()
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
			Sender:    puppet.MXID,
			StateKey:  &mxid,
			Timestamp: beforeFirstMessageTimestampMillis,
			Content:   event.Content{Parsed: &content},
		})
	}

	if len(req.Events) == 0 {
		return nil
	}

	if len(req.BatchID) == 0 || isForward {
		portal.log.Debugln("Sending a dummy event to avoid forward extremity errors with backfill")
		_, err := portal.MainIntent().SendMessageEvent(portal.MXID, PreBackfillDummyEvent, struct{}{})
		if err != nil {
			portal.log.Warnln("Error sending pre-backfill dummy event:", err)
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
			portal.NextBatchID = resp.NextBatchID
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

// func (portal *Portal) requestMediaRetries(source *User, eventIDs []id.EventID, infos []*wrappedInfo) {
// 	for i, info := range infos {
// 		if info != nil && info.Error == database.MsgErrMediaNotFound && info.MediaKey != nil {
// 			switch portal.bridge.Config.Bridge.HistorySync.MediaRequests.RequestMethod {
// 			case config.MediaRequestMethodImmediate:
// 				err := source.Client.SendMediaRetryReceipt(info.MessageInfo, info.MediaKey)
// 				if err != nil {
// 					portal.log.Warnfln("Failed to send post-backfill media retry request for %s: %v", info.ID, err)
// 				} else {
// 					portal.log.Debugfln("Sent post-backfill media retry request for %s", info.ID)
// 				}
// 			case config.MediaRequestMethodLocalTime:
// 				req := portal.bridge.DB.MediaBackfillRequest.NewMediaBackfillRequestWithValues(source.MXID, &portal.Key, eventIDs[i], info.MediaKey)
// 				req.Upsert()
// 			}
// 		}
// 	}
// }

// func (portal *Portal) appendBatchEvents(converted *ConvertedMessage, info *types.MessageInfo, raw *waProto.WebMessageInfo, eventsArray *[]*event.Event, infoArray *[]*wrappedInfo) error {
// 	if portal.bridge.Config.Bridge.CaptionInMessage {
// 		converted.MergeCaption()
// 	}
// 	mainEvt, err := portal.wrapBatchEvent(info, converted.Intent, converted.Type, converted.Content, converted.Extra, "")
// 	if err != nil {
// 		return err
// 	}
// 	expirationStart := raw.GetEphemeralStartTimestamp()
// 	mainInfo := &wrappedInfo{
// 		MessageInfo:     info,
// 		Type:            database.MsgNormal,
// 		Error:           converted.Error,
// 		MediaKey:        converted.MediaKey,
// 		ExpirationStart: expirationStart,
// 		ExpiresIn:       converted.ExpiresIn,
// 	}
// 	if converted.Caption != nil {
// 		captionEvt, err := portal.wrapBatchEvent(info, converted.Intent, converted.Type, converted.Caption, nil, "caption")
// 		if err != nil {
// 			return err
// 		}
// 		*eventsArray = append(*eventsArray, mainEvt, captionEvt)
// 		*infoArray = append(*infoArray, mainInfo, nil)
// 	} else {
// 		*eventsArray = append(*eventsArray, mainEvt)
// 		*infoArray = append(*infoArray, mainInfo)
// 	}
// 	if converted.MultiEvent != nil {
// 		for i, subEvtContent := range converted.MultiEvent {
// 			subEvt, err := portal.wrapBatchEvent(info, converted.Intent, converted.Type, subEvtContent, nil, fmt.Sprintf("multi-%d", i))
// 			if err != nil {
// 				return err
// 			}
// 			*eventsArray = append(*eventsArray, subEvt)
// 			*infoArray = append(*infoArray, nil)
// 		}
// 	}
// 	// Sending reactions in the same batch requires deterministic event IDs, so only do it on hungryserv
// 	if portal.bridge.Config.Homeserver.Software == bridgeconfig.SoftwareHungry {
// 		for _, reaction := range raw.GetReactions() {
// 			reactionEvent, reactionInfo := portal.wrapBatchReaction(source, reaction, mainEvt.ID, info.Timestamp)
// 			if reactionEvent != nil {
// 				*eventsArray = append(*eventsArray, reactionEvent)
// 				*infoArray = append(*infoArray, &wrappedInfo{
// 					MessageInfo:    reactionInfo,
// 					ReactionTarget: info.ID,
// 					Type:           database.MsgReaction,
// 				})
// 			}
// 		}
// 	}
// 	return nil
// }

// func (portal *Portal) wrapBatchReaction(source *User, reaction *waProto.Reaction, mainEventID id.EventID, mainEventTS time.Time) (reactionEvent *event.Event, reactionInfo *types.MessageInfo) {
// 	var senderJID types.JID
// 	if reaction.GetKey().GetFromMe() {
// 		senderJID = source.JID.ToNonAD()
// 	} else if reaction.GetKey().GetParticipant() != "" {
// 		senderJID, _ = types.ParseJID(reaction.GetKey().GetParticipant())
// 	} else if portal.IsPrivateChat() {
// 		senderJID = portal.Key.JID
// 	}
// 	if senderJID.IsEmpty() {
// 		return
// 	}
// 	reactionInfo = &types.MessageInfo{
// 		MessageSource: types.MessageSource{
// 			Chat:     portal.Key.JID,
// 			Sender:   senderJID,
// 			IsFromMe: reaction.GetKey().GetFromMe(),
// 			IsGroup:  portal.IsGroupChat(),
// 		},
// 		ID:        reaction.GetKey().GetId(),
// 		Timestamp: mainEventTS,
// 	}
// 	puppet := portal.getMessagePuppet(source, reactionInfo)
// 	if puppet == nil {
// 		return
// 	}
// 	intent := puppet.IntentFor(portal)
// 	content := event.ReactionEventContent{
// 		RelatesTo: event.RelatesTo{
// 			Type:    event.RelAnnotation,
// 			EventID: mainEventID,
// 			Key:     variationselector.Add(reaction.GetText()),
// 		},
// 	}
// 	if rawTS := reaction.GetSenderTimestampMs(); rawTS >= mainEventTS.UnixMilli() && rawTS <= time.Now().UnixMilli() {
// 		reactionInfo.Timestamp = time.UnixMilli(rawTS)
// 	}
// 	reactionEvent = &event.Event{
// 		ID:        portal.deterministicEventID(senderJID, reactionInfo.ID, ""),
// 		Type:      event.EventReaction,
// 		Content:   event.Content{Parsed: content},
// 		Sender:    intent.UserID,
// 		Timestamp: reactionInfo.Timestamp.UnixMilli(),
// 	}
// 	return
// }

// func (portal *Portal) wrapBatchEvent(slackSender string, slackID string, timestamp int64, intent *appservice.IntentAPI, eventType event.Type, content *event.MessageEventContent, extraContent map[string]interface{}, partName string) (*event.Event, error) {
// 	wrappedContent := event.Content{
// 		Parsed: content,
// 		Raw:    extraContent,
// 	}
// 	newEventType, err := portal.encrypt(intent, &wrappedContent, eventType)
// 	if err != nil {
// 		return nil, err
// 	}
// 	if newEventType != eventType {
// 		intent.AddDoublePuppetValue(&wrappedContent)
// 	}
// 	var eventID id.EventID
// 	if portal.bridge.Config.Homeserver.Software == bridgeconfig.SoftwareHungry {
// 		eventID = portal.deterministicEventID(slackSender, slackID, partName)
// 	}

// 	return &event.Event{
// 		ID:        eventID,
// 		Sender:    intent.UserID,
// 		Type:      newEventType,
// 		Timestamp: timestamp,
// 		Content:   wrappedContent,
// 	}, nil
// }

func (portal *Portal) finishBatch(txn dbutil.Transaction, eventIDs []id.EventID, convertedMessages *[]ConvertedSlackMessage) {
	var idx int
	// This is a dubious way to match up the received event IDs back to the converted slack messages
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
			attachment.Insert()
			idx += 1
		}
		if converted.Event != nil {
			if idx >= len(eventIDs) {
				portal.log.Errorln("Server returned fewer event IDs than events in our batch!")
				return
			}
			portal.markMessageHandled(converted.SlackTimestamp, "", eventIDs[idx], converted.SlackAuthor)
			idx += 1
		}
	}
	portal.log.Infofln("Successfully sent %d events", len(eventIDs))
}

func (portal *Portal) sendPostBackfillDummy(lastTimestamp time.Time, insertionEventId id.EventID) {
	_, err := portal.MainIntent().SendMessageEvent(portal.MXID, HistorySyncMarker, map[string]interface{}{
		"org.matrix.msc2716.marker.insertion": insertionEventId,
		//"m.marker.insertion":                  insertionEventId,
	})
	if err != nil {
		portal.log.Errorln("Error sending post-backfill dummy event:", err)
		return
	}
	// msg := portal.bridge.DB.Message.New()
	// msg.Channel = portal.Key
	// msg.MatrixID = resp.EventID
	// msg.JID = types.MessageID(resp.EventID)
	// msg.Timestamp = lastTimestamp.Add(1 * time.Second)
	// msg.Sent = true
	// msg.Type = database.MsgFake
	// msg.Insert(nil)
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
