// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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
	"errors"
	"sync"

	log "maunium.net/go/maulogger/v2"

	"github.com/slack-go/slack"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/id"

	"github.com/mautrix/slack/auth"
	"github.com/mautrix/slack/database"
)

var (
	ErrNotConnected = errors.New("not connected")
	ErrNotLoggedIn  = errors.New("not logged in")
)

type User struct {
	*database.User

	sync.Mutex

	bridge *SlackBridge
	log    log.Logger

	PermissionLevel bridgeconfig.PermissionLevel
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

func (user *User) GetCommandState() map[string]interface{} {
	return nil
}

func (user *User) GetIDoublePuppet() bridge.DoublePuppet {
	p := user.bridge.GetPuppetByCustomMXID(user.MXID)
	if p == nil || p.CustomIntent() == nil {
		return nil
	}
	return p
}

func (user *User) GetIGhost() bridge.Ghost {
	// if user.ID == "" {
	// 	return nil
	// }
	// p := user.bridge.GetPuppetByID(user.ID)
	// if p == nil {
	// 	return nil
	// }
	// return p
	return nil
}

var _ bridge.User = (*User)(nil)

func (br *SlackBridge) loadUser(dbUser *database.User, mxid *id.UserID) *User {
	// If we weren't passed in a user we attempt to create one if we were given
	// a matrix id.
	if dbUser == nil {
		if mxid == nil {
			return nil
		}

		dbUser = br.DB.User.New()
		dbUser.MXID = *mxid
		dbUser.Insert()
	}

	user := br.NewUser(dbUser)

	// We assume the usersLock was acquired by our caller.
	br.usersByMXID[user.MXID] = user

	if user.ManagementRoom != "" {
		// Lock the management rooms for our update
		br.managementRoomsLock.Lock()
		br.managementRooms[user.ManagementRoom] = user
		br.managementRoomsLock.Unlock()
	}

	return user
}

func (br *SlackBridge) GetUserByMXID(userID id.UserID) *User {
	// TODO: check if puppet

	br.usersLock.Lock()
	defer br.usersLock.Unlock()

	user, ok := br.usersByMXID[userID]
	if !ok {
		return br.loadUser(br.DB.User.GetByMXID(userID), &userID)
	}

	return user
}

// func (br *SlackBridge) GetUserByID(user_id, domain_id string) *User {
// 	br.usersLock.Lock()
// 	defer br.usersLock.Unlock()

// 	user, ok := br.usersByID[id]
// 	if !ok {
// 		return br.loadUser(br.DB.User.GetBySlackID(user_id, domain_id), nil)
// 	}

// 	return user
// }

func (br *SlackBridge) NewUser(dbUser *database.User) *User {
	user := &User{
		User:   dbUser,
		bridge: br,
		log:    br.Log.Sub("User").Sub(string(dbUser.MXID)),
	}

	user.PermissionLevel = br.Config.Bridge.Permissions.Get(user.MXID)

	return user
}

func (br *SlackBridge) getAllUsers() []*User {
	br.usersLock.Lock()
	defer br.usersLock.Unlock()

	dbUsers := br.DB.User.GetAll()
	users := make([]*User, len(dbUsers))

	for idx, dbUser := range dbUsers {
		user, ok := br.usersByMXID[dbUser.MXID]
		if !ok {
			user = br.loadUser(dbUser, nil)
		}
		users[idx] = user
	}

	return users
}

func (br *SlackBridge) startUsers() {
	br.Log.Debugln("Starting users")

	for _, user := range br.getAllUsers() {
		go user.Connect()
	}

	br.Log.Debugln("Starting custom puppets")
	for _, customPuppet := range br.GetAllPuppetsWithCustomMXID() {
		go func(puppet *Puppet) {
			br.Log.Debugln("Starting custom puppet", puppet.CustomMXID)

			if err := puppet.StartCustomMXID(true); err != nil {
				puppet.log.Errorln("Failed to start custom puppet:", err)
			}
		}(customPuppet)
	}
}

func (user *User) SetManagementRoom(roomID id.RoomID) {
	user.bridge.managementRoomsLock.Lock()
	defer user.bridge.managementRoomsLock.Unlock()

	existing, ok := user.bridge.managementRooms[roomID]
	if ok {
		// If there's a user already assigned to this management room, clear it
		// out.
		// I think this is due a name change or something? I dunno, leaving it
		// for now.
		existing.ManagementRoom = ""
		existing.Update()
	}

	user.ManagementRoom = roomID
	user.bridge.managementRooms[user.ManagementRoom] = user
	user.Update()
}

func (user *User) tryAutomaticDoublePuppeting() {
	user.Lock()
	defer user.Unlock()

	if !user.bridge.Config.CanAutoDoublePuppet(user.MXID) {
		return
	}

	user.log.Debugln("Checking if double puppeting needs to be enabled")

	// puppet := user.bridge.GetPuppetByID(user.ID)
	// if puppet.CustomMXID != "" {
	// 	user.log.Debugln("User already has double-puppeting enabled")

	// 	return
	// }

	// accessToken, err := puppet.loginWithSharedSecret(user.MXID)
	// if err != nil {
	// 	user.log.Warnln("Failed to login with shared secret:", err)

	// 	return
	// }

	// err = puppet.SwitchCustomMXID(accessToken, user.MXID)
	// if err != nil {
	// 	puppet.log.Warnln("Failed to switch to auto-logined custom puppet:", err)

	// 	return
	// }

	// user.log.Infoln("Successfully automatically enabled custom puppet")
}

func (user *User) syncChatDoublePuppetDetails(portal *Portal, justCreated bool) {
	doublePuppet := portal.bridge.GetPuppetByCustomMXID(user.MXID)
	if doublePuppet == nil {
		return
	}

	if doublePuppet == nil || doublePuppet.CustomIntent() == nil || portal.MXID == "" {
		return
	}

	// TODO sync mute status
}

func (user *User) LoginTeam(email, team, password string) error {
	info, err := auth.Login(user.log, email, team, password)
	if err != nil {
		return err
	}

	userTeam := user.bridge.DB.UserTeam.New()

	userTeam.Key.MXID = user.MXID
	userTeam.Key.SlackID = info.UserID
	userTeam.Key.TeamID = info.TeamID
	userTeam.SlackEmail = info.UserEmail
	userTeam.TeamName = info.TeamName
	userTeam.Token = info.Token

	// We minimize the time we hold the lock because SyncTeams also needs the
	// lock.
	user.TeamsLock.Lock()
	user.Teams[userTeam.Key] = userTeam
	user.TeamsLock.Unlock()

	user.User.SyncTeams()

	user.log.Debugln("logged into %s successfully", info.TeamName)

	return user.connectTeam(userTeam)
}

// func (user *User) TokenLogin(token string) error {
// 	user.Token = token
// 	user.Update()
// 	return user.Connect()
// }

func (user *User) IsLoggedIn() bool {
	return len(user.GetLoggedInTeams()) > 0
}

func (user *User) IsLoggedInTeam(email, team string) bool {
	if user.TeamLoggedIn(email, team) {
		user.log.Errorf("%s is already logged into team %s with %s", user.MXID, team, email)

		return true
	}

	return false
}

func (user *User) LogoutTeam(email, team string) error {
	userTeam := user.bridge.DB.UserTeam.GetBySlackTeam(user.MXID, email, team)
	if userTeam == nil {
		return ErrNotLoggedIn
	}

	// If this is the last slack team, also disconnect the double puppet.
	if len(user.Teams) == 1 {
		// puppet := user.bridge.GetPuppetByID(user.ID)
		// var puppet Puppet
		// if puppet.CustomMXID != "" {
		// 	err := puppet.SwitchCustomMXID("", "")
		// 	if err != nil {
		// 		user.log.Warnln("Failed to logout-matrix while logging out of Slack:", err)
		// 	}
		// }
	}

	if userTeam.RTM != nil {
		if err := userTeam.RTM.Disconnect(); err != nil {
			return err
		}
	}

	userTeam.Client = nil

	user.TeamsLock.Lock()
	delete(user.Teams, userTeam.Key)
	user.TeamsLock.Unlock()

	userTeam.Token = ""

	user.Update()

	return nil
}

func (user *User) slackMessageHandler(userTeam *database.UserTeam) {
	for msg := range userTeam.RTM.IncomingEvents {
		switch event := msg.Data.(type) {
		case *slack.ConnectingEvent:
			user.log.Debugfln("connecting: attempt %d", event.Attempt)
		case *slack.ConnectedEvent:
			// Update all of our values according to what the server has for us.
			userTeam.Key.SlackID = event.Info.User.ID
			userTeam.Key.TeamID = event.Info.Team.ID
			userTeam.TeamName = event.Info.Team.Name

			userTeam.Upsert()

			user.log.Infofln("connected to team %s as %s", userTeam.TeamName, userTeam.SlackEmail)
		case *slack.HelloEvent:
			// Ignored for now
		case *slack.InvalidAuthEvent:
			user.log.Errorln("invalid authentication token")

			user.LogoutTeam(userTeam.SlackEmail, userTeam.TeamName)

			// TODO: Should drop a message in the management room

			return
		case *slack.LatencyReport:
			user.log.Debugln("latency report:", event.Value)
		case *slack.MessageEvent:
			key := database.NewPortalKey(userTeam.Key.TeamID, userTeam.Key.SlackID, event.Channel)
			portal := user.bridge.GetPortalByID(key)

			if portal != nil {
				portal.HandleSlackMessage(user, userTeam, event)
			}
		case *slack.RTMError:
			user.log.Errorln("rtm error:", event.Error())
		default:
			user.log.Warnln("uknown message", msg)
		}
	}
}

func (user *User) connectTeam(userTeam *database.UserTeam) error {
	user.log.Debugfln("connecting %s to team %s", userTeam.SlackEmail, userTeam.SlackEmail)
	userTeam.Client = slack.New(userTeam.Token)

	userTeam.RTM = userTeam.Client.NewRTM()

	go userTeam.RTM.ManageConnection()

	go user.slackMessageHandler(userTeam)

	return nil
}

func (user *User) Connect() error {
	user.Lock()
	defer user.Unlock()

	user.log.Debugln("connecting to slack")

	for _, userTeam := range user.Teams {
		user.connectTeam(userTeam)
	}

	return nil
}

func (user *User) disconnectTeam(userTeam *database.UserTeam) error {
	if userTeam.RTM != nil {
		if err := userTeam.RTM.Disconnect(); err != nil {
			return err
		}
	}

	userTeam.Client = nil

	return nil
}

func (user *User) Disconnect() error {
	user.Lock()
	defer user.Unlock()

	for _, userTeam := range user.Teams {
		if err := user.disconnectTeam(userTeam); err != nil {
			return err
		}
	}

	return nil
}

func (user *User) GetUserTeam(teamID, userID string) *database.UserTeam {
	user.TeamsLock.Lock()
	defer user.TeamsLock.Unlock()

	key := database.UserTeamKey{
		MXID:    user.MXID,
		TeamID:  teamID,
		SlackID: userID,
	}

	if userTeam, found := user.Teams[key]; found {
		return userTeam
	}

	return nil
}
