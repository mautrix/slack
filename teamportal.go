package main

import (
	"errors"
	"fmt"
	"sync"

	"github.com/slack-go/slack"

	"go.mau.fi/mautrix-slack/database"
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type Team struct {
	*database.TeamInfo

	bridge *SlackBridge
	log    log.Logger

	roomCreateLock sync.Mutex
}

func (br *SlackBridge) loadTeam(dbTeam *database.TeamInfo, id string, createIfNotExist bool) *Team {
	if dbTeam == nil {
		if id == "" || !createIfNotExist {
			return nil
		}

		dbTeam = br.DB.TeamInfo.New()
		dbTeam.TeamID = id
		dbTeam.Upsert()
	}

	team := br.NewTeam(dbTeam)

	br.teamsByID[team.TeamID] = team
	if team.SpaceRoom != "" {
		br.teamsByMXID[team.SpaceRoom] = team
	}

	return team
}

func (br *SlackBridge) GetTeamByMXID(mxid id.RoomID) *Team {
	br.teamsLock.Lock()
	defer br.teamsLock.Unlock()

	portal, ok := br.teamsByMXID[mxid]
	if !ok {
		return br.loadTeam(br.DB.TeamInfo.GetByMXID(mxid), "", false)
	}

	return portal
}

func (br *SlackBridge) GetTeamByID(id string, createIfNotExist bool) *Team {
	br.teamsLock.Lock()
	defer br.teamsLock.Unlock()

	team, ok := br.teamsByID[id]
	if !ok {
		return br.loadTeam(br.DB.TeamInfo.GetBySlackTeam(id), id, createIfNotExist)
	}

	return team
}

// func (br *SlackBridge) GetAllTeams() []*Team {
// 	return br.dbTeamsToTeams(br.DB.TeamInfo.GetAll())
// }

// func (br *SlackBridge) dbTeamsToTeams(dbTeams []*database.TeamInfo) []*Team {
// 	br.teamsLock.Lock()
// 	defer br.teamsLock.Unlock()

// 	output := make([]*Team, len(dbTeams))
// 	for index, dbTeam := range dbTeams {
// 		if dbTeam == nil {
// 			continue
// 		}

// 		team, ok := br.teamsByID[dbTeam.TeamID]
// 		if !ok {
// 			team = br.loadTeam(dbTeam, "", false)
// 		}

// 		output[index] = team
// 	}

// 	return output
// }

func (br *SlackBridge) NewTeam(dbTeam *database.TeamInfo) *Team {
	team := &Team{
		TeamInfo: dbTeam,
		bridge:   br,
		log:      br.Log.Sub(fmt.Sprintf("Team/%s", dbTeam.TeamID)),
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
			ID:          team.TeamID,
			DisplayName: team.TeamName,
			AvatarURL:   team.AvatarUrl.CUString(),
		},
	}
	bridgeInfoStateKey := fmt.Sprintf("fi.mau.slack://slackgo/%s", team.TeamID)
	return bridgeInfoStateKey, bridgeInfo
}

func (team *Team) UpdateBridgeInfo() {
	if len(team.SpaceRoom) == 0 {
		team.log.Debugln("Not updating bridge info: no Matrix room created")
		return
	}
	team.log.Debugln("Updating bridge info...")
	stateKey, content := team.getBridgeInfo()
	_, err := team.bridge.Bot.SendStateEvent(team.SpaceRoom, event.StateBridge, stateKey, content)
	if err != nil {
		team.log.Warnln("Failed to update m.bridge:", err)
	}
	// TODO remove this once https://github.com/matrix-org/matrix-doc/pull/2346 is in spec
	_, err = team.bridge.Bot.SendStateEvent(team.SpaceRoom, event.StateHalfShotBridge, stateKey, content)
	if err != nil {
		team.log.Warnln("Failed to update uk.half-shot.bridge:", err)
	}
}

func (team *Team) CreateMatrixRoom(user *User, meta *slack.TeamInfo) error {
	team.roomCreateLock.Lock()
	defer team.roomCreateLock.Unlock()
	if team.SpaceRoom != "" {
		return nil
	}
	team.log.Infoln("Creating Matrix room for team")
	team.UpdateInfo(user, meta)

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

	if !team.AvatarUrl.IsEmpty() {
		initialState = append(initialState, &event.Event{
			Type: event.StateRoomAvatar,
			Content: event.Content{Parsed: &event.RoomAvatarEventContent{
				URL: team.AvatarUrl,
			}},
		})
	}

	creationContent := map[string]interface{}{
		"type": event.RoomTypeSpace,
	}
	if !team.bridge.Config.Bridge.FederateRooms {
		creationContent["m.federate"] = false
	}

	resp, err := team.bridge.Bot.CreateRoom(&mautrix.ReqCreateRoom{
		Visibility:      "private",
		Name:            team.TeamName,
		Preset:          "private_chat",
		InitialState:    initialState,
		CreationContent: creationContent,
	})
	if err != nil {
		team.log.Warnln("Failed to create room:", err)
		return err
	}

	team.SpaceRoom = resp.RoomID
	team.NameSet = true
	team.AvatarSet = !team.AvatarUrl.IsEmpty()
	team.Upsert()
	team.bridge.teamsLock.Lock()
	team.bridge.teamsByMXID[team.SpaceRoom] = team
	team.bridge.teamsLock.Unlock()
	team.log.Infoln("Matrix room created:", team.SpaceRoom)

	user.ensureInvited(nil, team.SpaceRoom, false)

	return nil
}

func (team *Team) UpdateInfo(source *User, meta *slack.TeamInfo) (changed bool) {
	changed = team.UpdateName(meta) || changed
	changed = team.UpdateAvatar(meta) || changed
	if team.TeamDomain != meta.Domain {
		team.TeamDomain = meta.Domain
		changed = true
	}
	if team.TeamUrl != meta.URL {
		team.TeamUrl = meta.URL
		changed = true
	}
	if changed {
		team.UpdateBridgeInfo()
		team.Upsert()
	}
	return
}

func (team *Team) UpdateName(meta *slack.TeamInfo) (changed bool) {
	if team.TeamName != meta.Name {
		team.log.Debugfln("Updating name %q -> %q", team.TeamName, meta.Name)
		team.TeamName = meta.Name
		changed = true
	}
	if team.SpaceRoom != "" {
		_, err := team.bridge.Bot.SetRoomName(team.SpaceRoom, team.TeamName)
		if err != nil {
			team.log.Warnln("Failed to update room name: %s", err)
		} else {
			team.NameSet = true
		}
	}
	return
}

func (team *Team) UpdateAvatar(meta *slack.TeamInfo) (changed bool) {
	if meta.Icon["image_default"] != nil && meta.Icon["image_default"] == true && team.Avatar != "" {
		team.Avatar = ""
		team.AvatarUrl = id.MustParseContentURI("")
		changed = true
	} else if meta.Icon["image_default"] != nil && meta.Icon["image_default"] == false && meta.Icon["image_230"] != nil && team.Avatar != meta.Icon["image_230"] {
		avatar, err := uploadPlainFile(team.bridge.AS.BotIntent(), meta.Icon["image_230"].(string))
		if err != nil {
			team.log.Warnfln("Error uploading new team avatar for team %s: %v", team.TeamID, err)
		} else {
			team.Avatar = meta.Icon["image_230"].(string)
			team.AvatarUrl = avatar
			changed = true
		}
	}
	if team.SpaceRoom != "" {
		_, err := team.bridge.Bot.SetRoomAvatar(team.SpaceRoom, team.AvatarUrl)
		if err != nil {
			team.log.Warnln("Failed to update room avatar:", err)
		} else {
			team.AvatarSet = true
		}
	}
	return
}

func (team *Team) cleanup() {
	if team.SpaceRoom == "" {
		return
	}
	intent := team.bridge.Bot
	if team.bridge.SpecVersions.UnstableFeatures["com.beeper.room_yeeting"] {
		err := intent.BeeperDeleteRoom(team.SpaceRoom)
		if err == nil || errors.Is(err, mautrix.MNotFound) {
			return
		}
		team.log.Warnfln("Failed to delete %s using hungryserv yeet endpoint, falling back to normal behavior: %v", team.SpaceRoom, err)
	}
	team.bridge.cleanupRoom(intent, team.SpaceRoom, false, team.log)
}

func (team *Team) RemoveMXID() {
	team.bridge.teamsLock.Lock()
	defer team.bridge.teamsLock.Unlock()
	if team.SpaceRoom == "" {
		return
	}
	delete(team.bridge.teamsByMXID, team.SpaceRoom)
	team.SpaceRoom = ""
	team.AvatarSet = false
	team.NameSet = false
	team.Upsert()
}

// func (team *Team) Delete() {
// 	team.TeamInfo.Delete()
// 	team.bridge.teamsLock.Lock()
// 	delete(team.bridge.teamsByID, team.TeamID)
// 	if team.SpaceRoom != "" {
// 		delete(team.bridge.teamsByMXID, team.SpaceRoom)
// 	}
// 	team.bridge.teamsLock.Unlock()

// }
