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
	"fmt"
	"sort"
	"strings"
	"sync"

	log "maunium.net/go/maulogger/v2"

	"github.com/slack-go/slack"

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

var (
	ErrNotConnected = errors.New("not connected")
	ErrNotLoggedIn  = errors.New("not logged in")
)

type User struct {
	*database.User

	sync.Mutex

	bridge *SlackBridge
	log    log.Logger

	BridgeStates map[string]*bridge.BridgeStateQueue

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
	_, _, isPuppet := br.ParsePuppetMXID(userID)
	if isPuppet || userID == br.Bot.UserID {
		return nil
	}

	br.usersLock.Lock()
	defer br.usersLock.Unlock()

	user, ok := br.usersByMXID[userID]
	if !ok {
		br.Log.Debugfln("User %s not present in usersByMXID map!", userID)
		return br.loadUser(br.DB.User.GetByMXID(userID), &userID)
	}

	return user
}

func (br *SlackBridge) GetUserByID(teamID, userID string) *User {
	br.usersLock.Lock()
	defer br.usersLock.Unlock()

	user, ok := br.usersByID[teamID+"-"+userID]
	if !ok {
		return br.loadUser(br.DB.User.GetBySlackID(teamID, userID), nil)
	}

	return user
}

func (br *SlackBridge) NewUser(dbUser *database.User) *User {
	user := &User{
		User:   dbUser,
		bridge: br,
		log:    br.Log.Sub("User").Sub(string(dbUser.MXID)),
	}

	user.PermissionLevel = br.Config.Bridge.Permissions.Get(user.MXID)
	user.BridgeStates = make(map[string]*bridge.BridgeStateQueue)

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

	if !br.historySyncLoopStarted {
		br.Log.Debugln("Starting backfill loop")
		go br.handleHistorySyncsLoop()
	}

	users := br.getAllUsers()

	for _, user := range users {
		go user.Connect()
	}
	if sort.Search(len(users), func(i int) bool { return len(users[i].Teams) > 0 }) == len(users) { // if there are no users with any configured userTeams
		br.Log.Debugln("No users with userTeams found, sending UNCONFIGURED")
		br.SendGlobalBridgeState(status.BridgeState{StateEvent: status.StateUnconfigured}.Fill(nil))
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

func (user *User) tryAutomaticDoublePuppeting(userTeam *database.UserTeam) {
	user.Lock()
	defer user.Unlock()

	if !user.bridge.Config.CanAutoDoublePuppet(user.MXID) {
		return
	}

	user.log.Debugln("Checking if double puppeting needs to be enabled")

	puppet := user.bridge.GetPuppetByID(userTeam.Key.TeamID, userTeam.Key.SlackID)
	if puppet.CustomMXID != "" {
		user.log.Debugln("User already has double-puppeting enabled")

		return
	}

	accessToken, err := puppet.loginWithSharedSecret(user.MXID, userTeam.Key.TeamID)
	if err != nil {
		user.log.Warnln("Failed to login with shared secret:", err)

		return
	}

	err = puppet.SwitchCustomMXID(accessToken, user.MXID)
	if err != nil {
		puppet.log.Warnln("Failed to switch to auto-logined custom puppet:", err)

		return
	}

	user.log.Infoln("Successfully automatically enabled custom puppet")
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

func (user *User) login(info *auth.Info, force bool) {
	userTeam := user.bridge.DB.UserTeam.New()

	userTeam.Key.MXID = user.MXID
	userTeam.Key.SlackID = info.UserID
	userTeam.Key.TeamID = info.TeamID
	userTeam.SlackEmail = info.UserEmail
	userTeam.TeamName = info.TeamName
	userTeam.Token = info.Token
	userTeam.CookieToken = info.CookieToken

	// We minimize the time we hold the lock because SyncTeams also needs the
	// lock.
	user.TeamsLock.Lock()
	user.Teams[userTeam.Key.TeamID] = userTeam
	user.TeamsLock.Unlock()

	user.User.SyncTeams()

	user.log.Debugfln("logged into %s successfully", info.TeamName)

	user.BridgeStates[info.TeamID] = user.bridge.NewBridgeStateQueue(userTeam, user.log)
	user.bridge.usersByID[fmt.Sprintf("%s-%s", userTeam.Key.TeamID, userTeam.Key.SlackID)] = user
	user.connectTeam(userTeam)
}

func (user *User) LoginTeam(email, team, password string) error {
	info, err := auth.LoginPassword(user.log, email, team, password)
	if err != nil {
		return err
	}

	go user.login(info, false)
	return nil
}

func (user *User) TokenLogin(token string, cookieToken string) (*auth.Info, error) {
	info, err := auth.LoginToken(token, cookieToken)
	if err != nil {
		return nil, err
	}

	go user.login(info, true)
	return info, nil
}

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

func (user *User) LogoutUserTeam(userTeam *database.UserTeam) error {
	if userTeam == nil || !userTeam.IsLoggedIn() {
		return ErrNotLoggedIn
	}

	user.leavePortals(userTeam)

	puppet := user.bridge.GetPuppetByID(userTeam.Key.TeamID, userTeam.Key.SlackID)
	if puppet.CustomMXID != "" {
		err := puppet.SwitchCustomMXID("", "")
		if err != nil {
			user.log.Warnln("Failed to logout-matrix while logging out of Slack:", err)
		}
	}

	if userTeam.RTM != nil {
		if err := userTeam.RTM.Disconnect(); err != nil {
			user.BridgeStates[userTeam.Key.TeamID].Send(status.BridgeState{StateEvent: status.StateUnknownError, Message: err.Error()})
			return err
		}
	}

	userTeam.Client = nil

	user.BridgeStates[userTeam.Key.TeamID].Send(status.BridgeState{StateEvent: status.StateLoggedOut})

	user.TeamsLock.Lock()
	delete(user.Teams, userTeam.Key.TeamID)
	user.TeamsLock.Unlock()

	userTeam.Token = ""
	userTeam.CookieToken = ""
	userTeam.Upsert()

	user.Update()

	return nil
}

func (user *User) leavePortals(userTeam *database.UserTeam) {
	for _, portal := range user.bridge.GetAllPortalsForUserTeam(userTeam.Key) {
		portal.leave(userTeam)
	}
}

func (user *User) slackMessageHandler(userTeam *database.UserTeam) {
	user.log.Debugfln("Start receiving Slack events for %s", userTeam.Key)
	for msg := range userTeam.RTM.IncomingEvents {
		switch event := msg.Data.(type) {
		case *slack.ConnectingEvent:
			user.log.Debugfln("connecting: attempt %d", event.Attempt)
			user.BridgeStates[userTeam.Key.TeamID].Send(status.BridgeState{StateEvent: status.StateConnecting})
		case *slack.ConnectedEvent:
			// Update all of our values according to what the server has for us.
			userTeam.Key.SlackID = event.Info.User.ID
			userTeam.Key.TeamID = event.Info.Team.ID
			userTeam.TeamName = event.Info.Team.Name

			userTeam.Upsert()

			user.tryAutomaticDoublePuppeting(userTeam)
			user.BridgeStates[userTeam.Key.TeamID].Send(status.BridgeState{StateEvent: status.StateConnected})

			user.log.Infofln("connected to team %s as %s", userTeam.TeamName, userTeam.SlackEmail)
		case *slack.HelloEvent:
			// Ignored for now
		case *slack.InvalidAuthEvent:
			user.log.Errorln("invalid authentication token")

			user.BridgeStates[userTeam.Key.TeamID].Send(status.BridgeState{StateEvent: status.StateBadCredentials})

			// TODO: Should drop a message in the management room

			return
		case *slack.LatencyReport:
			user.log.Debugln("latency report:", event.Value)
		case *slack.MessageEvent:
			key := database.NewPortalKey(userTeam.Key.TeamID, event.Channel)
			portal := user.bridge.GetPortalByID(key)
			if portal != nil {
				if portal.MXID == "" {
					channel, err := userTeam.Client.GetConversationInfo(&slack.GetConversationInfoInput{
						ChannelID:         event.Channel,
						IncludeLocale:     true,
						IncludeNumMembers: true,
					})
					if err != nil {
						portal.log.Errorln("failed to lookup channel info:", err)
						return
					}

					portal.log.Debugln("Creating Matrix room from incoming message")
					if err := portal.CreateMatrixRoom(user, userTeam, channel, false); err != nil {
						portal.log.Errorln("Failed to create portal room:", err)
						return
					}
				}
				portal.HandleSlackMessage(user, userTeam, event)
			}
		case *slack.ReactionAddedEvent:
			key := database.NewPortalKey(userTeam.Key.TeamID, event.Item.Channel)
			portal := user.bridge.GetPortalByID(key)
			if portal != nil {
				portal.HandleSlackReaction(user, userTeam, event)
			}
		case *slack.ReactionRemovedEvent:
			key := database.NewPortalKey(userTeam.Key.TeamID, event.Item.Channel)
			portal := user.bridge.GetPortalByID(key)
			if portal != nil {
				portal.HandleSlackReactionRemoved(user, userTeam, event)
			}
		case *slack.UserTypingEvent:
			key := database.NewPortalKey(userTeam.Key.TeamID, event.Channel)
			portal := user.bridge.GetPortalByID(key)
			if portal != nil {
				portal.HandleSlackTyping(user, userTeam, event)
			}
		case *slack.ChannelMarkedEvent:
			key := database.NewPortalKey(userTeam.Key.TeamID, event.Channel)
			portal := user.bridge.GetPortalByID(key)
			if portal != nil {
				portal.HandleSlackChannelMarked(user, userTeam, event)
			}
		case *slack.RTMError:
			user.log.Errorln("rtm error:", event.Error())
			user.BridgeStates[userTeam.Key.TeamID].Send(status.BridgeState{StateEvent: status.StateUnknownError, Message: event.Error()})
		default:
			user.log.Warnln("unknown message", msg)
		}
	}
	user.log.Errorfln("Slack RTM for %s unexpectedly disconnected!", userTeam.Key)
	user.BridgeStates[userTeam.Key.TeamID].Send(status.BridgeState{StateEvent: status.StateUnknownError, Message: "Disconnected for unknown reason"})
}

func (user *User) connectTeam(userTeam *database.UserTeam) {
	user.log.Infofln("Connecting %s to Slack userteam %s (%s)", user.MXID, userTeam.Key, userTeam.TeamName)
	slackOptions := []slack.Option{
		slack.OptionLog(SlackgoLogger{user.log.Sub(fmt.Sprintf("SlackGo/%s", userTeam.Key))}),
		//slack.OptionDebug(user.bridge.Config.Logging.PrintLevel <= 0),
	}
	if userTeam.CookieToken != "" {
		slackOptions = append(slackOptions, slack.OptionCookie("d", userTeam.CookieToken))
	}
	userTeam.Client = slack.New(userTeam.Token, slackOptions...)

	userTeam.RTM = userTeam.Client.NewRTM()

	go userTeam.RTM.ManageConnection()

	go user.slackMessageHandler(userTeam)

	user.UpdateTeam(userTeam, false)
}

func (user *User) isChannelOrOpenIM(channel *slack.Channel, userTeam *database.UserTeam) bool {
	if !channel.IsIM {
		return true
	} else {
		info, err := userTeam.Client.GetConversationInfo(&slack.GetConversationInfoInput{
			ChannelID:         channel.ID,
			IncludeLocale:     true,
			IncludeNumMembers: true,
		})
		if err != nil {
			user.log.Errorfln("Error getting information about IM: %v", err)
			return false
		}
		return info.Latest != nil && info.Latest.SubType != "joiner_notification_for_inviter" && info.Latest.SubType != "joiner_notification"
	}
}

func (user *User) SyncPortals(userTeam *database.UserTeam, force bool) error {
	channelInfo := map[string]slack.Channel{}

	if !strings.HasPrefix(userTeam.Token, "xoxs") {
		// TODO: use pagination to make sure we get everything!
		channels, _, err := userTeam.Client.GetConversationsForUser(&slack.GetConversationsForUserParameters{
			Types: []string{"public_channel", "private_channel", "mpim", "im"},
		})
		if err != nil {
			user.log.Warnfln("Error fetching channels: %v", err)
		}
		for _, channel := range channels {
			if user.isChannelOrOpenIM(&channel, userTeam) {
				channelInfo[channel.ID] = channel
			}
		}
	} else {
		user.log.Warnfln("Not fetching channels for userteam %s: xoxs token type can't fetch user's joined channels", userTeam.Key)
	}

	portals := user.bridge.DB.Portal.GetAllForUserTeam(userTeam.Key)
	for _, dbPortal := range portals {
		// First, go through all pre-existing portals and update their info
		portal := user.bridge.GetPortalByID(dbPortal.Key)
		channel := channelInfo[dbPortal.Key.ChannelID]
		if portal.MXID != "" {
			portal.UpdateInfo(user, userTeam, &channel, force)
			portal.ensureUserInvited(user)
			portal.InsertUser(userTeam.Key)
		} else {
			portal.CreateMatrixRoom(user, userTeam, &channel, true)
		}
		// Delete already handled ones from the map
		delete(channelInfo, dbPortal.Key.ChannelID)
	}

	for _, channel := range channelInfo {
		// Remaining ones in the map are new channels that weren't handled yet
		key := database.NewPortalKey(userTeam.Key.TeamID, channel.ID)
		portal := user.bridge.GetPortalByID(key)
		if portal.MXID != "" {
			portal.UpdateInfo(user, userTeam, &channel, force)
			portal.InsertUser(userTeam.Key)
		} else {
			portal.CreateMatrixRoom(user, userTeam, &channel, true)
		}
	}

	return nil
}

func (user *User) UpdateTeam(userTeam *database.UserTeam, force bool) error {
	user.log.Debugfln("Updating team info for team %s", userTeam.Key.TeamID)
	currentTeamInfo := user.bridge.DB.TeamInfo.GetBySlackTeam(userTeam.Key.TeamID)
	if currentTeamInfo == nil {
		currentTeamInfo = user.bridge.DB.TeamInfo.New()
		currentTeamInfo.TeamID = userTeam.Key.TeamID
	}

	teamInfo, err := userTeam.Client.GetTeamInfo()
	if err != nil {
		user.log.Errorfln("Error fetching info for team %s: %v", userTeam.Key.TeamID, err)
		return err
	}
	changed := false

	if currentTeamInfo.TeamName != teamInfo.Name {
		currentTeamInfo.TeamName = teamInfo.Name
		changed = true
	}
	if currentTeamInfo.TeamDomain != teamInfo.Domain {
		currentTeamInfo.TeamDomain = teamInfo.Domain
		changed = true
	}
	if currentTeamInfo.TeamUrl != teamInfo.URL {
		currentTeamInfo.TeamUrl = teamInfo.URL
		changed = true
	}
	if teamInfo.Icon["image_230"] != nil && currentTeamInfo.Avatar != teamInfo.Icon["image_230"] {
		avatar, err := uploadAvatar(user.bridge.AS.BotIntent(), teamInfo.Icon["image_230"].(string))
		if err != nil {
			user.log.Warnfln("Error uploading new team avatar for team %s: %v", userTeam.Key.TeamID, err)
		} else {
			currentTeamInfo.Avatar = teamInfo.Icon["image_230"].(string)
			currentTeamInfo.AvatarUrl = avatar
			changed = true
		}
	}

	currentTeamInfo.Upsert()
	return user.SyncPortals(userTeam, changed || force)
}

func (user *User) Connect() error {
	user.Lock()
	defer user.Unlock()

	user.log.Infofln("Connecting Slack teams for user %s", user.MXID)
	for key, userTeam := range user.Teams {
		user.bridge.usersByID[fmt.Sprintf("%s-%s", userTeam.Key.TeamID, userTeam.Key.SlackID)] = user
		user.BridgeStates[key] = user.bridge.NewBridgeStateQueue(userTeam, user.log)
		user.connectTeam(userTeam)
		// if err != nil {
		// 	user.log.Errorfln("Error connecting to Slack userteam %s: %v", userTeam.Key, err)
		// 	// TODO: more detailed error state
		// 	user.BridgeStates[key].Send(status.BridgeState{StateEvent: status.StateUnknownError, Message: err.Error()})
		// }
	}

	return nil
}

func (user *User) disconnectTeam(userTeam *database.UserTeam) error {
	user.log.Infofln("Disconnecting Slack userteam %s", userTeam.Key)
	if userTeam.RTM != nil {
		if err := userTeam.RTM.Disconnect(); err != nil {
			user.log.Errorfln("Error disconnecting RTM for %s: %v", userTeam.Key, err)
			user.BridgeStates[userTeam.Key.TeamID].Send(status.BridgeState{StateEvent: status.StateUnknownError, Message: err.Error()})
			return err
		}
	}

	userTeam.Client = nil
	user.log.Debugfln("Slack client for %s set to nil!", userTeam.Key)

	return nil
}

func (user *User) Disconnect() error {
	user.Lock()
	defer user.Unlock()

	user.log.Infofln("Disconnecting Slack teams for user %s", user.MXID)
	for _, userTeam := range user.Teams {
		if err := user.disconnectTeam(userTeam); err != nil {
			return err
		}
	}

	return nil
}

func (user *User) GetUserTeam(teamID string) *database.UserTeam {
	user.TeamsLock.Lock()
	defer user.TeamsLock.Unlock()

	if userTeam, found := user.Teams[teamID]; found {
		return userTeam
	}

	return nil
}

// func (user *User) getDirectChats() map[id.UserID][]id.RoomID {
// 	chats := map[id.UserID][]id.RoomID{}

// 	privateChats := user.bridge.DB.Portal.FindPrivateChatsOf(user.DiscordID)
// 	for _, portal := range privateChats {
// 		if portal.MXID != "" {
// 			puppetMXID := user.bridge.FormatPuppetMXID(portal.Key.Receiver)

// 			chats[puppetMXID] = []id.RoomID{portal.MXID}
// 		}
// 	}

// 	return chats
// }

// func (user *User) updateDirectChats(chats map[id.UserID][]id.RoomID) {
// 	if !user.bridge.Config.Bridge.SyncDirectChatList {
// 		return
// 	}

// 	puppet := user.bridge.GetPuppetByMXID(user.MXID)
// 	if puppet == nil {
// 		return
// 	}

// 	intent := puppet.CustomIntent()
// 	if intent == nil {
// 		return
// 	}

// 	method := http.MethodPatch
// 	if chats == nil {
// 		chats = user.getDirectChats()
// 		method = http.MethodPut
// 	}

// 	user.log.Debugln("Updating m.direct list on homeserver")

// 	var err error
// 	if user.bridge.Config.Homeserver.Software {
// 		urlPath := intent.BuildURL(mautrix.ClientURLPath{"unstable", "com.beeper.asmux", "dms"})
// 		_, err = intent.MakeFullRequest(mautrix.FullRequest{
// 			Method:      method,
// 			URL:         urlPath,
// 			Headers:     http.Header{"X-Asmux-Auth": {user.bridge.AS.Registration.AppToken}},
// 			RequestJSON: chats,
// 		})
// 	} else {
// 		existingChats := map[id.UserID][]id.RoomID{}

// 		err = intent.GetAccountData(event.AccountDataDirectChats.Type, &existingChats)
// 		if err != nil {
// 			user.log.Warnln("Failed to get m.direct list to update it:", err)

// 			return
// 		}

// 		for userID, rooms := range existingChats {
// 			if _, ok := user.bridge.ParsePuppetMXID(userID); !ok {
// 				// This is not a ghost user, include it in the new list
// 				chats[userID] = rooms
// 			} else if _, ok := chats[userID]; !ok && method == http.MethodPatch {
// 				// This is a ghost user, but we're not replacing the whole list, so include it too
// 				chats[userID] = rooms
// 			}
// 		}

// 		err = intent.SetAccountData(event.AccountDataDirectChats.Type, &chats)
// 	}

// 	if err != nil {
// 		user.log.Warnln("Failed to update m.direct list:", err)
// 	}
// }

func (user *User) ensureInvited(intent *appservice.IntentAPI, roomID id.RoomID, isDirect bool) bool {
	if intent == nil {
		intent = user.bridge.Bot
	}
	ret := false

	inviteContent := event.Content{
		Parsed: &event.MemberEventContent{
			Membership: event.MembershipInvite,
			IsDirect:   isDirect,
		},
		Raw: map[string]interface{}{},
	}

	customPuppet := user.bridge.GetPuppetByCustomMXID(user.MXID)
	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		inviteContent.Raw["fi.mau.will_auto_accept"] = true
	}

	_, err := intent.SendStateEvent(roomID, event.StateMember, user.MXID.String(), &inviteContent)

	var httpErr mautrix.HTTPError
	if err != nil && errors.As(err, &httpErr) && httpErr.RespError != nil && strings.Contains(httpErr.RespError.Err, "is already in the room") {
		user.bridge.StateStore.SetMembership(roomID, user.MXID, event.MembershipJoin)
		ret = true
	} else if err != nil {
		user.log.Warnfln("Failed to invite user to %s: %v", roomID, err)
	} else {
		ret = true
	}

	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		err = customPuppet.CustomIntent().EnsureJoined(roomID, appservice.EnsureJoinedParams{IgnoreCache: true})
		if err != nil {
			user.log.Warnfln("Failed to auto-join %s: %v", roomID, err)
			ret = false
		} else {
			ret = true
		}
	}

	return ret
}

func (user *User) updateChatMute(portal *Portal, muted bool) {
	if len(portal.MXID) == 0 {
		return
	}
	puppet := user.GetIDoublePuppet()
	if puppet == nil {
		return
	}
	intent := puppet.CustomIntent()
	if intent == nil {
		return
	}
	var err error
	if muted {
		user.log.Debugfln("Muting portal %s...", portal.MXID)
		err = intent.PutPushRule("global", pushrules.RoomRule, string(portal.MXID), &mautrix.ReqPutPushRule{
			Actions: []pushrules.PushActionType{pushrules.ActionDontNotify},
		})
	} else {
		user.log.Debugfln("Unmuting portal %s...", portal.MXID)
		err = intent.DeletePushRule("global", pushrules.RoomRule, string(portal.MXID))
	}
	if err != nil && !errors.Is(err, mautrix.MNotFound) {
		user.log.Warnfln("Failed to update push rule for %s through double puppet: %v", portal.MXID, err)
	}
}
