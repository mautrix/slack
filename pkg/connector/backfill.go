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
	"slices"

	"github.com/rs/zerolog"

	"github.com/slack-go/slack"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-slack/pkg/slackid"
)

var (
	_ bridgev2.BackfillingNetworkAPI           = (*SlackClient)(nil)
	_ bridgev2.BackfillingNetworkAPIWithLimits = (*SlackClient)(nil)
)

func (s *SlackClient) GetBackfillMaxBatchCount(ctx context.Context, portal *bridgev2.Portal, task *database.BackfillTask) int {
	switch portal.RoomType {
	case database.RoomTypeSpace:
		return 0
	case database.RoomTypeDefault:
		return s.Main.br.Config.Backfill.Queue.GetOverride("channel")
	case database.RoomTypeGroupDM:
		return s.Main.br.Config.Backfill.Queue.GetOverride("group_dm")
	case database.RoomTypeDM:
		return s.Main.br.Config.Backfill.Queue.GetOverride("dm")
	default:
		return s.Main.br.Config.Backfill.Queue.MaxBatches
	}
}

func (s *SlackClient) FetchMessages(ctx context.Context, params bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	if s.Client == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	_, channelID := slackid.ParsePortalID(params.Portal.ID)
	if channelID == "" {
		return nil, fmt.Errorf("invalid channel ID")
	}
	var anchorMessageID string
	if params.AnchorMessage != nil {
		_, _, anchorMessageID, _ = slackid.ParseMessageID(params.AnchorMessage.ID)
	}
	slackParams := &slack.GetConversationHistoryParameters{
		ChannelID:          channelID,
		Cursor:             string(params.Cursor),
		Latest:             anchorMessageID,
		Limit:              min(params.Count, 999),
		Inclusive:          false,
		IncludeAllMetadata: false,
	}
	if params.Forward {
		slackParams.Oldest = slackParams.Latest
		slackParams.Latest = ""
	}
	var chunk *slack.GetConversationHistoryResponse
	var err error
	var threadTS string
	if params.ThreadRoot != "" {
		var ok bool
		_, _, threadTS, ok = slackid.ParseMessageID(params.ThreadRoot)
		if !ok {
			return nil, fmt.Errorf("invalid thread root ID")
		}
		chunk, err = s.Client.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
			GetConversationHistoryParameters: *slackParams,
			Timestamp:                        threadTS,
		})
	} else {
		chunk, err = s.Client.GetConversationHistoryContext(ctx, slackParams)
	}
	if err != nil {
		return nil, err
	}
	convertedMessages := make([]*bridgev2.BackfillMessage, 0, len(chunk.Messages))
	var maxMsgID string
	for _, msg := range chunk.Messages {
		if threadTS != "" && msg.Timestamp == threadTS {
			continue
		} else if threadTS == "" && msg.ThreadTimestamp != "" && msg.ThreadTimestamp != msg.Timestamp {
			continue
		}
		convertedMessages = append(convertedMessages, s.wrapBackfillMessage(ctx, params.Portal, &msg.Msg, threadTS != ""))
		if maxMsgID < msg.Timestamp {
			maxMsgID = msg.Timestamp
		}
	}
	slices.Reverse(convertedMessages)
	lastRead := s.getLastReadCache(channelID)
	return &bridgev2.FetchMessagesResponse{
		Messages: convertedMessages,
		Cursor:   networkid.PaginationCursor(chunk.ResponseMetadata.Cursor),
		HasMore:  chunk.HasMore,
		Forward:  params.Forward,
		MarkRead: lastRead != "" && maxMsgID != "" && lastRead >= maxMsgID,
	}, nil
}

func (s *SlackClient) wrapBackfillMessage(ctx context.Context, portal *bridgev2.Portal, msg *slack.Msg, inThread bool) *bridgev2.BackfillMessage {
	senderID := msg.User
	if senderID == "" {
		senderID = msg.BotID
	}
	sender := s.makeEventSender(senderID)
	ghost, err := s.Main.br.GetGhostByID(ctx, sender.Sender)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get ghost")
	}
	var intent bridgev2.MatrixAPI
	if ghost == nil {
		intent = portal.Bridge.Bot
	} else {
		intent = ghost.Intent
	}
	_, channelID := slackid.ParsePortalID(portal.ID)
	out := &bridgev2.BackfillMessage{
		ConvertedMessage: s.Main.MsgConv.ToMatrix(ctx, portal, intent, s.UserLogin, msg),
		Sender:           sender,
		ID:               slackid.MakeMessageID(s.TeamID, channelID, msg.Timestamp),
		Timestamp:        slackid.ParseSlackTimestamp(msg.Timestamp),
		Reactions:        make([]*bridgev2.BackfillReaction, 0, len(msg.Reactions)),
	}
	if msg.ReplyCount > 0 && !inThread {
		out.ShouldBackfillThread = true
		out.LastThreadMessage = slackid.MakeMessageID(s.TeamID, channelID, msg.LatestReply)
	}
	for _, reaction := range msg.Reactions {
		emoji, extraContent := s.getReactionInfo(ctx, reaction.Name)
		for _, user := range reaction.Users {
			out.Reactions = append(out.Reactions, &bridgev2.BackfillReaction{
				Sender:       s.makeEventSender(user),
				EmojiID:      networkid.EmojiID(reaction.Name),
				Emoji:        emoji,
				ExtraContent: extraContent,
			})
		}
	}
	return out
}
