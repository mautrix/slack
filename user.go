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
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridge/commands"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/pushrules"

	"go.mau.fi/mautrix-slack/auth"
	"go.mau.fi/mautrix-slack/database"
)

type User struct {
	*database.User

	bridge *SlackBridge
	zlog   zerolog.Logger

	teams map[string]*UserTeam

	spaceCreateLock      sync.Mutex
	autoDoublePuppetLock sync.Mutex
	PermissionLevel      bridgeconfig.PermissionLevel
	DoublePuppetIntent   *appservice.IntentAPI
	CommandState         *commands.CommandState
}

func (user *User) GetPermissionLevel() bridgeconfig.PermissionLevel {
	return user.PermissionLevel
}

func (user *User) GetManagementRoomID() id.RoomID {
	return user.ManagementRoom
}

func (user *User) GetMXID() id.UserID {
	return user.MXID
}

func (user *User) GetIDoublePuppet() bridge.DoublePuppet {
	return user
}

func (user *User) GetIGhost() bridge.Ghost {
	return nil
}

func (user *User) GetCommandState() *commands.CommandState {
	return user.CommandState
}

func (user *User) SetCommandState(state *commands.CommandState) {
	user.CommandState = state
}

var (
	_ bridge.User             = (*User)(nil)
	_ commands.CommandingUser = (*User)(nil)
)

func (br *SlackBridge) loadUser(ctx context.Context, dbUser *database.User, mxid *id.UserID) *User {
	if dbUser == nil {
		if mxid == nil {
			return nil
		}

		dbUser = br.DB.User.New()
		dbUser.MXID = *mxid
		err := dbUser.Insert(ctx)
		if err != nil {
			br.ZLog.Err(err).Stringer("user_id", dbUser.MXID).Msg("Failed to insert new user to database")
			return nil
		}
	}

	user := br.newUser(dbUser)

	br.usersByMXID[user.MXID] = user
	if user.ManagementRoom != "" {
		br.managementRooms[user.ManagementRoom] = user
	}
	user.bridge.unlockedGetAllUserTeamsForUser(user.MXID)

	return user
}

func (br *SlackBridge) newUser(dbUser *database.User) *User {
	user := &User{
		User:   dbUser,
		bridge: br,
		zlog:   br.ZLog.With().Stringer("user_id", dbUser.MXID).Logger(),
		teams:  make(map[string]*UserTeam),
	}
	user.PermissionLevel = br.Config.Bridge.Permissions.Get(user.MXID)
	return user
}

func (br *SlackBridge) GetUserByMXID(userID id.UserID) *User {
	return br.getUserByMXID(userID, false)
}

func (br *SlackBridge) GetCachedUserByMXID(userID id.UserID) *User {
	br.userAndTeamLock.Lock()
	defer br.userAndTeamLock.Unlock()
	return br.usersByMXID[userID]
}

func (br *SlackBridge) getUserByMXID(userID id.UserID, onlyIfExists bool) *User {
	_, isPuppet := br.ParsePuppetMXID(userID)
	if isPuppet || userID == br.Bot.UserID {
		return nil
	}
	br.userAndTeamLock.Lock()
	defer br.userAndTeamLock.Unlock()
	return br.unlockedGetUserByMXID(userID, onlyIfExists)
}

func (br *SlackBridge) unlockedGetUserByMXID(userID id.UserID, onlyIfExists bool) *User {
	user, ok := br.usersByMXID[userID]
	if !ok {
		ctx := context.TODO()
		dbUser, err := br.DB.User.GetByMXID(ctx, userID)
		if err != nil {
			br.ZLog.Err(err).Stringer("user_id", userID).Msg("Failed to get user by MXID from database")
			return nil
		}
		mxidPtr := &userID
		if onlyIfExists {
			mxidPtr = nil
		}
		return br.loadUser(ctx, dbUser, mxidPtr)
	}

	return user
}

func (br *SlackBridge) GetAllUsersWithAccessToken() []*User {
	br.userAndTeamLock.Lock()
	defer br.userAndTeamLock.Unlock()

	dbUsers, err := br.DB.User.GetAllWithAccessToken(context.TODO())
	if err != nil {
		br.ZLog.Err(err).Msg("Failed to get all users from database")
		return nil
	}
	users := make([]*User, len(dbUsers))

	for i, dbUser := range dbUsers {
		user, ok := br.usersByMXID[dbUser.MXID]
		if !ok {
			users[i] = br.loadUser(context.TODO(), dbUser, nil)
		} else {
			users[i] = user
		}
	}

	return users
}

func (br *SlackBridge) startUsers() {
	//if !br.historySyncLoopStarted {
	//	br.ZLog.Debug().Msg("Starting backfill loop")
	//	go br.handleHistorySyncsLoop()
	//}

	br.ZLog.Debug().Msg("Starting user teams")
	userTeams := br.GetAllUserTeamsWithToken()
	for _, ut := range userTeams {
		go ut.Connect()
	}
	if len(userTeams) == 0 {
		br.ZLog.Debug().Msg("No users to start, sending unconfigured state")
		br.SendGlobalBridgeState(status.BridgeState{StateEvent: status.StateUnconfigured}.Fill(nil))
	}

	br.ZLog.Debug().Msg("Starting custom puppets")
	for _, user := range br.GetAllUsersWithAccessToken() {
		go func(user *User) {
			user.zlog.Debug().Msg("Starting double puppet")
			if err := user.StartCustomMXID(true); err != nil {
				user.zlog.Err(err).Msg("Failed to start double puppet")
			}
		}(user)
	}
}

func (user *User) SetManagementRoom(roomID id.RoomID) {
	user.bridge.userAndTeamLock.Lock()
	defer user.bridge.userAndTeamLock.Unlock()
	ctx := context.TODO()

	existing, ok := user.bridge.managementRooms[roomID]
	if ok {
		existing.ManagementRoom = ""
		err := existing.Update(ctx)
		if err != nil {
			user.zlog.Err(err).Stringer("previous_user_mxid", existing.MXID).
				Msg("Failed to update previous user's management room")
		}
	}

	user.ManagementRoom = roomID
	user.bridge.managementRooms[user.ManagementRoom] = user
	err := user.Update(ctx)
	if err != nil {
		user.zlog.Err(err).Stringer("management_room_mxid", roomID).
			Msg("Failed to save user after updating management room")
	}
}

func (user *User) login(ctx context.Context, info *auth.Info) error {
	user.bridge.userAndTeamLock.Lock()
	existingTeam, ok := user.teams[info.TeamID]
	user.bridge.userAndTeamLock.Unlock()
	if ok && existingTeam.UserID != info.UserID {
		if existingTeam.Token == "" {
			existingTeam.Delete(ctx)
		} else {
			return fmt.Errorf("already logged into that team as %s/%s", existingTeam.Email, existingTeam.UserID)
		}
	}

	userTeam := user.bridge.GetUserTeamByID(database.UserTeamKey{
		TeamID: info.TeamID,
		UserID: info.UserID,
	}, user.MXID)
	if userTeam == nil {
		return fmt.Errorf("failed to get user team from database")
	} else if userTeam.UserMXID != user.MXID {
		return fmt.Errorf("%s is already logged into that account", userTeam.UserMXID)
	}
	userTeam.Email = info.UserEmail
	userTeam.Token = info.Token
	userTeam.CookieToken = info.CookieToken
	err := userTeam.Update(ctx)
	if err != nil {
		return fmt.Errorf("failed to save token: %w", err)
	}
	user.zlog.Info().
		Str("team_name", info.TeamName).
		Str("team_id", info.TeamID).
		Str("user_id", info.UserID).
		Str("user_email", info.UserEmail).
		Msg("Successfully logged in")
	go userTeam.Connect()
	return nil
}

func (user *User) LoginTeam(ctx context.Context, email, team, password string) error {
	info, err := auth.LoginPassword(email, team, password)
	if err != nil {
		return err
	}

	return user.login(ctx, info)
}

func (user *User) TokenLogin(ctx context.Context, token string, cookieToken string) (*auth.Info, error) {
	info, err := auth.LoginToken(token, cookieToken)
	if err != nil {
		return nil, err
	}

	return info, user.login(ctx, info)
}

func (user *User) IsLoggedIn() bool {
	user.bridge.userAndTeamLock.Lock()
	defer user.bridge.userAndTeamLock.Unlock()
	for _, ut := range user.teams {
		if ut.Client != nil {
			return true
		}
	}
	return false
}

func (user *User) GetTeam(teamID string) *UserTeam {
	user.bridge.userAndTeamLock.Lock()
	defer user.bridge.userAndTeamLock.Unlock()
	return user.teams[teamID]
}

func (user *User) ensureInvited(ctx context.Context, intent *appservice.IntentAPI, roomID id.RoomID, isDirect bool) (ok bool) {
	extraContent := make(map[string]interface{})
	if isDirect {
		extraContent["is_direct"] = true
	}
	customPuppet := user.DoublePuppetIntent
	if customPuppet != nil {
		extraContent["fi.mau.will_auto_accept"] = true
	}
	_, err := intent.InviteUser(ctx, roomID, &mautrix.ReqInviteUser{UserID: user.MXID}, extraContent)
	var httpErr mautrix.HTTPError
	if err != nil && errors.As(err, &httpErr) && httpErr.RespError != nil && strings.Contains(httpErr.RespError.Err, "is already in the room") {
		err = user.bridge.StateStore.SetMembership(ctx, roomID, user.MXID, event.MembershipJoin)
		if err != nil {
			user.zlog.Err(err).Stringer("room_id", roomID).Msg("Failed to update membership to join in state store after invite failed")
		}
		ok = true
		return
	} else if err != nil {
		user.zlog.Err(err).Stringer("room_id", roomID).Msg("Failed to invite user to room")
	} else {
		ok = true
	}

	if customPuppet != nil {
		err = customPuppet.EnsureJoined(ctx, roomID, appservice.EnsureJoinedParams{IgnoreCache: true})
		if err != nil {
			user.zlog.Err(err).Stringer("room_id", roomID).Msg("Failed to auto-join room")
			ok = false
		} else {
			ok = true
		}
	}
	return
}

func (user *User) updateChatMute(ctx context.Context, portal *Portal, muted bool) {
	if len(portal.MXID) == 0 || user.DoublePuppetIntent == nil {
		return
	}
	var err error
	if muted {
		user.zlog.Debug().Stringer("portal_mxid", portal.MXID).Msg("Muting portal")
		err = user.DoublePuppetIntent.PutPushRule(ctx, "global", pushrules.RoomRule, string(portal.MXID), &mautrix.ReqPutPushRule{
			Actions: []pushrules.PushActionType{pushrules.ActionDontNotify},
		})
	} else {
		user.zlog.Debug().Stringer("portal_mxid", portal.MXID).Msg("Unmuting portal")
		err = user.DoublePuppetIntent.DeletePushRule(ctx, "global", pushrules.RoomRule, string(portal.MXID))
	}
	if err != nil && !errors.Is(err, mautrix.MNotFound) {
		user.zlog.Err(err).Stringer("portal_mxid", portal.MXID).Msg("Failed to update push rule for portal")
	}
}

func (user *User) GetSpaceRoom(ctx context.Context) (id.RoomID, error) {
	user.spaceCreateLock.Lock()
	defer user.spaceCreateLock.Unlock()
	if len(user.SpaceRoom) > 0 {
		return user.SpaceRoom, nil
	}

	resp, err := user.bridge.Bot.CreateRoom(ctx, &mautrix.ReqCreateRoom{
		Visibility: "private",
		Name:       "Slack",
		Topic:      "Your Slack bridged chats",
		InitialState: []*event.Event{{
			Type: event.StateRoomAvatar,
			Content: event.Content{
				Parsed: &event.RoomAvatarEventContent{
					URL: user.bridge.Config.AppService.Bot.ParsedAvatar,
				},
			},
		}},
		CreationContent: map[string]interface{}{
			"type": event.RoomTypeSpace,
		},
		PowerLevelOverride: &event.PowerLevelsEventContent{
			Users: map[id.UserID]int{
				user.bridge.Bot.UserID: 9001,
				user.MXID:              50,
			},
		},
	})
	if err != nil {
		user.zlog.Err(err).Msg("Failed to auto-create space room")
		return "", fmt.Errorf("failed to create space: %w", err)
	}
	user.SpaceRoom = resp.RoomID
	err = user.Update(ctx)
	if err != nil {
		user.zlog.Err(err).Msg("Failed to save user after creating space room")
	}
	user.ensureInvited(ctx, user.bridge.Bot, user.SpaceRoom, false)
	return user.SpaceRoom, nil
}

func (user *User) SendBridgeAlert(message string, args ...any) {
	if user.ManagementRoom == "" {
		return
	}
	if len(args) > 0 {
		message = fmt.Sprintf(message, args...)
	}
	_, err := user.bridge.Bot.SendText(context.TODO(), user.ManagementRoom, message)
	if err != nil {
		user.zlog.Err(err).Msg("Failed to send bridge alert")
	}
}
