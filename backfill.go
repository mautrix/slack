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
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"
	"golang.org/x/exp/slices"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-slack/database"
)

func (portal *Portal) MissedForwardBackfill(ctx context.Context, source *UserTeam, expectedLastMessage string) {
	portal.forwardBackfillLock.Lock()
	defer portal.forwardBackfillLock.Unlock()
	lastMessage, err := portal.bridge.DB.Message.GetLastNonThreadInChannel(ctx, portal.PortalKey)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get last message in channel")
		return
	} else if !parseSlackTimestamp(lastMessage.MessageID).Before(parseSlackTimestamp(expectedLastMessage)) {
		return
	}
	portal.unlockedForwardBackfill(ctx, source, lastMessage.MessageID, portal.bridge.Config.Bridge.Backfill.MissedMessages)
}

func (portal *Portal) PostCreateForwardBackfill(ctx context.Context, source *UserTeam) {
	if portal.forwardBackfillLock.TryLock() {
		panic("PostCreateForwardBackfill called without lock")
	}
	defer portal.forwardBackfillLock.Unlock()
	portal.unlockedForwardBackfill(ctx, source, "", portal.bridge.Config.Bridge.Backfill.InitialMessages)
}

func (portal *Portal) unlockedForwardBackfill(ctx context.Context, source *UserTeam, lastMessageID string, limit int) {
	if !portal.bridge.Config.Bridge.Backfill.Enable {
		return
	}
	log := zerolog.Ctx(ctx)

	var cursor string
	var collectedMessages []*slack.Message
	for {
		chunkLimit := limit
		if limit < 0 || limit > 200 {
			chunkLimit = 200
		}
		chunk, err := source.Client.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
			ChannelID:          portal.ChannelID,
			Cursor:             cursor,
			Oldest:             lastMessageID,
			Limit:              chunkLimit,
			Inclusive:          false,
			IncludeAllMetadata: false,
		})
		if err != nil {
			log.Err(err).Msg("Failed to fetch conversation history chunk")
			return
		}
		collectedMessages = slices.Grow(collectedMessages, len(chunk.Messages))
		for i := range chunk.Messages {
			collectedMessages = append(collectedMessages, &chunk.Messages[i])
		}
		if len(chunk.Messages) == 0 || !chunk.HasMore || (limit > 0 && limit-len(chunk.Messages) <= 0) {
			break
		}
		cursor = chunk.ResponseMetadata.Cursor
		limit -= len(chunk.Messages)
	}
	portal.sendBackfill(ctx, source, collectedMessages, true)
}

func (portal *Portal) DoHistoricalBackfillTask(ctx context.Context, source *UserTeam, task *database.UserPortal) {
	portal.backfillLock.Lock()
	defer portal.backfillLock.Unlock()
	log := zerolog.Ctx(ctx)
	batchLimit := portal.bridge.Config.Bridge.Backfill.Incremental.MessagesPerBatch
	totalLimit := portal.bridge.Config.Bridge.Backfill.Incremental.MaxMessages.LimitFor(portal.Type)
	if totalLimit == 0 {
		log.Debug().Msg("Backfill limit is 0, marking task as completed")
		task.Finished = true
		return
	}
	remainingTotalLimit := totalLimit - task.BackfilledCount
	if remainingTotalLimit <= 0 {
		log.Debug().
			Int("total_limit", totalLimit).
			Int("backfilled_count", task.BackfilledCount).
			Msg("Backfill task already at or above limit, marking as completed")
		task.Finished = true
		return
	} else if remainingTotalLimit < batchLimit {
		batchLimit = remainingTotalLimit
	}
	log.Debug().
		Int("default_batch_limit", portal.bridge.Config.Bridge.Backfill.Incremental.MessagesPerBatch).
		Int("batch_limit", batchLimit).
		Int("total_limit", totalLimit).
		Int("remaining_total_limit", remainingTotalLimit).
		Int("backfilled_count", task.BackfilledCount).
		Msg("Fetching messages for backfill")
	chunk, err := source.Client.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
		ChannelID:          portal.ChannelID,
		Latest:             portal.OldestSlackMessageID,
		Limit:              batchLimit,
		Inclusive:          false,
		IncludeAllMetadata: false,
	})
	if err != nil {
		log.Err(err).Msg("Failed to fetch conversation history chunk")
		task.CooldownUntil = time.Now().Add(BackfillErrorCooldown)
		return
	}
	pointerChunk := make([]*slack.Message, len(chunk.Messages))
	for i := range chunk.Messages {
		pointerChunk[i] = &chunk.Messages[i]
	}
	portal.sendBackfill(ctx, source, pointerChunk, false)
}

func (portal *Portal) deterministicEventID(evt *slack.Message, part database.PartID) id.EventID {
	data := fmt.Sprintf("%s/slack/%s/%s/%s/%s", portal.MXID, evt.Team, evt.Channel, evt.Timestamp, part.String())
	sum := sha256.Sum256([]byte(data))
	return id.EventID(fmt.Sprintf("$%s:slack.com", base64.RawURLEncoding.EncodeToString(sum[:])))
}

type batchEventInfo struct {
	*slack.Message
	Part   database.PartID
	Intent *appservice.IntentAPI
}

func (portal *Portal) sendBackfill(ctx context.Context, source *UserTeam, messages []*slack.Message, forward bool) {
	log := zerolog.Ctx(ctx)
	events := make([]*event.Event, 0, len(messages))
	infos := make([]batchEventInfo, 0, len(messages))
	for _, message := range messages {
		converted := portal.MsgConv.ToMatrix(ctx, &message.Msg)
		if portal.bridge.Config.Bridge.CaptionInMessage {
			converted.MergeCaption()
		}
		slackAuthor := message.User
		if slackAuthor == "" {
			slackAuthor = message.BotID
		}
		sender := portal.Team.GetPuppetByID(slackAuthor)
		sender.UpdateInfoIfNecessary(ctx, source)
		intent := sender.IntentFor(portal)
		for _, part := range converted.Parts {
			content := event.Content{
				Parsed: part.Content,
				Raw:    part.Extra,
			}
			eventType, err := portal.encrypt(ctx, intent, &content, part.Type)
			if err != nil {
				log.Err(err).Msg("Failed to encrypt message part")
				continue
			}
			events = append(events, &event.Event{
				Sender:    intent.UserID,
				Timestamp: parseSlackTimestamp(message.Timestamp).UnixMilli(),
				ID:        portal.deterministicEventID(message, part.PartID),
				RoomID:    portal.MXID,
				Type:      eventType,
				Content:   content,
			})
			infos = append(infos, batchEventInfo{
				Message: message,
				Part:    part.PartID,
				Intent:  intent,
			})
		}
	}
	var eventIDs []id.EventID
	if portal.bridge.SpecVersions.Supports(mautrix.BeeperFeatureBatchSending) {
		resp, err := portal.MainIntent().BeeperBatchSend(ctx, portal.MXID, &mautrix.ReqBeeperBatchSend{
			ForwardIfNoMessages: forward,
			Forward:             false,
			SendNotification:    false,
			MarkReadBy:          "",
			Events:              events,
		})
		if err != nil {

		}
		eventIDs = resp.EventIDs
	} else if forward {
		eventIDs = make([]id.EventID, len(events))
		for i, evt := range events {
			resp, err := infos[i].Intent.SendMassagedMessageEvent(ctx, evt.RoomID, evt.Type, &evt.Content, evt.Timestamp)
			if err != nil {

			}
			eventIDs[i] = resp.EventID
		}
	}
	if !forward || portal.OldestSlackMessageID == "" {
		portal.OldestSlackMessageID = messages[0].Timestamp
	}
	//for i, evtID := range eventIDs {
	//
	//}
}
