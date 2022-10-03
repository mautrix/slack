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
	"fmt"
	"regexp"
	"strings"
	"sync"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/id"

	"github.com/slack-go/slack"

	"go.mau.fi/mautrix-slack/database"
)

type Puppet struct {
	*database.Puppet

	bridge *SlackBridge
	log    log.Logger

	MXID id.UserID

	customIntent *appservice.IntentAPI
	customUser   *User

	syncLock sync.Mutex
}

var _ bridge.Ghost = (*Puppet)(nil)

func (puppet *Puppet) GetMXID() id.UserID {
	return puppet.MXID
}

var userIDRegex *regexp.Regexp

func (br *SlackBridge) NewPuppet(dbPuppet *database.Puppet) *Puppet {
	return &Puppet{
		Puppet: dbPuppet,
		bridge: br,
		log:    br.Log.Sub(fmt.Sprintf("Puppet/%s-%s", dbPuppet.TeamID, dbPuppet.UserID)),

		MXID: br.FormatPuppetMXID(dbPuppet.TeamID + "-" + dbPuppet.UserID),
	}
}

func (br *SlackBridge) ParsePuppetMXID(mxid id.UserID) (string, string, bool) {
	if userIDRegex == nil {
		pattern := fmt.Sprintf(
			"^@%s:%s$",
			br.Config.Bridge.FormatUsername("([A-Z0-9]+)-([A-Z0-9]+)"),
			br.Config.Homeserver.Domain,
		)

		userIDRegex = regexp.MustCompile(pattern)
	}

	match := userIDRegex.FindStringSubmatch(string(mxid))
	if len(match) == 3 {
		return match[1], match[2], true
	}

	return "", "", false
}

func (br *SlackBridge) GetPuppetByMXID(mxid id.UserID) *Puppet {
	team, id, ok := br.ParsePuppetMXID(mxid)
	if !ok {
		return nil
	}

	return br.GetPuppetByID(team, id)
}

func (br *SlackBridge) GetPuppetByID(teamID, userID string) *Puppet {
	br.puppetsLock.Lock()
	defer br.puppetsLock.Unlock()

	puppet, ok := br.puppets[teamID+"-"+userID]
	if !ok {
		dbPuppet := br.DB.Puppet.Get(teamID, userID)
		if dbPuppet == nil {
			dbPuppet = br.DB.Puppet.New()
			dbPuppet.TeamID = teamID
			dbPuppet.UserID = userID
			dbPuppet.Insert()
		}

		puppet = br.NewPuppet(dbPuppet)
		br.puppets[puppet.Key()] = puppet
	}

	return puppet
}

func (br *SlackBridge) GetPuppetByCustomMXID(mxid id.UserID) *Puppet {
	br.puppetsLock.Lock()
	defer br.puppetsLock.Unlock()

	puppet, ok := br.puppetsByCustomMXID[mxid]
	if !ok {
		dbPuppet := br.DB.Puppet.GetByCustomMXID(mxid)
		if dbPuppet == nil {
			return nil
		}

		puppet = br.NewPuppet(dbPuppet)
		br.puppets[puppet.Key()] = puppet
		br.puppetsByCustomMXID[puppet.CustomMXID] = puppet
	}

	return puppet
}

func (br *SlackBridge) GetAllPuppetsWithCustomMXID() []*Puppet {
	return br.dbPuppetsToPuppets(br.DB.Puppet.GetAllWithCustomMXID())
}

func (br *SlackBridge) GetAllPuppets() []*Puppet {
	return br.dbPuppetsToPuppets(br.DB.Puppet.GetAll())
}

func (br *SlackBridge) dbPuppetsToPuppets(dbPuppets []*database.Puppet) []*Puppet {
	br.puppetsLock.Lock()
	defer br.puppetsLock.Unlock()

	output := make([]*Puppet, len(dbPuppets))
	for index, dbPuppet := range dbPuppets {
		if dbPuppet == nil {
			continue
		}

		puppet, ok := br.puppets[dbPuppet.TeamID+"-"+dbPuppet.UserID]
		if !ok {
			puppet = br.NewPuppet(dbPuppet)
			br.puppets[puppet.Key()] = puppet

			if dbPuppet.CustomMXID != "" {
				br.puppetsByCustomMXID[dbPuppet.CustomMXID] = puppet
			}
		}

		output[index] = puppet
	}

	return output
}

func (br *SlackBridge) FormatPuppetMXID(did string) id.UserID {
	return id.NewUserID(
		br.Config.Bridge.FormatUsername(strings.ToLower(did)),
		br.Config.Homeserver.Domain,
	)
}

func (puppet *Puppet) DefaultIntent() *appservice.IntentAPI {
	return puppet.bridge.AS.Intent(puppet.MXID)
}

func (puppet *Puppet) IntentFor(portal *Portal) *appservice.IntentAPI {
	if puppet.customIntent == nil {
		return puppet.DefaultIntent()
	}

	return puppet.customIntent
}

func (puppet *Puppet) CustomIntent() *appservice.IntentAPI {
	return puppet.customIntent
}

func (puppet *Puppet) Key() string {
	return puppet.TeamID + "-" + puppet.UserID
}

func (puppet *Puppet) updatePortalMeta(meta func(portal *Portal)) {
	for _, portal := range puppet.bridge.GetDMPortalsWith(puppet.UserID) {
		// Get room create lock to prevent races between receiving contact info and room creation.
		portal.roomCreateLock.Lock()
		meta(portal)
		portal.roomCreateLock.Unlock()
	}
}

func (puppet *Puppet) updateName(source *User) bool {
	userTeam := source.GetUserTeam(puppet.TeamID)
	user, err := userTeam.Client.GetUserInfo(puppet.UserID)
	if err != nil {
		puppet.log.Warnln("failed to get user from id:", err)
		return false
	}

	newName := puppet.bridge.Config.Bridge.FormatDisplayname(user)

	if puppet.Name != newName {
		err := puppet.DefaultIntent().SetDisplayName(newName)
		if err == nil {
			puppet.Name = newName
			puppet.Update()
		} else {
			puppet.log.Warnln("failed to set display name:", err)
		}

		return true
	}

	return false
}

func (puppet *Puppet) updateAvatar(source *User) bool {
	// TODO
	return false
	// user, err := source.Client.GetUserInfo(puppet.ID)
	// if err != nil {
	// 	puppet.log.Warnln("Failed to get user:", err)

	// 	return false
	// }

	// if puppet.Avatar == user.Avatar {
	// 	return false
	// }

	// if user.Avatar == "" {
	// 	puppet.log.Warnln("User does not have an avatar")

	// 	return false
	// }

	// url, err := uploadAvatar(puppet.DefaultIntent(), user.AvatarURL(""))
	// if err != nil {
	// 	puppet.log.Warnln("Failed to upload user avatar:", err)

	// 	return false
	// }

	// puppet.AvatarURL = url

	// err = puppet.DefaultIntent().SetAvatarURL(puppet.AvatarURL)
	// if err != nil {
	// 	puppet.log.Warnln("Failed to set avatar:", err)
	// }

	// puppet.log.Debugln("Updated avatar", puppet.Avatar, "->", user.Avatar)
	// puppet.Avatar = user.Avatar
	// go puppet.updatePortalAvatar()

	// return true
}

func (puppet *Puppet) updatePortalAvatar() {
	puppet.updatePortalMeta(func(portal *Portal) {
		if portal.MXID != "" {
			_, err := portal.MainIntent().SetRoomAvatar(portal.MXID, puppet.AvatarURL)
			if err != nil {
				portal.log.Warnln("Failed to set avatar:", err)
			}
		}

		portal.AvatarURL = puppet.AvatarURL
		portal.Avatar = puppet.Avatar
		portal.Update()
	})

}

func (puppet *Puppet) SyncContact(source *User) {
	puppet.syncLock.Lock()
	defer puppet.syncLock.Unlock()

	puppet.log.Debugln("syncing contact", puppet.Name)

	err := puppet.DefaultIntent().EnsureRegistered()
	if err != nil {
		puppet.log.Errorln("Failed to ensure registered:", err)
	}

	update := false

	update = puppet.updateName(source) || update

	if puppet.Avatar == "" {
		update = puppet.updateAvatar(source) || update
		puppet.log.Debugln("update avatar returned", update)
	}

	if update {
		puppet.Update()
	}
}

func (puppet *Puppet) UpdateName(newName string) bool {
	if puppet.Name == newName && puppet.NameSet {
		return false
	}
	puppet.Name = newName
	puppet.NameSet = false
	err := puppet.DefaultIntent().SetDisplayName(newName)
	if err != nil {
		puppet.log.Warnln("Failed to update displayname:", err)
	} else {
		go puppet.updatePortalMeta(func(portal *Portal) {
			if portal.UpdateNameDirect(puppet.Name) {
				portal.Update()
				portal.UpdateBridgeInfo()
			}
		})
		puppet.NameSet = true
	}
	return true
}

func (puppet *Puppet) UpdateAvatar(url string) bool {
	if puppet.Avatar == url && puppet.AvatarSet {
		return false
	}
	avatarChanged := url != puppet.Avatar
	puppet.Avatar = url
	puppet.AvatarSet = false
	puppet.AvatarURL = id.ContentURI{}

	// TODO should we just use slack's default avatars for users with no avatar?
	if puppet.Avatar != "" && (puppet.AvatarURL.IsEmpty() || avatarChanged) {
		url, err := uploadAvatar(puppet.DefaultIntent(), url)
		if err != nil {
			puppet.log.Warnfln("Failed to reupload user avatar %s: %v", puppet.Avatar, err)
			return true
		}
		puppet.AvatarURL = url
	}

	err := puppet.DefaultIntent().SetAvatarURL(puppet.AvatarURL)
	if err != nil {
		puppet.log.Warnln("Failed to update avatar:", err)
	} else {
		go puppet.updatePortalMeta(func(portal *Portal) {
			if portal.UpdateAvatarFromPuppet(puppet) {
				portal.Update()
				portal.UpdateBridgeInfo()
			}
		})
		puppet.AvatarSet = true
	}
	return true
}

func (puppet *Puppet) UpdateInfo(userTeam *database.UserTeam, info *slack.User) {
	puppet.syncLock.Lock()
	defer puppet.syncLock.Unlock()

	if info == nil {
		if puppet.Name != "" {
			return
		}

		var err error
		puppet.log.Debugfln("Fetching info through team %s to update", userTeam.Key.TeamID)

		info, err = userTeam.Client.GetUserInfo(puppet.UserID)
		if err != nil {
			puppet.log.Errorfln("Failed to fetch info through %s: %v", userTeam.Key.TeamID, err)
			return
		}
	}

	err := puppet.DefaultIntent().EnsureRegistered()
	if err != nil {
		puppet.log.Errorln("Failed to ensure registered:", err)
	}

	changed := false

	newName := puppet.bridge.Config.Bridge.FormatDisplayname(info)
	changed = puppet.UpdateName(newName) || changed
	changed = puppet.UpdateAvatar(info.Profile.ImageOriginal) || changed

	if changed {
		puppet.Update()
	}
}

func (puppet *Puppet) UpdateInfoBot(userTeam *database.UserTeam) {
	puppet.syncLock.Lock()
	defer puppet.syncLock.Unlock()

	puppet.log.Debugfln("Fetching bot info through team %s to update", userTeam.Key.TeamID)
	info, err := userTeam.Client.GetBotInfo(puppet.UserID)
	if err != nil {
		puppet.log.Errorfln("Failed to fetch bot info through %s: %v", userTeam.Key.TeamID, err)
		return
	}

	err = puppet.DefaultIntent().EnsureRegistered()
	if err != nil {
		puppet.log.Errorfln("Failed to ensure bot %s registered: %v", puppet.UserID, err)
	}

	changed := false

	newName := puppet.bridge.Config.Bridge.FormatBotDisplayname(info)
	changed = puppet.UpdateName(newName) || changed
	changed = puppet.UpdateAvatar(info.Icons.Image72) || changed

	if changed {
		puppet.Update()
	}
}
