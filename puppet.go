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
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/id"

	"github.com/slack-go/slack"

	"go.mau.fi/mautrix-slack/database"
)

type Puppet struct {
	*database.Puppet

	bridge *SlackBridge
	zlog   zerolog.Logger

	MXID id.UserID

	lastSync time.Time
	syncLock sync.Mutex
}

var _ bridge.Ghost = (*Puppet)(nil)

func (puppet *Puppet) SwitchCustomMXID(accessToken string, userID id.UserID) error {
	return fmt.Errorf("puppets don't support custom MXIDs here")
}

func (puppet *Puppet) ClearCustomMXID() {}

func (puppet *Puppet) CustomIntent() *appservice.IntentAPI {
	return nil
}

func (puppet *Puppet) GetMXID() id.UserID {
	return puppet.MXID
}

func (br *SlackBridge) loadPuppet(ctx context.Context, dbPuppet *database.Puppet, key *database.UserTeamKey) *Puppet {
	if dbPuppet == nil {
		if key == nil {
			return nil
		}
		dbPuppet = br.DB.Puppet.New()
		dbPuppet.UserTeamKey = *key
		err := dbPuppet.Insert(ctx)
		if err != nil {
			br.ZLog.Err(err).Object("puppet_id", key).Msg("Failed to insert new puppet")
			return nil
		}
	}

	puppet := br.newPuppet(dbPuppet)
	br.puppets[puppet.UserTeamKey] = puppet
	return puppet
}

func (br *SlackBridge) newPuppet(dbPuppet *database.Puppet) *Puppet {
	log := br.ZLog.With().Object("puppet_id", dbPuppet.UserTeamKey).Logger()
	return &Puppet{
		Puppet: dbPuppet,
		bridge: br,
		zlog:   log,
		MXID:   br.FormatPuppetMXID(dbPuppet.UserTeamKey),
	}
}

func (br *SlackBridge) FormatPuppetMXID(utk database.UserTeamKey) id.UserID {
	return id.NewUserID(
		br.Config.Bridge.FormatUsername(fmt.Sprintf("%s-%s", strings.ToLower(utk.TeamID), strings.ToLower(utk.UserID))),
		br.Config.Homeserver.Domain,
	)
}

var userIDRegex *regexp.Regexp

func (br *SlackBridge) ParsePuppetMXID(mxid id.UserID) (database.UserTeamKey, bool) {
	if userIDRegex == nil {
		userIDRegex = br.Config.MakeUserIDRegex("([a-z0-9]+)-([a-z0-9]+)")
	}

	match := userIDRegex.FindStringSubmatch(string(mxid))
	if len(match) == 3 {
		return database.UserTeamKey{TeamID: strings.ToUpper(match[1]), UserID: strings.ToUpper(match[2])}, true
	}

	return database.UserTeamKey{}, false
}

func (br *SlackBridge) GetPuppetByMXID(mxid id.UserID) *Puppet {
	key, ok := br.ParsePuppetMXID(mxid)
	if !ok {
		return nil
	}

	return br.GetPuppetByID(key)
}

func (br *SlackBridge) GetPuppetByID(key database.UserTeamKey) *Puppet {
	if key.TeamID == "" || key.UserID == "" {
		return nil
	}
	br.puppetsLock.Lock()
	defer br.puppetsLock.Unlock()

	puppet, ok := br.puppets[key]
	if !ok {
		ctx := context.TODO()
		dbPuppet, err := br.DB.Puppet.Get(ctx, key)
		if err != nil {
			br.ZLog.Err(err).Object("puppet_id", key).Msg("Failed to get puppet from database")
			return nil
		}
		return br.loadPuppet(ctx, dbPuppet, &key)
	}

	return puppet
}

func (br *SlackBridge) GetAllPuppetsForTeam(teamID string) []*Puppet {
	return br.dbPuppetsToPuppets(br.DB.Puppet.GetAllForTeam(context.TODO(), teamID))
}

func (br *SlackBridge) dbPuppetsToPuppets(dbPuppets []*database.Puppet, err error) []*Puppet {
	if err != nil {
		br.ZLog.Err(err).Msg("Failed to load puppets")
		return nil
	}
	br.puppetsLock.Lock()
	defer br.puppetsLock.Unlock()

	output := make([]*Puppet, len(dbPuppets))
	ctx := context.TODO()
	for i, dbPuppet := range dbPuppets {
		puppet, ok := br.puppets[dbPuppet.UserTeamKey]
		if ok {
			output[i] = puppet
		} else {
			output[i] = br.loadPuppet(ctx, dbPuppet, nil)
		}
	}

	return output
}

func (puppet *Puppet) DefaultIntent() *appservice.IntentAPI {
	return puppet.bridge.AS.Intent(puppet.MXID)
}

func (puppet *Puppet) IntentFor(portal *Portal) *appservice.IntentAPI {
	if puppet.UserID != portal.DMUserID {
		userTeam := puppet.bridge.GetCachedUserTeamByID(puppet.UserTeamKey)
		if userTeam != nil && userTeam.User.DoublePuppetIntent != nil {
			return userTeam.User.DoublePuppetIntent
		}
	}
	return puppet.DefaultIntent()
}

const minPuppetSyncInterval = 4 * time.Hour

func (puppet *Puppet) UpdateInfoIfNecessary(ctx context.Context, source *UserTeam) {
	puppet.syncLock.Lock()
	defer puppet.syncLock.Unlock()
	if puppet.Name != "" && time.Since(puppet.lastSync) > minPuppetSyncInterval {
		return
	}
	puppet.unlockedUpdateInfo(ctx, source, nil, nil)
}

func (puppet *Puppet) UpdateInfo(ctx context.Context, userTeam *UserTeam, info *slack.User, botInfo *slack.Bot) {
	puppet.syncLock.Lock()
	defer puppet.syncLock.Unlock()
	puppet.unlockedUpdateInfo(ctx, userTeam, info, botInfo)
}

func (puppet *Puppet) unlockedUpdateInfo(ctx context.Context, userTeam *UserTeam, info *slack.User, botInfo *slack.Bot) {
	puppet.lastSync = time.Now()

	err := puppet.DefaultIntent().EnsureRegistered(ctx)
	if err != nil {
		puppet.zlog.Err(err).Msg("Failed to ensure registered")
	}

	if info == nil && botInfo == nil {
		if strings.ToLower(puppet.UserID[0:1]) == "b" {
			botInfo, err = userTeam.Client.GetBotInfo(puppet.UserID)
		} else {
			info, err = userTeam.Client.GetUserInfo(puppet.UserID)
		}
		if err != nil {
			puppet.zlog.Err(err).Object("fetch_via_id", userTeam.UserTeamMXIDKey).
				Msg("Failed to fetch info to update ghost")
			return
		}
	}

	changed := false

	if !puppet.IsBot && (strings.ToLower(puppet.UserID) == "uslackbot" || botInfo != nil) {
		puppet.IsBot = true
		changed = true
	}
	if info != nil {
		newName := puppet.bridge.Config.Bridge.FormatDisplayname(info)
		changed = puppet.UpdateName(ctx, newName) || changed
		changed = puppet.UpdateAvatar(ctx, info.Profile.ImageOriginal) || changed

		if (info.IsBot || info.IsAppUser) && !puppet.IsBot {
			puppet.IsBot = true
			changed = true
		}
	} else if botInfo != nil {
		newName := puppet.bridge.Config.Bridge.FormatBotDisplayname(botInfo)
		changed = puppet.UpdateName(ctx, newName) || changed
		changed = puppet.UpdateAvatar(ctx, botInfo.Icons.Image72) || changed
	}
	changed = puppet.UpdateContactInfo(ctx, puppet.IsBot) || changed

	if changed {
		err = puppet.Update(ctx)
		if err != nil {
			puppet.zlog.Err(err).Msg("Failed to save info to database")
		}
	}
}

func (puppet *Puppet) UpdateName(ctx context.Context, newName string) bool {
	if puppet.Name == newName && puppet.NameSet {
		return false
	}
	puppet.zlog.Debug().Str("old_name", puppet.Name).Str("new_name", newName).Msg("Updating displayname")
	puppet.Name = newName
	puppet.NameSet = false
	err := puppet.DefaultIntent().SetDisplayName(ctx, newName)
	if err != nil {
		puppet.zlog.Err(err).Msg("Failed to update displayname")
	} else {
		go puppet.updatePortalMeta(func(portal *Portal) {
			portal.UpdateNameFromPuppet(ctx, puppet)
		})
		puppet.NameSet = true
	}
	return true
}

func (puppet *Puppet) updatePortalMeta(meta func(portal *Portal)) {
	for _, portal := range puppet.bridge.GetDMPortalsWith(puppet.UserTeamKey) {
		// Get room create lock to prevent races between receiving contact info and room creation.
		portal.roomCreateLock.Lock()
		meta(portal)
		portal.roomCreateLock.Unlock()
	}
}

func (puppet *Puppet) UpdateAvatar(ctx context.Context, url string) bool {
	if puppet.Avatar == url && puppet.AvatarSet {
		return false
	}
	avatarChanged := url != puppet.Avatar
	puppet.Avatar = url
	puppet.AvatarSet = false
	puppet.AvatarMXC = id.ContentURI{}

	if puppet.Avatar != "" && (puppet.AvatarMXC.IsEmpty() || avatarChanged) {
		url, err := uploadPlainFile(ctx, puppet.DefaultIntent(), url)
		if err != nil {
			puppet.zlog.Err(err).Msg("Failed to reupload new avatar")
			return true
		}
		puppet.AvatarMXC = url
	}

	err := puppet.DefaultIntent().SetAvatarURL(ctx, puppet.AvatarMXC)
	if err != nil {
		puppet.zlog.Err(err).Msg("Failed to update avatar")
	} else {
		go puppet.updatePortalMeta(func(portal *Portal) {
			portal.UpdateAvatarFromPuppet(ctx, puppet)
		})
		puppet.AvatarSet = true
	}
	return true
}

func (puppet *Puppet) UpdateContactInfo(ctx context.Context, isBot bool) bool {
	if puppet.bridge.SpecVersions.Supports(mautrix.BeeperFeatureArbitraryProfileMeta) || puppet.ContactInfoSet {
		return false
	}
	err := puppet.DefaultIntent().BeeperUpdateProfile(ctx, map[string]any{
		"com.beeper.bridge.remote_id":      puppet.UserID,
		"com.beeper.bridge.service":        "slackgo",
		"com.beeper.bridge.network":        "slack",
		"com.beeper.bridge.is_network_bot": isBot,
	})
	if err != nil {
		puppet.zlog.Err(err).Msg("Failed to store custom contact info in profile")
		return false
	} else {
		puppet.ContactInfoSet = true
		return true
	}
}
