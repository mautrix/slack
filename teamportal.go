// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2022 Max Sandholm
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
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/slack-go/slack"

	"go.mau.fi/mautrix-slack/database"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type Team struct {
	*database.TeamPortal

	bridge *SlackBridge
	log    zerolog.Logger

	roomCreateLock  sync.Mutex
	emojiLock       sync.Mutex
	lastEmojiResync time.Time
}

func (br *SlackBridge) loadTeam(ctx context.Context, dbTeam *database.TeamPortal, teamID *string) *Team {
	if dbTeam == nil {
		if teamID == nil {
			return nil
		}

		dbTeam = br.DB.TeamPortal.New()
		dbTeam.ID = *teamID
		err := dbTeam.Insert(ctx)
		if err != nil {
			br.ZLog.Err(err).Str("team_id", *teamID).Msg("Failed to insert new team")
			return nil
		}
	}

	team := br.newTeam(dbTeam)

	br.teamsByID[team.ID] = team
	if team.MXID != "" {
		br.teamsByMXID[team.MXID] = team
	}

	return team
}

func (br *SlackBridge) newTeam(dbTeam *database.TeamPortal) *Team {
	team := &Team{
		TeamPortal: dbTeam,
		bridge:     br,
	}
	team.updateLogger()
	return team
}

func (team *Team) updateLogger() {
	withLog := team.bridge.ZLog.With().Str("team_id", team.ID)
	if team.MXID != "" {
		withLog = withLog.Stringer("space_room_id", team.MXID)
	}
	team.log = withLog.Logger()
}

func (br *SlackBridge) GetTeamByMXID(mxid id.RoomID) *Team {
	br.userAndTeamLock.Lock()
	defer br.userAndTeamLock.Unlock()

	portal, ok := br.teamsByMXID[mxid]
	if !ok {
		ctx := context.TODO()
		dbTeam, err := br.DB.TeamPortal.GetByMXID(ctx, mxid)
		if err != nil {
			br.ZLog.Err(err).Str("mxid", mxid.String()).Msg("Failed to get team by mxid")
			return nil
		}
		return br.loadTeam(ctx, dbTeam, nil)
	}

	return portal
}

func (br *SlackBridge) GetTeamByID(id string) *Team {
	br.userAndTeamLock.Lock()
	defer br.userAndTeamLock.Unlock()
	return br.unlockedGetTeamByID(id, false)
}

func (br *SlackBridge) unlockedGetTeamByID(id string, onlyIfExists bool) *Team {
	team, ok := br.teamsByID[id]
	if !ok {
		ctx := context.TODO()
		dbTeam, err := br.DB.TeamPortal.GetBySlackID(ctx, id)
		if err != nil {
			br.ZLog.Err(err).Str("team_id", id).Msg("Failed to get team by ID")
			return nil
		}
		idPtr := &id
		if onlyIfExists {
			idPtr = nil
		}
		return br.loadTeam(ctx, dbTeam, idPtr)
	}

	return team
}

func (team *Team) getBridgeInfo() (string, event.BridgeEventContent) {
	bridgeInfo := event.BridgeEventContent{
		BridgeBot: team.bridge.Bot.UserID,
		Creator:   team.bridge.Bot.UserID,
		Protocol: event.BridgeInfoSection{
			ID:          "slackgo",
			DisplayName: "Slack",
			AvatarURL:   team.bridge.Config.AppService.Bot.ParsedAvatar.CUString(),
			ExternalURL: "https://slack.com/",
		},
		Channel: event.BridgeInfoSection{
			ID:          team.ID,
			DisplayName: team.Name,
			AvatarURL:   team.AvatarMXC.CUString(),
		},
	}
	bridgeInfoStateKey := fmt.Sprintf("fi.mau.slack://slackgo/%s", team.ID)
	return bridgeInfoStateKey, bridgeInfo
}

func (team *Team) UpdateBridgeInfo(ctx context.Context) {
	if len(team.MXID) == 0 {
		team.log.Debug().Msg("Not updating bridge info: no Matrix room created")
		return
	}
	team.log.Debug().Msg("Updating bridge info")
	stateKey, content := team.getBridgeInfo()
	_, err := team.bridge.Bot.SendStateEvent(ctx, team.MXID, event.StateBridge, stateKey, content)
	if err != nil {
		team.log.Err(err).Msg("Failed to update m.bridge event")
	}
	// TODO remove this once https://github.com/matrix-org/matrix-doc/pull/2346 is in spec
	_, err = team.bridge.Bot.SendStateEvent(ctx, team.MXID, event.StateHalfShotBridge, stateKey, content)
	if err != nil {
		team.log.Err(err).Msg("Failed to update uk.half-shot.bridge event")
	}
}

func (team *Team) GetCachedUserByID(userID string) *UserTeam {
	return team.bridge.GetCachedUserTeamByID(database.UserTeamKey{
		TeamID: team.ID,
		UserID: userID,
	})
}

func (team *Team) GetCachedUserByMXID(userID id.UserID) *UserTeam {
	team.bridge.userAndTeamLock.Lock()
	defer team.bridge.userAndTeamLock.Unlock()
	user, ok := team.bridge.usersByMXID[userID]
	if !ok {
		return nil
	}
	return user.teams[team.ID]
}

func (team *Team) GetPuppetByID(userID string) *Puppet {
	return team.bridge.GetPuppetByID(database.UserTeamKey{
		TeamID: team.ID,
		UserID: userID,
	})
}

func (team *Team) GetPortalByID(channelID string) *Portal {
	return team.bridge.GetPortalByID(database.PortalKey{
		TeamID:    team.ID,
		ChannelID: channelID,
	})
}

func (team *Team) CreateMatrixRoom(ctx context.Context) error {
	team.roomCreateLock.Lock()
	defer team.roomCreateLock.Unlock()
	if team.MXID != "" {
		return nil
	}
	team.log.Info().Msg("Creating Matrix space for team")

	bridgeInfoStateKey, bridgeInfo := team.getBridgeInfo()

	initialState := []*event.Event{{
		Type:     event.StateBridge,
		Content:  event.Content{Parsed: bridgeInfo},
		StateKey: &bridgeInfoStateKey,
	}, {
		// TODO remove this once https://github.com/matrix-org/matrix-doc/pull/2346 is in spec
		Type:     event.StateHalfShotBridge,
		Content:  event.Content{Parsed: bridgeInfo},
		StateKey: &bridgeInfoStateKey,
	}}

	if !team.AvatarMXC.IsEmpty() {
		initialState = append(initialState, &event.Event{
			Type: event.StateRoomAvatar,
			Content: event.Content{Parsed: &event.RoomAvatarEventContent{
				URL: team.AvatarMXC,
			}},
		})
	}

	creationContent := map[string]any{
		"type": event.RoomTypeSpace,
	}
	if !team.bridge.Config.Bridge.FederateRooms {
		creationContent["m.federate"] = false
	}

	resp, err := team.bridge.Bot.CreateRoom(ctx, &mautrix.ReqCreateRoom{
		Visibility:      "private",
		Name:            team.Name,
		Preset:          "private_chat",
		InitialState:    initialState,
		CreationContent: creationContent,
	})
	if err != nil {
		team.log.Err(err).Msg("Failed to create Matrix space")
		return err
	}

	team.bridge.userAndTeamLock.Lock()
	team.MXID = resp.RoomID
	team.NameSet = true
	team.AvatarSet = !team.AvatarMXC.IsEmpty()
	team.bridge.teamsByMXID[team.MXID] = team
	team.bridge.userAndTeamLock.Unlock()
	team.updateLogger()
	team.log.Info().Msg("Matrix space created")

	err = team.Update(ctx)
	if err != nil {
		team.log.Err(err).Msg("Failed to save team after creating Matrix space")
	}

	return nil
}

func (team *Team) UpdateInfo(ctx context.Context, meta *slack.TeamInfo) (changed bool) {
	changed = team.UpdateName(ctx, meta) || changed
	changed = team.UpdateAvatar(ctx, meta) || changed
	if team.Domain != meta.Domain {
		team.Domain = meta.Domain
		changed = true
	}
	if team.URL != meta.URL {
		team.URL = meta.URL
		changed = true
	}
	if changed {
		team.UpdateBridgeInfo(ctx)
		err := team.Update(ctx)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to save team after updating info")
		}
	}
	return
}

func (team *Team) UpdateName(ctx context.Context, meta *slack.TeamInfo) bool {
	newName := team.bridge.Config.Bridge.FormatTeamName(meta)
	if team.Name == newName && team.NameSet {
		return false
	}
	team.log.Debug().Str("old_name", team.Name).Str("new_name", newName).Msg("Updating name")
	team.Name = newName
	team.NameSet = false
	if team.MXID != "" {
		_, err := team.bridge.Bot.SetRoomName(ctx, team.MXID, team.Name)
		if err != nil {
			team.log.Err(err).Msg("Failed to update room name")
		} else {
			team.NameSet = true
		}
	}
	return true
}

func (team *Team) UpdateAvatar(ctx context.Context, meta *slack.TeamInfo) (changed bool) {
	if meta.Icon["image_default"] != nil && meta.Icon["image_default"] == true && team.Avatar != "" {
		team.AvatarSet = false
		team.Avatar = ""
		team.AvatarMXC = id.MustParseContentURI("")
		changed = true
	} else if meta.Icon["image_default"] != nil && meta.Icon["image_default"] == false && meta.Icon["image_230"] != nil && team.Avatar != meta.Icon["image_230"] {
		avatar, err := uploadPlainFile(ctx, team.bridge.AS.BotIntent(), meta.Icon["image_230"].(string))
		if err != nil {
			team.log.Err(err).Msg("Failed to reupload team avatar")
		} else {
			team.Avatar = meta.Icon["image_230"].(string)
			team.AvatarMXC = avatar
			team.AvatarSet = false
			changed = true
		}
	}
	if team.MXID != "" && (changed || !team.AvatarSet) {
		_, err := team.bridge.Bot.SetRoomAvatar(ctx, team.MXID, team.AvatarMXC)
		if err != nil {
			team.log.Err(err).Msg("Failed to update room avatar")
		} else {
			team.AvatarSet = true
		}
	}
	return
}

func (team *Team) Cleanup(ctx context.Context) {
	if team.MXID == "" {
		return
	}
	intent := team.bridge.Bot
	team.bridge.cleanupRoom(ctx, intent, team.MXID, false)
}

func (team *Team) RemoveMXID(ctx context.Context) {
	team.bridge.userAndTeamLock.Lock()
	defer team.bridge.userAndTeamLock.Unlock()
	if team.MXID == "" {
		return
	}
	delete(team.bridge.teamsByMXID, team.MXID)
	team.MXID = ""
	team.AvatarSet = false
	team.NameSet = false
	err := team.Update(ctx)
	if err != nil {
		team.log.Err(err).Msg("Failed to save team after removing mxid")
	}
}

// func (team *Team) Delete() {
// 	team.TeamPortal.Delete()
// 	team.bridge.teamsLock.Lock()
// 	delete(team.bridge.teamsByID, team.ID)
// 	if team.MXID != "" {
// 		delete(team.bridge.teamsByMXID, team.MXID)
// 	}
// 	team.bridge.teamsLock.Unlock()

// }

func (team *Team) AddPortalToSpace(ctx context.Context, portal *Portal) bool {
	if len(team.MXID) == 0 {
		team.log.Error().Msg("Tried to add portal to team that has no matrix ID")
		if portal.InSpace {
			portal.InSpace = false
			return true
		}
		return false
	} else if portal.InSpace {
		return false
	}

	_, err := team.bridge.Bot.SendStateEvent(ctx, team.MXID, event.StateSpaceChild, portal.MXID.String(), &event.SpaceChildEventContent{
		Via: []string{team.bridge.AS.HomeserverDomain},
	})
	if err != nil {
		team.log.Err(err).Stringer("room_mxid", portal.MXID).Msg("Failed to add portal to team space")
		portal.InSpace = false
	} else {
		portal.InSpace = true
	}
	return true
}
