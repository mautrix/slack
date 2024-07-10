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

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-slack/pkg/slackid"
)

func (s *SlackClient) fetchChannelMembers(ctx context.Context, channelID string, limit int) (output []bridgev2.ChatMember) {
	var cursor string
	for limit > 0 {
		chunkLimit := limit
		if chunkLimit > 200 {
			chunkLimit = 100
		}
		membersChunk, nextCursor, err := s.Client.GetUsersInConversation(&slack.GetUsersInConversationParameters{
			ChannelID: channelID,
			Limit:     limit,
			Cursor:    cursor,
		})
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to get channel members")
			break
		}
		for _, member := range membersChunk {
			output = append(output, bridgev2.ChatMember{EventSender: s.makeEventSender(member)})
		}
		cursor = nextCursor
		limit -= len(membersChunk)
		if nextCursor == "" || len(membersChunk) < chunkLimit {
			break
		}
	}
	return
}

func (s *SlackClient) fetchChatInfo(ctx context.Context, channelID string, fetchMembers bool) (*bridgev2.ChatInfo, error) {
	info, err := s.Client.GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{
		ChannelID:         channelID,
		IncludeLocale:     true,
		IncludeNumMembers: true,
	})
	if err != nil {
		return nil, err
	}
	var members bridgev2.ChatMemberList
	var avatar *bridgev2.Avatar
	switch {
	case info.IsMpIM:
		members.IsFull = true
		members.Members = make([]bridgev2.ChatMember, len(info.Members))
		for i, member := range info.Members {
			members.Members[i] = bridgev2.ChatMember{EventSender: s.makeEventSender(member)}
		}
	case info.IsIM:
		members.IsFull = true
		selfMember := bridgev2.ChatMember{EventSender: s.makeEventSender(s.UserID)}
		otherMember := bridgev2.ChatMember{EventSender: s.makeEventSender(info.User)}
		if s.UserID == info.User {
			members.Members = []bridgev2.ChatMember{selfMember}
		} else {
			members.Members = []bridgev2.ChatMember{selfMember, otherMember}
		}
	case info.Name != "":
		if fetchMembers {
			members.Members = s.fetchChannelMembers(ctx, channelID, s.Main.Config.ParticipantSyncCount)
		}
		hasSelf := false
		for _, mem := range members.Members {
			if mem.IsFromMe {
				hasSelf = true
			}
		}
		if !hasSelf && info.IsMember {
			members.Members = append(members.Members, bridgev2.ChatMember{EventSender: s.makeEventSender(s.UserID)})
		}
		members.IsFull = len(members.Members) >= info.NumMembers
	default:
		return nil, fmt.Errorf("unrecognized channel type")
	}
	if s.Main.Config.WorkspaceAvatarInRooms && (info.Name != "" || info.IsMpIM) {
		avatar = &bridgev2.Avatar{
			ID:     s.TeamPortal.AvatarID,
			Remove: s.TeamPortal.AvatarID == "",
			MXC:    s.TeamPortal.AvatarMXC,
			Hash:   s.TeamPortal.AvatarHash,
		}
	}
	members.TotalMemberCount = info.NumMembers
	name := s.Main.Config.FormatChannelName(&ChannelNameParams{
		Channel:      info,
		TeamName:     s.BootResp.Team.Name,
		TeamDomain:   s.BootResp.Team.Domain,
		IsNoteToSelf: info.IsIM && info.User == s.UserID,
	})
	return &bridgev2.ChatInfo{
		Name:         ptr.Ptr(name),
		Topic:        ptr.Ptr(info.Topic.Value),
		Avatar:       avatar,
		Members:      &members,
		IsDirectChat: ptr.Ptr(info.IsIM),
		IsSpace:      ptr.Ptr(false),
	}, nil
}

func (s *SlackClient) getTeamInfo() *bridgev2.ChatInfo {
	name := s.Main.Config.FormatTeamName(&s.BootResp.Team)
	avatarURL, _ := s.BootResp.Team.Icon["image_230"].(string)
	if s.BootResp.Team.Icon["image_default"] == true {
		avatarURL = ""
	}
	return &bridgev2.ChatInfo{
		Name:   &name,
		Topic:  nil,
		Avatar: makeAvatar(avatarURL),
		Members: &bridgev2.ChatMemberList{
			IsFull:           false,
			TotalMemberCount: 0,
			Members:          []bridgev2.ChatMember{{EventSender: s.makeEventSender(s.UserID)}},
			PowerLevels:      &bridgev2.PowerLevelChanges{EventsDefault: ptr.Ptr(100)},
		},
		IsSpace: ptr.Ptr(true),
	}
}

func (s *SlackClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	teamID, channelID := slackid.ParsePortalID(portal.ID)
	if teamID == "" {
		return nil, fmt.Errorf("invalid portal ID %q", portal.ID)
	} else if channelID == "" {
		return s.getTeamInfo(), nil
	} else {
		return s.fetchChatInfo(ctx, channelID, portal.MXID == "" || !s.Main.Config.ParticipantSyncOnlyOnCreate)
	}
}

func makeAvatar(avatarURL string) *bridgev2.Avatar {
	return &bridgev2.Avatar{
		ID: networkid.AvatarID(avatarURL),
		Get: func(ctx context.Context) ([]byte, error) {
			return downloadPlainFile(ctx, avatarURL, "avatar")
		},
		Remove: avatarURL == "",
	}
}

func (s *SlackClient) fetchUserInfo(ctx context.Context, userID string) (*bridgev2.UserInfo, error) {
	var info *slack.User
	var botInfo *slack.Bot
	var err error
	if userID[0] == 'B' {
		botInfo, err = s.Client.GetBotInfoContext(ctx, userID)
	} else {
		info, err = s.Client.GetUserInfoContext(ctx, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}
	var name *string
	var avatarURL string
	isBot := userID == "USLACKBOT"
	if info != nil {
		name = ptr.Ptr(s.Main.Config.FormatDisplayname(info))
		avatarURL = info.Profile.ImageOriginal
		if avatarURL == "" && info.Profile.Image512 != "" {
			avatarURL = info.Profile.Image512
		}
		isBot = isBot || info.IsBot || info.IsAppUser
	} else if botInfo != nil {
		name = ptr.Ptr(s.Main.Config.FormatBotDisplayname(botInfo))
		avatarURL = botInfo.Icons.Image72
		isBot = true
	}
	return &bridgev2.UserInfo{
		Identifiers:  []string{fmt.Sprintf("slack-internal:%s", userID)},
		Name:         name,
		Avatar:       makeAvatar(avatarURL),
		IsBot:        &isBot,
		ExtraUpdates: nil,
	}, nil
}

func (s *SlackClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	_, userID := slackid.ParseUserID(ghost.ID)
	return s.fetchUserInfo(ctx, userID)
}
