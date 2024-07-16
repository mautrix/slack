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
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-slack/pkg/msgconv"
	"go.mau.fi/mautrix-slack/pkg/slackid"
)

func init() {
	status.BridgeStateHumanErrors.Update(status.BridgeStateErrorMap{
		"slack-invalid-auth":           "Invalid credentials, please log in again",
		"slack-user-removed-from-team": "You were removed from the Slack workspace",
		"slack-id-mismatch":            "Unexpected internal error: got different user ID",
	})
}

func makeSlackClient(log *zerolog.Logger, token, cookieToken string) *slack.Client {
	options := []slack.Option{
		slack.OptionLog(slackgoZerolog{Logger: log.With().Str("component", "slackgo").Logger()}),
		slack.OptionDebug(log.GetLevel() == zerolog.TraceLevel),
	}
	if cookieToken != "" {
		options = append(options, slack.OptionCookie("d", cookieToken))
	}
	return slack.New(token, options...)
}

func (s *SlackConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	teamID, userID := slackid.ParseUserLoginID(login.ID)
	meta := login.Metadata.(*UserLoginMetadata)
	var sc *SlackClient
	if meta.Token == "" {
		sc = &SlackClient{Main: s, UserLogin: login, UserID: userID, TeamID: teamID}
	} else {
		client := makeSlackClient(&login.Log, meta.Token, meta.CookieToken)
		sc = &SlackClient{
			Main:      s,
			UserLogin: login,
			Client:    client,
			RTM:       client.NewRTM(),
			UserID:    userID,
			TeamID:    teamID,

			chatInfoCache: make(map[string]chatInfoCacheEntry),
			lastReadCache: make(map[string]string),
		}
	}
	teamPortalKey := networkid.PortalKey{ID: slackid.MakeTeamPortalID(teamID)}
	var err error
	sc.TeamPortal, err = s.br.UnlockedGetPortalByKey(ctx, teamPortalKey, false)
	if err != nil {
		return fmt.Errorf("failed to get team portal: %w", err)
	}
	login.Client = sc
	return nil
}

type chatInfoCacheEntry struct {
	ts   time.Time
	data *slack.Channel
}

type SlackClient struct {
	Main       *SlackConnector
	UserLogin  *bridgev2.UserLogin
	Client     *slack.Client
	RTM        *slack.RTM
	UserID     string
	TeamID     string
	BootResp   *slack.ClientBootResponse
	TeamPortal *bridgev2.Portal

	chatInfoCache     map[string]chatInfoCacheEntry
	chatInfoCacheLock sync.Mutex
	lastReadCache     map[string]string
	lastReadCacheLock sync.Mutex
}

var _ bridgev2.NetworkAPI = (*SlackClient)(nil)

var _ msgconv.SlackClientProvider = (*SlackClient)(nil)

func (s *SlackClient) GetClient() *slack.Client {
	return s.Client
}

func (s *SlackClient) Connect(ctx context.Context) error {
	bootResp, err := s.Client.ClientBootContext(ctx)
	if err != nil {
		if err.Error() == "user_removed_from_team" || err.Error() == "invalid_auth" {
			s.invalidateSession(ctx, status.BridgeState{
				StateEvent: status.StateBadCredentials,
				Error:      status.BridgeStateErrorCode(fmt.Sprintf("slack-%s", strings.ReplaceAll(err.Error(), "_", "-"))),
			})
		} else {
			s.UserLogin.BridgeState.Send(status.BridgeState{
				StateEvent: status.StateUnknownError,
				Error:      "slack-unknown-fetch-error",
				Message:    fmt.Sprintf("Unknown error from Slack: %s", err.Error()),
			})
		}
		return err
	}
	return s.connect(ctx, bootResp)
}

func (s *SlackClient) connect(ctx context.Context, bootResp *slack.ClientBootResponse) error {
	s.BootResp = bootResp
	err := s.syncTeamPortal(ctx)
	if err != nil {
		return err
	}
	go s.consumeEvents()
	go s.RTM.ManageConnection()
	go s.SyncEmojis(ctx)
	go s.SyncChannels(ctx)
	return nil
}

func (s *SlackClient) syncTeamPortal(ctx context.Context) error {
	info := s.getTeamInfo()
	if s.TeamPortal.MXID == "" {
		err := s.TeamPortal.CreateMatrixRoom(ctx, s.UserLogin, info)
		if err != nil {
			return err
		}
	} else {
		s.TeamPortal.UpdateInfo(ctx, info, s.UserLogin, nil, time.Time{})
	}
	return nil
}

func (s *SlackClient) setLastReadCache(channelID, ts string) {
	s.lastReadCacheLock.Lock()
	s.lastReadCache[channelID] = ts
	s.lastReadCacheLock.Unlock()
}

func (s *SlackClient) getLastReadCache(channelID string) string {
	s.lastReadCacheLock.Lock()
	defer s.lastReadCacheLock.Unlock()
	return s.lastReadCache[channelID]
}

func (s *SlackClient) SyncChannels(ctx context.Context) {
	log := zerolog.Ctx(ctx)
	clientCounts, err := s.Client.ClientCountsContext(ctx, &slack.ClientCountsParams{
		ThreadCountsByChannel: true,
		OrgWideAware:          true,
		IncludeFileChannels:   true,
	})
	if err != nil {
		log.Err(err).Msg("Failed to fetch client counts")
		return
	}
	latestMessageIDs := make(map[string]string, len(clientCounts.Channels)+len(clientCounts.MpIMs)+len(clientCounts.IMs))
	lastReadCache := make(map[string]string, len(clientCounts.Channels)+len(clientCounts.MpIMs)+len(clientCounts.IMs))
	for _, ch := range clientCounts.Channels {
		latestMessageIDs[ch.ID] = ch.Latest
		lastReadCache[ch.ID] = ch.LastRead
	}
	for _, ch := range clientCounts.MpIMs {
		latestMessageIDs[ch.ID] = ch.Latest
		lastReadCache[ch.ID] = ch.LastRead
	}
	for _, ch := range clientCounts.IMs {
		latestMessageIDs[ch.ID] = ch.Latest
		lastReadCache[ch.ID] = ch.LastRead
	}
	s.lastReadCacheLock.Lock()
	s.lastReadCache = lastReadCache
	s.lastReadCacheLock.Unlock()
	userPortals, err := s.UserLogin.Bridge.DB.UserPortal.GetAllForLogin(ctx, s.UserLogin.UserLogin)
	if err != nil {
		log.Err(err).Msg("Failed to fetch user portals")
		return
	}
	existingPortals := make(map[networkid.PortalKey]struct{}, len(userPortals))
	for _, up := range userPortals {
		existingPortals[up.Portal] = struct{}{}
	}
	var channels []*slack.Channel
	token := s.UserLogin.Metadata.(*UserLoginMetadata).Token
	if strings.HasPrefix(token, "xoxs") || s.Main.Config.Backfill.ConversationCount == -1 {
		for _, ch := range s.BootResp.Channels {
			ch.IsMember = true
			channels = append(channels, &ch.Channel)
		}
		for _, ch := range s.BootResp.IMs {
			ch.IsMember = true
			channels = append(channels, &ch.Channel)
		}
		log.Debug().Int("channel_count", len(channels)).Msg("Using channels from boot response for sync")
	} else {
		totalLimit := s.Main.Config.Backfill.ConversationCount
		var cursor string
		log.Debug().Int("total_limit", totalLimit).Msg("Fetching conversation list for sync")
		for totalLimit > 0 {
			reqLimit := totalLimit
			if totalLimit > 200 {
				reqLimit = 100
			}
			channelsChunk, nextCursor, err := s.Client.GetConversationsForUserContext(ctx, &slack.GetConversationsForUserParameters{
				Types:  []string{"public_channel", "private_channel", "mpim", "im"},
				Limit:  reqLimit,
				Cursor: cursor,
			})
			if err != nil {
				log.Err(err).Msg("Failed to fetch conversations for sync")
				return
			}
			log.Debug().Int("chunk_size", len(channelsChunk)).Msg("Fetched chunk of conversations")
			for _, channel := range channelsChunk {
				channels = append(channels, &channel)
			}
			if nextCursor == "" || len(channelsChunk) == 0 {
				break
			}
			totalLimit -= len(channelsChunk)
			cursor = nextCursor
		}
	}
	slices.SortFunc(channels, func(a, b *slack.Channel) int {
		return cmp.Compare(latestMessageIDs[a.ID], latestMessageIDs[b.ID])
	})
	for _, ch := range channels {
		portalKey := s.makePortalKey(ch)
		delete(existingPortals, portalKey)
		latestMessageID, hasCounts := latestMessageIDs[ch.ID]
		s.Main.br.QueueRemoteEvent(s.UserLogin, &SlackChatResync{
			SlackEventMeta: &SlackEventMeta{
				Type:         bridgev2.RemoteEventChatResync,
				PortalKey:    portalKey,
				CreatePortal: hasCounts || !(ch.IsIM || ch.IsMpIM),
			},
			Client:         s,
			LatestMessage:  latestMessageID,
			PreFetchedInfo: ch,
		})
	}
	for portalKey := range existingPortals {
		_, channelID := slackid.ParsePortalID(portalKey.ID)
		if channelID == "" {
			continue
		}
		latestMessageID, ok := latestMessageIDs[channelID]
		if !ok {
			// TODO delete portal if it's actually gone?
			continue
		}
		s.Main.br.QueueRemoteEvent(s.UserLogin, &SlackChatResync{
			SlackEventMeta: &SlackEventMeta{
				Type:      bridgev2.RemoteEventChatResync,
				PortalKey: portalKey,
			},
			Client:        s,
			LatestMessage: latestMessageID,
		})
	}
}

func (s *SlackClient) consumeEvents() {
	for evt := range s.RTM.IncomingEvents {
		s.HandleSlackEvent(evt.Data)
	}
}

func (s *SlackClient) Disconnect() {
	if rtm := s.RTM; rtm != nil {
		err := rtm.Disconnect()
		if err != nil {
			s.UserLogin.Log.Err(err).Msg("Failed to disconnect RTM")
		}
		// TODO stop consumeEvents?
		s.RTM = nil
	}
	s.Client = nil
}

func (s *SlackClient) IsLoggedIn() bool {
	return s.Client != nil
}

func (s *SlackClient) LogoutRemote(ctx context.Context) {
	_, err := s.Client.SendAuthSignoutContext(ctx)
	if err != nil {
		s.UserLogin.Log.Err(err).Msg("Failed to send sign out request to Slack")
	}
}

func (s *SlackClient) invalidateSession(ctx context.Context, state status.BridgeState) {
	meta := s.UserLogin.Metadata.(*UserLoginMetadata)
	meta.Token = ""
	meta.CookieToken = ""
	err := s.UserLogin.Save(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to save user login after invalidating session")
	}
	s.Disconnect()
	s.UserLogin.BridgeState.Send(state)
}

func (s *SlackClient) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	return slackid.UserIDToUserLoginID(userID) == s.UserLogin.ID
}
