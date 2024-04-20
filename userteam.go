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
	"cmp"
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/rs/zerolog"
	"golang.org/x/exp/slices"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/slack-go/slack"

	"go.mau.fi/mautrix-slack/database"
)

type UserTeam struct {
	*database.UserTeam
	bridge *SlackBridge
	User   *User
	Team   *Team
	Log    zerolog.Logger

	BridgeState *bridge.BridgeStateQueue

	RTM    *slack.RTM
	Client *slack.Client

	stopEventLoop atomic.Pointer[context.CancelFunc]
}

func (ut *UserTeam) GetMXID() id.UserID {
	return ut.UserMXID
}

func (ut *UserTeam) GetRemoteID() string {
	return ut.TeamID
}

func (ut *UserTeam) GetRemoteName() string {
	// TODO why is this the team name?
	return ut.Team.Name
}

func (br *SlackBridge) loadUserTeam(ctx context.Context, dbUserTeam *database.UserTeam, key *database.UserTeamMXIDKey) *UserTeam {
	var team *Team
	var user *User
	if dbUserTeam == nil {
		if key == nil {
			return nil
		}
		// Get team and user beforehand to ensure they exist in the database
		team = br.unlockedGetTeamByID(key.TeamID, false)
		if team == nil {
			br.ZLog.Warn().Str("team_id", key.TeamID).Msg("Failed to get team by ID before inserting user team")
		}
		user = br.unlockedGetUserByMXID(key.UserMXID, false)
		dbUserTeam = br.DB.UserTeam.New()
		dbUserTeam.UserTeamMXIDKey = *key
		err := dbUserTeam.Insert(ctx)
		if err != nil {
			br.ZLog.Err(err).Object("key", key).Msg("Failed to insert new user team")
			return nil
		}
	}
	userTeam := &UserTeam{
		UserTeam: dbUserTeam,
		bridge:   br,
		Log:      br.ZLog.With().Object("user_team_key", dbUserTeam.UserTeamMXIDKey).Logger(),
	}
	userTeam.BridgeState = br.NewBridgeStateQueue(userTeam)

	existingUT, alreadyExists := br.userTeamsByID[userTeam.UserTeamKey]
	if alreadyExists {
		panic(fmt.Errorf("%v (%s/%s) already exists in bridge map", userTeam.UserTeamKey, userTeam.UserMXID, existingUT.UserMXID))
	}
	br.userTeamsByID[userTeam.UserTeamKey] = userTeam

	if team == nil || user == nil {
		team = br.unlockedGetTeamByID(dbUserTeam.TeamID, false)
		user = br.unlockedGetUserByMXID(dbUserTeam.UserMXID, false)
	}
	userTeam.Team = team
	userTeam.User = user

	existingUT, alreadyExists = userTeam.User.teams[userTeam.TeamID]
	if alreadyExists {
		panic(fmt.Errorf("%s (%s/%s) already exists in %s's user team map", userTeam.TeamID, userTeam.UserID, existingUT.UserID, userTeam.UserMXID))
	}
	userTeam.User.teams[userTeam.TeamID] = userTeam

	return userTeam
}

func (br *SlackBridge) loadUserTeams(dbUserTeams []*database.UserTeam, err error) []*UserTeam {
	if err != nil {
		br.ZLog.Err(err).Msg("Failed to load user teams")
		return nil
	}
	userTeams := make([]*UserTeam, len(dbUserTeams))
	for i, dbUserTeam := range dbUserTeams {
		cached, ok := br.userTeamsByID[dbUserTeam.UserTeamKey]
		if ok {
			userTeams[i] = cached
		} else {
			userTeams[i] = br.loadUserTeam(context.TODO(), dbUserTeam, nil)
		}
	}
	return userTeams
}

func (br *SlackBridge) GetUserTeamByID(key database.UserTeamKey, userMXID id.UserID) *UserTeam {
	br.userAndTeamLock.Lock()
	defer br.userAndTeamLock.Unlock()
	return br.unlockedGetUserTeamByID(key, userMXID)
}

func (br *SlackBridge) GetExistingUserTeamByID(key database.UserTeamKey) *UserTeam {
	br.userAndTeamLock.Lock()
	defer br.userAndTeamLock.Unlock()
	return br.unlockedGetUserTeamByID(key, "")
}

func (br *SlackBridge) GetCachedUserTeamByID(key database.UserTeamKey) *UserTeam {
	br.userAndTeamLock.Lock()
	defer br.userAndTeamLock.Unlock()
	return br.userTeamsByID[key]
}

func (br *SlackBridge) unlockedGetUserTeamByID(key database.UserTeamKey, userMXID id.UserID) *UserTeam {
	userTeam, ok := br.userTeamsByID[key]
	if !ok {
		ctx := context.TODO()
		dbUserTeam, err := br.DB.UserTeam.GetByID(ctx, key)
		if err != nil {
			br.ZLog.Err(err).Any("key", key).Msg("Failed to get user team by ID")
			return nil
		}
		var newKey *database.UserTeamMXIDKey
		if userMXID != "" {
			newKey = &database.UserTeamMXIDKey{UserTeamKey: key, UserMXID: userMXID}
		}
		return br.loadUserTeam(ctx, dbUserTeam, newKey)
	}
	return userTeam
}

func (br *SlackBridge) GetAllUserTeamsWithToken() []*UserTeam {
	br.userAndTeamLock.Lock()
	defer br.userAndTeamLock.Unlock()
	return br.loadUserTeams(br.DB.UserTeam.GetAllWithToken(context.TODO()))
}

func (br *SlackBridge) GetAllUserTeamsForUser(userID id.UserID) []*UserTeam {
	br.userAndTeamLock.Lock()
	defer br.userAndTeamLock.Unlock()
	return br.unlockedGetAllUserTeamsForUser(userID)
}

func (br *SlackBridge) unlockedGetAllUserTeamsForUser(userID id.UserID) []*UserTeam {
	return br.loadUserTeams(br.DB.UserTeam.GetAllForUser(context.TODO(), userID))
}

type slackgoZerolog struct {
	zerolog.Logger
}

func (l slackgoZerolog) Output(i int, s string) error {
	level := zerolog.DebugLevel
	if strings.HasPrefix(s, "Sending PING ") {
		level = zerolog.TraceLevel
	}
	l.WithLevel(level).Msg(strings.TrimSpace(s))
	return nil
}

func (ut *UserTeam) Connect() {
	ut.User.tryAutomaticDoublePuppeting()
	evt := ut.Log.Trace()
	hasTraceLog := evt.Enabled()
	evt.Discard()
	slackOptions := []slack.Option{
		slack.OptionLog(slackgoZerolog{ut.Log.With().Str("component", "slackgo").Logger()}),
		slack.OptionDebug(hasTraceLog),
	}
	if ut.CookieToken != "" {
		slackOptions = append(slackOptions, slack.OptionCookie("d", ut.CookieToken))
	}
	ut.Client = slack.New(ut.Token, slackOptions...)

	ctx := context.TODO()
	teamInfo, err := ut.Client.GetTeamInfoContext(ctx)
	if err != nil {
		ut.Log.Err(err).Msg("Failed to connect to Slack team")
		// TODO use proper error comparisons
		if err.Error() == "user_removed_from_team" {
			go ut.Logout(ctx, status.BridgeState{StateEvent: status.StateBadCredentials, Error: "slack-user-removed-from-team"})
		} else if err.Error() == "invalid_auth" {
			go ut.Logout(ctx, status.BridgeState{StateEvent: status.StateBadCredentials, Error: "slack-invalid-auth"})
		} else {
			ut.BridgeState.Send(status.BridgeState{StateEvent: status.StateUnknownError, Error: "slack-get-info-failed"})
		}
		return
	}

	ut.RTM = ut.Client.NewRTM()

	go ut.slackEventLoop()
	go ut.RTM.ManageConnection()
	go ut.Sync(ctx, teamInfo)
	return
}

func (ut *UserTeam) Sync(ctx context.Context, meta *slack.TeamInfo) {
	if meta == nil {
		var err error
		meta, err = ut.Client.GetTeamInfoContext(ctx)
		if err != nil {
			ut.Log.Err(err).Msg("Failed to get team info from Slack for sync")
			return
		}
	}
	ut.Team.UpdateInfo(ctx, meta)
	if ut.Team.MXID == "" {
		err := ut.Team.CreateMatrixRoom(ctx)
		if err != nil {
			ut.Log.Err(err).Msg("Failed to create Matrix space for team")
			return
		}
	}
	ut.AddToSpace(ctx)
	ut.User.ensureInvited(ctx, ut.bridge.Bot, ut.Team.MXID, false)
	ut.syncPortals(ctx)
	ut.SyncEmojis(ctx)
}

func (ut *UserTeam) syncPortals(ctx context.Context) {
	serverInfo := make(map[string]*slack.Channel)
	if !strings.HasPrefix(ut.Token, "xoxs") {
		totalLimit := ut.bridge.Config.Bridge.Backfill.ConversationsCount
		var cursor string
		for totalLimit > 0 {
			reqLimit := totalLimit
			if totalLimit > 200 {
				reqLimit = 100
			}
			channelsChunk, nextCursor, err := ut.Client.GetConversationsForUserContext(ctx, &slack.GetConversationsForUserParameters{
				Types:  []string{"public_channel", "private_channel", "mpim", "im"},
				Limit:  reqLimit,
				Cursor: cursor,
			})
			if err != nil {
				ut.Log.Err(err).Msg("Failed to fetch conversations for sync")
				return
			}
			for _, channel := range channelsChunk {
				// Skip non-"open" DMs
				if channel.IsIM && (channel.Latest == nil || channel.Latest.SubType == "") {
					continue
				}
				// TODO remove this after switching to Go 1.22 with loop var fix
				channelCopy := channel
				serverInfo[channel.ID] = &channelCopy
			}
			if nextCursor == "" || len(channelsChunk) == 0 {
				break
			}
			totalLimit -= len(channelsChunk)
			cursor = nextCursor
		}
	}
	existingPortals := ut.bridge.GetAllPortalsForUserTeam(ut.UserTeamMXIDKey)
	for _, portal := range existingPortals {
		if portal.MXID != "" {
			// Don't actually use the fetched metadata, it doesn't have enough info
			portal.UpdateInfo(ctx, ut, nil, true)
			delete(serverInfo, portal.ChannelID)
		}
	}
	remainingChannels := make([]*slack.Channel, len(serverInfo))
	i := 0
	for _, channel := range serverInfo {
		remainingChannels[i] = channel
		i++
	}
	slices.SortFunc(remainingChannels, func(a, b *slack.Channel) int {
		return cmp.Compare(a.LastRead, b.LastRead)
	})
	for _, ch := range remainingChannels {
		portal := ut.Team.GetPortalByID(ch.ID)
		if portal == nil {
			continue
		}
		err := portal.CreateMatrixRoom(ctx, ut, nil)
		if err != nil {
			ut.Log.Err(err).Str("channel_id", ch.ID).Msg("Failed to create Matrix room for channel")
		}
	}
}

func (ut *UserTeam) AddToSpace(ctx context.Context) {
	if ut.Team.MXID == "" {
		if ut.InSpace {
			ut.InSpace = false
			err := ut.Update(ctx)
			if err != nil {
				ut.Log.Err(err).Msg("Failed to save user team info after marking not in space")
			}
		}
		return
	} else if ut.InSpace {
		return
	}
	spaceRoom, err := ut.User.GetSpaceRoom(ctx)
	if err != nil {
		ut.Log.Err(err).Msg("Failed to get user's space room to add team space")
		return
	}
	_, err = ut.bridge.Bot.SendStateEvent(ctx, spaceRoom, event.StateSpaceChild, ut.Team.MXID.String(), &event.SpaceChildEventContent{
		Via: []string{ut.bridge.AS.HomeserverDomain},
	})
	if err != nil {
		ut.InSpace = false
		ut.Log.Err(err).Msg("Failed to add team space to user's personal space")
	} else {
		ut.InSpace = true
		ut.Log.Info().Msg("Added team space to user's personal space")
	}
	err = ut.Update(ctx)
	if err != nil {
		ut.Log.Err(err).Msg("Failed to save user team info after adding to space")
	}
}

func (ut *UserTeam) slackEventLoop() {
	log := ut.Log.With().Str("action", "slack event loop").Logger()
	ctx := log.WithContext(context.TODO())
	ctx, cancel := context.WithCancel(log.WithContext(context.TODO()))
	defer cancel()
	if prevCancel := ut.stopEventLoop.Swap(&cancel); prevCancel != nil {
		(*prevCancel)()
	}
	ctxDone := ctx.Done()
	log.Info().Msg("Event loop starting")
	for {
		select {
		case evt := <-ut.RTM.IncomingEvents:
			log.Trace().Type("event_type", evt).Any("event_data", evt).Msg("Received raw Slack event")
			if evt.Type == "" && evt.Data == nil {
				log.Warn().Msg("Event channel closed")
				ut.BridgeState.Send(status.BridgeState{StateEvent: status.StateUnknownError, Error: "slack-rtm-channel-closed"})
				return
			}
			ut.handleSlackEvent(ctx, evt.Data)
		case <-ctxDone:
			log.Info().Msg("Event loop stopping")
			return
		}
	}
}

func init() {
	status.BridgeStateHumanErrors.Update(map[status.BridgeStateErrorCode]string{
		"slack-invalid-auth":           "Invalid authentication token",
		"slack-user-removed-from-team": "Removed from Slack workspace",
	})
}

func (ut *UserTeam) handleSlackEvent(ctx context.Context, rawEvt any) {
	switch evt := rawEvt.(type) {
	case *slack.ConnectingEvent:
		ut.Log.Debug().
			Int("attempt_num", evt.Attempt).
			Int("connection_count", evt.ConnectionCount).
			Msg("Connecting to RTM")
		ut.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})
	case *slack.ConnectedEvent:
		if evt.Info.User.ID != ut.UserID || evt.Info.Team.ID != ut.TeamID {
			ut.Log.Error().
				Str("evt_user_id", evt.Info.User.ID).
				Str("evt_team_id", evt.Info.Team.ID).
				Msg("User/team ID mismatch in connected event")
			ut.Logout(context.WithoutCancel(ctx), status.BridgeState{
				StateEvent: status.StateUnknownError,
				Error:      "slack-id-mismatch",
			})
			return
		}
		ut.Log.Info().Msg("Connected to RTM")

		//if ut.bridge.Config.Bridge.Backfill.Enable {
		//	ut.BridgeState.Send(status.BridgeState{StateEvent: status.StateBackfilling})
		//
		//	portals := ut.bridge.GetAllPortalsForUserTeam(ut.UserTeamMXIDKey)
		//	for _, portal := range portals {
		//		err := portal.ForwardBackfill()
		//		if err != nil {
		//			ut.Log.Err(err).
		//				Str("channel_id", portal.ChannelID).
		//				Stringer("channel_mxid", portal.MXID).
		//				Msg("Failed to forward backfill channel")
		//		}
		//	}
		//}
		ut.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
	case *slack.HelloEvent:
		// Ignored for now
	case *slack.InvalidAuthEvent:
		ut.Logout(context.TODO(), status.BridgeState{StateEvent: status.StateBadCredentials, Error: "slack-invalid-auth"})
		return
	case *slack.MessageEvent:
		ut.pushPortalEvent(evt.Channel, evt)
	case *slack.ReactionAddedEvent:
		ut.pushPortalEvent(evt.Item.Channel, evt)
	case *slack.ReactionRemovedEvent:
		ut.pushPortalEvent(evt.Item.Channel, evt)
	case *slack.UserTypingEvent:
		ut.pushPortalEvent(evt.Channel, evt)
	case *slack.ChannelMarkedEvent:
		ut.pushPortalEvent(evt.Channel, evt)
	case *slack.IMMarkedEvent:
		ut.pushPortalEvent(evt.Channel, evt)
	case *slack.GroupMarkedEvent:
		ut.pushPortalEvent(evt.Channel, evt)
	case *slack.ChannelJoinedEvent:
		ut.pushPortalEvent(evt.Channel.ID, evt)
	case *slack.ChannelLeftEvent:
		ut.pushPortalEvent(evt.Channel, evt)
	case *slack.GroupJoinedEvent:
		ut.pushPortalEvent(evt.Channel.ID, evt)
	case *slack.GroupLeftEvent:
		ut.pushPortalEvent(evt.Channel, evt)
	case *slack.MemberJoinedChannelEvent:
		ut.pushPortalEvent(evt.Channel, evt)
	case *slack.MemberLeftChannelEvent:
		ut.pushPortalEvent(evt.Channel, evt)
	case *slack.ChannelUpdateEvent:
		ut.pushPortalEvent(evt.Channel, evt)
	case *slack.EmojiChangedEvent:
		go ut.handleEmojiChange(ctx, evt)
	case *slack.RTMError:
		ut.Log.Err(evt).Msg("Got RTM error")
		ut.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateUnknownError,
			Error:      status.BridgeStateErrorCode(fmt.Sprintf("slack-rtm-error-%d", evt.Code)),
			Message:    fmt.Sprintf("%d: %s", evt.Code, evt.Msg),
		})
	case *slack.FileSharedEvent, *slack.FilePublicEvent, *slack.FilePrivateEvent, *slack.FileCreatedEvent, *slack.FileChangeEvent, *slack.FileDeletedEvent, *slack.DesktopNotificationEvent, *slack.ReconnectUrlEvent, *slack.LatencyReport:
		// ignored intentionally, these are duplicates or do not contain useful information
	default:
		ut.Log.Warn().Any("event_data", evt).Msg("Unrecognized Slack event type")
	}
}

func (ut *UserTeam) pushPortalEvent(channelID string, evt any) {
	portal := ut.Team.GetPortalByID(channelID)
	if portal == nil {
		ut.Log.Warn().
			Str("channel_id", channelID).
			Type("event_type", evt).
			Msg("Portal is nil for incoming event")
		return
	}
	select {
	case portal.slackMessages <- portalSlackMessage{evt: evt, userTeam: ut}:
	default:
		ut.Log.Warn().
			Str("channel_id", channelID).
			Type("event_type", evt).
			Msg("Portal message channel is full")
	}
}

func (ut *UserTeam) stopRTM() {
	if stopFunc := ut.stopEventLoop.Swap(nil); stopFunc != nil {
		(*stopFunc)()
	}

	if ut.RTM != nil {
		go ut.RTM.Disconnect()
	}
}

func (ut *UserTeam) Disconnect() {
	ut.stopRTM()
	ut.Client = nil
	ut.RTM = nil
}

func (ut *UserTeam) Logout(ctx context.Context, state status.BridgeState) {
	ut.stopRTM()

	if state.StateEvent == status.StateLoggedOut {
		if ut.Client != nil {
			_, err := ut.Client.SendAuthSignoutContext(ctx)
			if err != nil {
				ut.Log.Warn().Err(err).Msg("Failed to send auth signout request to Slack")
			}
		}
	} else {
		go ut.User.SendBridgeAlert("Invalid credentials for %s (%s.slack.com / %s)", ut.Team.Name, ut.Team.Domain, ut.Team.ID)
	}

	ut.CookieToken = ""
	ut.Token = ""
	ut.Client = nil
	ut.RTM = nil
	ut.BridgeState.Send(state)
	err := ut.Update(ctx)
	if err != nil {
		ut.Log.Err(err).Msg("Failed to save user team after deleting token")
	}
	if ut.bridge.Config.Bridge.KickOnLogout {
		ut.Log.Debug().Msg("Kicking user from rooms and deleting user team from database")
		ut.RemoveFromRooms(ctx)
		ut.Delete(ctx)
	} else {
		ut.Log.Debug().Msg("Not kicking user from rooms")
	}
}

func (ut *UserTeam) RemoveFromRooms(ctx context.Context) {
	portals := ut.bridge.GetAllPortalsForUserTeam(ut.UserTeamMXIDKey)
	dpi := ut.User.DoublePuppetIntent
	var err error
	for _, portal := range portals {
		if dpi != nil {
			_, err = dpi.LeaveRoom(ctx, portal.MXID, &mautrix.ReqLeave{Reason: "Logged out from bridge"})
		} else {
			_, err = portal.MainIntent().KickUser(ctx, portal.MXID, &mautrix.ReqKickUser{
				Reason: "Logged out from bridge",
				UserID: ut.UserMXID,
			})
		}
		if err != nil {
			ut.Log.Err(err).
				Stringer("portal_mxid", portal.MXID).
				Stringer("portal_key", portal.PortalKey).
				Msg("Failed to remove user from room")
		}
		portal.CleanupIfEmpty(ctx)
	}
}

func (ut *UserTeam) Delete(ctx context.Context) {
	ut.bridge.userAndTeamLock.Lock()
	defer ut.bridge.userAndTeamLock.Unlock()
	delete(ut.User.teams, ut.TeamID)
	delete(ut.bridge.userTeamsByID, ut.UserTeamKey)
	err := ut.UserTeam.Delete(ctx)
	if err != nil {
		ut.Log.Err(err).Msg("Failed to delete user team")
	}
}
