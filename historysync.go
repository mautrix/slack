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
	"container/heap"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"github.com/slack-go/slack"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"

	"go.mau.fi/mautrix-slack/database"
)

// region history sync handling

type ThreadMessage struct {
	Message   slack.Message
	TimeStamp time.Time
}
type ThreadCollection []ThreadMessage

func (h ThreadCollection) Root() ThreadMessage { return h[0] }
func (h ThreadCollection) Len() int            { return len(h) }
func (h ThreadCollection) Less(i, j int) bool  { return h[i].TimeStamp.Before(h[j].TimeStamp) }
func (h ThreadCollection) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *ThreadCollection) Push(x any) {
	// Push and Pop use pointer receivers because they modify the slice's length,
	// not just its contents.
	*h = append(*h, x.(ThreadMessage))
}
func (h *ThreadCollection) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

func (bridge *SlackBridge) handleHistorySyncsLoop() {
	if !bridge.Config.Bridge.Backfill.Enable {
		return
	}

	// Backfills shouldn't be marked as dispatched during startup, this gives them a chance to retry
	bridge.DB.Backfill.UndispatchAll()

	bridge.HandleBackfillRequestsLoop()

}

func (portal *Portal) traditionalBackfill(userTeam *database.UserTeam) (int, error) {
	// collect only latest timestamp of each batch as ConversationBatch
	batches, err := portal.collectBatchesForTraditionalBackfill(userTeam)
	if err != nil {
		return 0, err
	}

	threadCollection := &ThreadCollection{}
	heap.Init(threadCollection)
	idx := 0

	// loop in reverse through each batch
	for i := len(batches) - 1; i >= 0; i-- {
		batch := batches[i]
		portal.log.Debugfln("Processing backfill batch %#v", batch)
		messageCount, err := portal.traditionalBackfillForBatch(userTeam, &batch, threadCollection)
		if err != nil {
			return idx, err
		}
		idx += messageCount
	}

	// process remaining threads in collection
	messageCount, err := portal.processThreads(userTeam, threadCollection, time.Now())
	return idx + messageCount, err
}

func (portal *Portal) processThreads(userTeam *database.UserTeam, threadCollection *ThreadCollection, upTo time.Time) (messageCount int, err error) {
	idx := 0
	for threadCollection.Len() > 0 && threadCollection.Root().TimeStamp.Before(upTo) {
		thread := heap.Pop(threadCollection).(ThreadMessage)
		err = portal.traditionalBackfillSingleMessage(userTeam, thread.Message)
		if err != nil {
			return idx, err
		}
		idx += 1
	}
	return idx, nil
}

func (portal *Portal) collectBatchesForTraditionalBackfill(userTeam *database.UserTeam) (batches []database.ConversationBatch, err error) {
	now := time.Now()
	initialTs := strconv.FormatInt(now.Unix(), 10)
	// get messages earlier than the given timestamp

	getReqParamsForTS := func(ts string) slack.GetConversationHistoryParameters {
		return slack.GetConversationHistoryParameters{
			ChannelID: portal.Key.ChannelID,
			Inclusive: false,
			Latest:    ts,
		}
	}

	foundMessages := true
	currentTs := initialTs
	for foundMessages {
		reqParams := getReqParamsForTS(currentTs)
		resp, err := userTeam.Client.GetConversationHistory(&reqParams)
		if err != nil {
			portal.log.Errorfln("Retrieving batch conversation info for portal %s failed, messages before ts %s", portal.Key, currentTs)
			return nil, err
		}
		messageCount := len(resp.Messages)
		if messageCount == 0 {
			portal.log.Infofln("Completed retrieving batch conversation info for portal %s", portal.Key)
			foundMessages = false
		} else {
			portal.log.Infofln("Found batch conversation info for portal %s, %d messages", portal.Key, messageCount)
			nextTs := resp.Messages[messageCount-1].Timestamp
			batches = append(batches, database.NewConversationBatch(portal.Key.ChannelID, currentTs))
			currentTs = nextTs
		}
	}

	return batches, nil
}

func (portal *Portal) traditionalBackfillForBatch(userTeam *database.UserTeam, batch *database.ConversationBatch, threadCollection *ThreadCollection) (int, error) {
	reqParams := slack.GetConversationHistoryParameters{
		ChannelID: portal.Key.ChannelID,
		Inclusive: false,
		Latest:    batch.TimeStamp,
	}
	resp, err := userTeam.Client.GetConversationHistory(&reqParams)
	if err != nil {
		portal.log.Errorfln("Retrieving conversation history for portal %s failed, messages before ts %s", portal.Key, batch.TimeStamp)
		return 0, err
	}
	portal.log.Infofln("Found %d messages in conversation history for portal %s, before ts %s", len(resp.Messages), portal.Key, batch.TimeStamp)
	if len(resp.Messages) == 0 {
		return 0, nil
	}

	idx := 0
	for i := len(resp.Messages) - 1; i >= 0; i-- {
		message := resp.Messages[i]

		// Space Optimization: process thread messages in the collection that occurred prior to the current message
		ts := parseSlackTimestamp(message.Msg.Timestamp)
		messageCount, err := portal.processThreads(userTeam, threadCollection, ts)
		if err != nil {
			return idx, err
		}
		idx += messageCount

		err = portal.traditionalBackfillSingleMessage(userTeam, message)
		if err != nil {
			portal.log.Errorfln("Portal backfill failed on message %#v: %#v", message, err)
			return idx, err
		}
		idx += 1

		err = portal.traditionalBackfillCollectThreads(userTeam, message, threadCollection)
		if err != nil {
			return idx, err
		}
	}
	return idx, err
}

func (portal *Portal) traditionalBackfillCollectThreads(userTeam *database.UserTeam, message slack.Message, threadCollection *ThreadCollection) error {
	if message.ReplyCount == 0 {
		return nil
	}

	ts := message.Msg.Timestamp
	hasMoreReplies := true
	nextCursor := ""
	var err error
	var messages []slack.Message

	for hasMoreReplies {
		messages, hasMoreReplies, nextCursor, err = userTeam.Client.GetConversationReplies(&slack.GetConversationRepliesParameters{
			ChannelID: portal.Key.ChannelID,
			Timestamp: ts,
			Cursor:    nextCursor,
		})
		if err != nil {
			return err
		}

		for _, message := range messages {
			if message.Msg.Timestamp != ts {
				tm := ThreadMessage{
					Message:   message,
					TimeStamp: parseSlackTimestamp(message.Msg.Timestamp),
				}
				heap.Push(threadCollection, tm)
			}
		}
	}

	return nil
}

func (portal *Portal) traditionalBackfillSingleMessage(userTeam *database.UserTeam, message slack.Message) error {
	if !(message.Type == "message" && (message.SubType == "" || message.SubType == "me_message" || message.SubType == "bot_message")) {
		portal.log.Infofln("Unsupported message for backfill: %#v", message)
		return nil
	}
	converted := portal.ConvertSlackMessage(userTeam, &message.Msg)
	ts := parseSlackTimestamp(converted.SlackTimestamp)

	converted.SlackReactions = message.Reactions
	if converted.Event == nil && len(converted.FileAttachments) == 0 {
		return nil
	}

	puppet := portal.bridge.GetPuppetByID(portal.Key.TeamID, converted.SlackAuthor)
	puppet.UpdateInfo(userTeam, true, nil)
	if puppet == nil {
		portal.log.Warnfln("No puppet found for %s while batch filling!", converted.SlackAuthor)
		return nil
	}
	intent := puppet.IntentFor(portal)
	var fileEventIDs []id.EventID
	for _, file := range converted.FileAttachments {
		resp, err := portal.sendMatrixMessage(intent, event.EventMessage, file.Event, nil, ts.UnixMilli())
		if err != nil {
			portal.log.Errorfln("Error while backfilling attached file: %#v", file)
			return err
		}
		fileEventIDs = append(fileEventIDs, resp.EventID)
	}
	var eventId id.EventID
	if converted.Event != nil {
		resp, err := portal.sendMatrixMessage(intent, event.EventMessage, converted.Event, nil, ts.UnixMilli())
		if err != nil {
			portal.log.Errorfln("Error while backfilling message: %#v", converted)
			return err
		}
		eventId = resp.EventID
	}

	txn, err := portal.bridge.DB.Begin()
	if err != nil {
		return err
	}

	for ix, file := range converted.FileAttachments {
		attachment := portal.bridge.DB.Attachment.New()
		attachment.Channel = portal.Key
		attachment.SlackFileID = file.SlackFileID
		attachment.SlackMessageID = converted.SlackTimestamp
		attachment.MatrixEventID = fileEventIDs[ix]
		attachment.SlackThreadID = converted.SlackThreadTs
		attachment.Insert(txn)
	}
	if converted.Event != nil {
		portal.log.Debugfln("Committing convertedMessage: %#v", converted)
		portal.markMessageHandled(txn, converted.SlackTimestamp, converted.SlackThreadTs, eventId, converted.SlackAuthor)
	}

	portal.Update(txn)

	err = txn.Commit()

	return err
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
	var isLatestEvents bool
	portal.latestEventBackfillLock.Lock()

	slackReqParams.Latest = portal.FirstSlackID
	if portal.FirstSlackID == "" {
		// Portal is empty -> events are latest
		isLatestEvents = true
	}

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
			resp, err := portal.backfill(userTeam, msgs, !backfillState.ImmediateComplete)
			if resp != nil && (resp.BaseInsertionEventID != "" || !isLatestEvents) {
				backfillState.MessageCount += len(msgs)
			} else if err != nil {
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

func (portal *Portal) backfill(userTeam *database.UserTeam, messages []slack.Message, isForward bool) (*mautrix.RespBatchSend, error) {
	req := mautrix.ReqBatchSend{
		Events:             []*event.Event{},
		StateEventsAtStart: []*event.Event{},
	}
	if !isForward {
		if portal.FirstEventID == "" {
			return nil, fmt.Errorf("no first event ID saved while backfilling backwards, can't backfill")
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
		puppet.UpdateInfo(userTeam, true, nil)
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
				slackReaction := strings.Trim(reaction.Name, ":")
				emoji := portal.bridge.GetEmoji(slackReaction, userTeam)

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
					extraContent := map[string]any{}
					if strings.HasPrefix(emoji, "mxc://") {
						extraContent["fi.mau.slack.reaction"] = map[string]any{
							"name": slackReaction,
							"mxc":  emoji,
						}
						if !portal.bridge.Config.Bridge.CustomEmojiReactions {
							content.RelatesTo.Key = slackReaction
						}
					}
					reactionPuppet := portal.bridge.GetPuppetByID(portal.Key.TeamID, user)
					if reactionPuppet == nil {
						portal.log.Errorfln("Not backfilling reaction: can't find puppet for Slack user %s", user)
						continue
					}
					reactionPuppet.UpdateInfo(userTeam, true, nil)
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
		portal.log.Debugln("No messages to send in batch!")
		return nil, nil
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
		return nil, nil
	}

	if isForward {
		req.BeeperNewMessages = true
		portal.log.Debugln("Sending a dummy event to avoid forward extremity errors with backfill")
		_, err := portal.MainIntent().SendMessageEvent(portal.MXID, PreBackfillDummyEvent, struct{}{})
		if err != nil {
			portal.log.Warnln("Error sending pre-backfill dummy event:", err)
		}
		conversationInfo, err := userTeam.Client.GetConversationInfo(&slack.GetConversationInfoInput{
			ChannelID: portal.Key.ChannelID,
		})
		if err != nil || conversationInfo.LastRead == convertedMessages[len(convertedMessages)-1].SlackTimestamp || time.Since(parseSlackTimestamp(convertedMessages[len(convertedMessages)-1].SlackTimestamp)).Hours() > float64(portal.bridge.Config.Bridge.Backfill.UnreadHoursThreshold) {
			req.BeeperMarkReadBy = userTeam.Key.MXID
		}
	}

	resp, err := portal.MainIntent().BatchSend(portal.MXID, &req)
	if err != nil {
		return nil, err
	} else {
		txn, err := portal.bridge.DB.Begin()
		if err != nil {
			return nil, err
		}

		// Do the following block in the transaction
		{
			portal.finishBatch(txn, resp.EventIDs, &convertedMessages)
			if earliestBridged != "" && (portal.FirstSlackID == "" || !isForward) {
				portal.FirstSlackID = earliestBridged
			}
			portal.Update(txn)
		}

		err = txn.Commit()
		if err != nil {
			return nil, err
		}
		return resp, nil
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

func (portal *Portal) ForwardBackfill() error {
	portal.slackMessageLock.Lock()
	defer portal.slackMessageLock.Unlock()

	backfillState := portal.bridge.DB.Backfill.GetBackfillState(&portal.Key)
	if backfillState != nil && !(backfillState.ImmediateComplete || backfillState.BackfillComplete) {
		portal.log.Debugln("Not forward backfilling, backfill is not complete yet")
		return nil
	}

	portal.log.Infoln("Forward backfilling messages after reconnect")
	userTeam := portal.bridge.DB.UserTeam.GetFirstUserTeamForPortal(&portal.Key)
	if userTeam == nil {
		portal.log.Errorln("Couldn't find logged in user for backfilling!")
		return nil
	}
	if userTeam.CookieToken != "" {
		userTeam.Client = slack.New(userTeam.Token, slack.OptionCookie("d", userTeam.CookieToken))
	} else {
		userTeam.Client = slack.New(userTeam.Token)
	}

	lastMessage := portal.bridge.DB.Message.GetLast(portal.Key)
	lastAttachment := portal.bridge.DB.Attachment.GetLast(portal.Key)
	var lastID string
	if lastMessage == nil && lastAttachment == nil {
		portal.log.Debugln("No last message for portal, can't forward backfill")
		return nil
	} else if lastMessage == nil {
		lastID = lastAttachment.SlackMessageID
	} else if lastAttachment == nil {
		lastID = lastMessage.SlackID
	} else {
		if parseSlackTimestamp(lastAttachment.SlackMessageID).After(parseSlackTimestamp(lastMessage.SlackID)) {
			lastID = lastAttachment.SlackMessageID
		} else {
			lastID = lastMessage.SlackID
		}
	}
	messages, err := userTeam.Client.GetConversationHistory(&slack.GetConversationHistoryParameters{
		ChannelID: portal.Key.ChannelID,
		Oldest:    lastID,
		Inclusive: false,
		Limit:     portal.bridge.Config.Bridge.Backfill.ImmediateMessages,
	})
	if err != nil {
		portal.log.Errorln("Error fetching messages for forward backfill", err)
		return err
	}
	_, err = portal.backfill(userTeam, messages.Messages, true)
	if err != nil {
		portal.log.Errorln("Error forward backfilling messages", err)
		return err
	}
	return nil
}

// endregion
