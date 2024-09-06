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
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"
	"go.mau.fi/util/jsontime"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-slack/pkg/slackid"
)

const ChatInfoCacheExpiry = 1 * time.Hour

func (s *SlackClient) fetchChatInfoWithCache(ctx context.Context, channelID string) (*slack.Channel, error) {
	s.chatInfoCacheLock.Lock()
	defer s.chatInfoCacheLock.Unlock()
	if cached, ok := s.chatInfoCache[channelID]; ok && time.Since(cached.ts) < ChatInfoCacheExpiry {
		return cached.data, nil
	}
	info, err := s.Client.GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{
		ChannelID:         channelID,
		IncludeLocale:     true,
		IncludeNumMembers: true,
	})
	if err != nil {
		return nil, err
	}
	s.chatInfoCache[channelID] = chatInfoCacheEntry{
		ts:   time.Now(),
		data: info,
	}
	return info, nil
}

func (s *SlackClient) fetchChannelMembers(ctx context.Context, channelID string, limit int) (output map[networkid.UserID]bridgev2.ChatMember) {
	var cursor string
	output = make(map[networkid.UserID]bridgev2.ChatMember)
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
			evtSender := s.makeEventSender(member)
			output[evtSender.Sender] = bridgev2.ChatMember{EventSender: evtSender}
		}
		cursor = nextCursor
		limit -= len(membersChunk)
		if nextCursor == "" || len(membersChunk) < chunkLimit {
			break
		}
	}
	return
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

func (s *SlackClient) generateGroupDMName(ctx context.Context, members []string) (string, error) {
	ghostNames := make([]string, 0, len(members))
	for _, member := range members {
		if member == s.UserID {
			continue
		}
		ghost, err := s.UserLogin.Bridge.GetGhostByID(ctx, slackid.MakeUserID(s.TeamID, member))
		if err != nil {
			return "", err
		}
		ghost.UpdateInfoIfNecessary(ctx, s.UserLogin, bridgev2.RemoteEventUnknown)
		if ghost.Name != "" {
			ghostNames = append(ghostNames, ghost.Name)
		}
	}
	slices.SortFunc(ghostNames, compareStringFold)
	return strings.Join(ghostNames, ", "), nil
}

func (s *SlackClient) generateMemberList(ctx context.Context, info *slack.Channel, fetchList bool) (members bridgev2.ChatMemberList) {
	selfUserID := slackid.MakeUserID(s.TeamID, s.UserID)
	if !fetchList {
		return bridgev2.ChatMemberList{
			IsFull:           false,
			TotalMemberCount: info.NumMembers,
			MemberMap: map[networkid.UserID]bridgev2.ChatMember{
				selfUserID: {EventSender: s.makeEventSender(s.UserID)},
			},
		}
	}
	members.MemberMap = s.fetchChannelMembers(ctx, info.ID, s.Main.Config.ParticipantSyncCount)
	if _, hasSelf := members.MemberMap[selfUserID]; !hasSelf && info.IsMember {
		members.MemberMap[selfUserID] = bridgev2.ChatMember{EventSender: s.makeEventSender(s.UserID)}
	}
	members.IsFull = info.NumMembers > 0 && len(members.MemberMap) >= info.NumMembers
	return
}

func (s *SlackClient) wrapChatInfo(ctx context.Context, info *slack.Channel, isNew bool) (*bridgev2.ChatInfo, error) {
	var members bridgev2.ChatMemberList
	var avatar *bridgev2.Avatar
	var roomType database.RoomType
	var err error
	var extraUpdates func(ctx context.Context, portal *bridgev2.Portal) bool
	var userLocal *bridgev2.UserLocalPortalInfo
	switch {
	case info.IsMpIM:
		roomType = database.RoomTypeGroupDM
		members.IsFull = true
		members.MemberMap = make(map[networkid.UserID]bridgev2.ChatMember, len(info.Members))
		for _, member := range info.Members {
			evtSender := s.makeEventSender(member)
			members.MemberMap[evtSender.Sender] = bridgev2.ChatMember{EventSender: evtSender}
		}
		info.Name, err = s.generateGroupDMName(ctx, info.Members)
		if err != nil {
			return nil, err
		}
	case info.IsIM:
		roomType = database.RoomTypeDM
		members.IsFull = true
		selfMember := bridgev2.ChatMember{EventSender: s.makeEventSender(s.UserID)}
		otherMember := bridgev2.ChatMember{EventSender: s.makeEventSender(info.User)}
		members.OtherUserID = otherMember.Sender
		members.MemberMap = map[networkid.UserID]bridgev2.ChatMember{
			selfMember.Sender:  selfMember,
			otherMember.Sender: otherMember,
		}
		ghost, err := s.UserLogin.Bridge.GetGhostByID(ctx, slackid.MakeUserID(s.TeamID, info.User))
		if err != nil {
			return nil, err
		}
		ghost.UpdateInfoIfNecessary(ctx, s.UserLogin, bridgev2.RemoteEventUnknown)
		info.Name = ghost.Name
	case info.Name != "":
		members = s.generateMemberList(ctx, info, !s.Main.Config.ParticipantSyncOnlyOnCreate || isNew)
		if isNew && s.Main.Config.MuteChannelsByDefault {
			userLocal = &bridgev2.UserLocalPortalInfo{
				MutedUntil: &event.MutedForever,
			}
		}
	default:
		return nil, fmt.Errorf("unrecognized channel type")
	}
	if s.Main.Config.WorkspaceAvatarInRooms && (roomType == database.RoomTypeDefault || roomType == database.RoomTypeGroupDM) {
		avatar = &bridgev2.Avatar{
			ID:     s.TeamPortal.AvatarID,
			Remove: s.TeamPortal.AvatarID == "",
			MXC:    s.TeamPortal.AvatarMXC,
			Hash:   s.TeamPortal.AvatarHash,
		}
	}
	members.TotalMemberCount = info.NumMembers
	var name *string
	if roomType != database.RoomTypeDM || len(members.MemberMap) == 1 {
		name = ptr.Ptr(s.Main.Config.FormatChannelName(&ChannelNameParams{
			Channel:      info,
			Team:         &s.BootResp.Team,
			IsNoteToSelf: info.IsIM && info.User == s.UserID,
		}))
	}
	return &bridgev2.ChatInfo{
		Name:         name,
		Topic:        ptr.Ptr(info.Topic.Value),
		Avatar:       avatar,
		Members:      &members,
		Type:         &roomType,
		ParentID:     ptr.Ptr(slackid.MakeTeamPortalID(s.TeamID)),
		ExtraUpdates: extraUpdates,
		UserLocal:    userLocal,
		CanBackfill:  true,
	}, nil
}

func (s *SlackClient) fetchChatInfo(ctx context.Context, channelID string, isNew bool) (*bridgev2.ChatInfo, error) {
	info, err := s.fetchChatInfoWithCache(ctx, channelID)
	if err != nil {
		return nil, err
	} else if isNew && info.IsChannel && !info.IsMember {
		return nil, fmt.Errorf("request cancelled due to user not being in channel")
	}
	return s.wrapChatInfo(ctx, info, isNew)
}

func (s *SlackClient) getTeamInfo() *bridgev2.ChatInfo {
	name := s.Main.Config.FormatTeamName(&s.BootResp.Team)
	avatarURL, _ := s.BootResp.Team.Icon["image_230"].(string)
	if s.BootResp.Team.Icon["image_default"] == true {
		avatarURL = ""
	}
	selfEvtSender := s.makeEventSender(s.UserID)
	return &bridgev2.ChatInfo{
		Name:   &name,
		Topic:  nil,
		Avatar: makeAvatar(avatarURL, ""),
		Members: &bridgev2.ChatMemberList{
			IsFull:           false,
			TotalMemberCount: 0,
			MemberMap:        map[networkid.UserID]bridgev2.ChatMember{selfEvtSender.Sender: {EventSender: selfEvtSender}},
			PowerLevels:      &bridgev2.PowerLevelOverrides{EventsDefault: ptr.Ptr(100)},
		},
		Type: ptr.Ptr(database.RoomTypeSpace),
		ExtraUpdates: func(ctx context.Context, portal *bridgev2.Portal) (changed bool) {
			meta := portal.Metadata.(*slackid.PortalMetadata)
			if meta.TeamDomain != s.BootResp.Team.Domain {
				meta.TeamDomain = s.BootResp.Team.Domain
				changed = true
			}
			return
		},
	}
}

func (s *SlackClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	teamID, channelID := slackid.ParsePortalID(portal.ID)
	if teamID == "" {
		return nil, fmt.Errorf("invalid portal ID %q", portal.ID)
	} else if channelID == "" {
		return s.getTeamInfo(), nil
	} else {
		return s.fetchChatInfo(ctx, channelID, portal.MXID == "")
	}
}

func makeAvatar(avatarURL, slackAvatarHash string) *bridgev2.Avatar {
	avatarID := networkid.AvatarID(slackAvatarHash)
	if avatarID == "" {
		avatarID = networkid.AvatarID(avatarURL)
	}
	return &bridgev2.Avatar{
		ID: avatarID,
		Get: func(ctx context.Context) ([]byte, error) {
			return downloadPlainFile(ctx, avatarURL, "avatar")
		},
		Remove: avatarURL == "",
	}
}

func (s *SlackClient) wrapUserInfo(userID string, info *slack.User, botInfo *slack.Bot, ghost *bridgev2.Ghost) *bridgev2.UserInfo {
	var name *string
	var avatar *bridgev2.Avatar
	var extraUpdateAvatarID networkid.AvatarID
	isBot := userID == "USLACKBOT"
	if info != nil {
		name = ptr.Ptr(s.Main.Config.FormatDisplayname(&DisplaynameParams{
			User: info,
			Team: &s.BootResp.Team,
		}))
		avatarURL := info.Profile.ImageOriginal
		if avatarURL == "" && info.Profile.Image512 != "" {
			avatarURL = info.Profile.Image512
		}
		if avatarURL == "" && info.Profile.AvatarHash != "" {
			avatarURL = (&url.URL{
				Scheme: "https",
				Host:   "ca.slack-edge.com",
				Path:   fmt.Sprintf("/%s-%s-%s-512", s.TeamID, info.ID, info.Profile.AvatarHash),
			}).String()
		}
		avatar = makeAvatar(avatarURL, info.Profile.AvatarHash)
		// Optimization to avoid updating legacy avatars
		oldAvatarID := string(ghost.AvatarID)
		if strings.HasPrefix(oldAvatarID, "https://") && (oldAvatarID == avatarURL || strings.Contains(oldAvatarID, info.Profile.AvatarHash)) {
			extraUpdateAvatarID = avatar.ID
			avatar = nil
		}
		isBot = isBot || info.IsBot || info.IsAppUser
	} else if botInfo != nil {
		name = ptr.Ptr(s.Main.Config.FormatBotDisplayname(botInfo, &s.BootResp.Team))
		avatar = makeAvatar(botInfo.Icons.Image72, botInfo.Icons.Image72)
		isBot = true
	}
	return &bridgev2.UserInfo{
		Identifiers: []string{fmt.Sprintf("slack-internal:%s", userID)},
		Name:        name,
		Avatar:      avatar,
		IsBot:       &isBot,
		ExtraUpdates: func(ctx context.Context, ghost *bridgev2.Ghost) bool {
			meta := ghost.Metadata.(*slackid.GhostMetadata)
			meta.LastSync = jsontime.UnixNow()
			if info != nil {
				meta.SlackUpdatedTS = int64(info.Updated)
			} else if botInfo != nil {
				meta.SlackUpdatedTS = int64(botInfo.Updated)
			}
			if extraUpdateAvatarID != "" {
				ghost.AvatarID = extraUpdateAvatarID
			}
			return true
		},
	}
}

func (s *SlackClient) syncManyUsers(ctx context.Context, ghosts map[string]*bridgev2.Ghost) {
	params := slack.GetCachedUsersParameters{
		CheckInteraction:        true,
		IncludeProfileOnlyUsers: true,
		UpdatedIDs:              make(map[string]int64, len(ghosts)),
	}
	for _, ghost := range ghosts {
		meta := ghost.Metadata.(*slackid.GhostMetadata)
		_, userID := slackid.ParseUserID(ghost.ID)
		params.UpdatedIDs[userID] = meta.SlackUpdatedTS
	}
	zerolog.Ctx(ctx).Debug().Any("request_map", params.UpdatedIDs).Msg("Requesting user info")
	infos, err := s.Client.GetUsersCacheContext(ctx, s.TeamID, params)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get user info")
		return
	}
	zerolog.Ctx(ctx).Debug().Int("updated_user_count", len(infos)).Msg("Got user info")
	var wg sync.WaitGroup
	wg.Add(len(infos))
	for userID, info := range infos {
		ghost, ok := ghosts[userID]
		if !ok {
			wg.Done()
			zerolog.Ctx(ctx).Warn().Str("user_id", userID).Msg("Got unexpected user info")
			continue
		}
		go func() {
			defer wg.Done()
			ghost.UpdateInfo(ctx, s.wrapUserInfo(userID, info, nil, ghost))
		}()
	}
	wg.Wait()
	zerolog.Ctx(ctx).Debug().Msg("Finished syncing users")
}

func (s *SlackClient) fetchUserInfo(ctx context.Context, userID string, lastUpdated int64, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	if len(userID) == 0 {
		return nil, fmt.Errorf("empty user ID")
	}
	var info *slack.User
	var botInfo *slack.Bot
	var err error
	if userID[0] == 'B' {
		botInfo, err = s.Client.GetBotInfoContext(ctx, slack.GetBotInfoParameters{
			Bot: userID,
		})
	} else if s.IsRealUser {
		var infos map[string]*slack.User
		infos, err = s.Client.GetUsersCacheContext(ctx, s.TeamID, slack.GetCachedUsersParameters{
			CheckInteraction:        true,
			IncludeProfileOnlyUsers: true,
			UpdatedIDs: map[string]int64{
				userID: lastUpdated,
			},
		})
		if infos != nil {
			var ok bool
			info, ok = infos[userID]
			if !ok {
				return nil, nil
			}
		}
	} else {
		info, err = s.Client.GetUserInfoContext(ctx, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user info for %q: %w", userID, err)
	}
	return s.wrapUserInfo(userID, info, botInfo, ghost), nil
}

const MinGhostSyncInterval = 4 * time.Hour

func (s *SlackClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	if ghost.ID == "" {
		return nil, nil
	}
	meta := ghost.Metadata.(*slackid.GhostMetadata)
	if time.Since(meta.LastSync.Time) < MinGhostSyncInterval {
		return nil, nil
	}
	if s.IsRealUser && (ghost.Name != "" || time.Since(s.initialConnect) < 1*time.Minute) {
		s.userResyncQueue <- ghost
		return nil, nil
	}
	_, userID := slackid.ParseUserID(ghost.ID)
	return s.fetchUserInfo(ctx, userID, meta.SlackUpdatedTS, ghost)
}
