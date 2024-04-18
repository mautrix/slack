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
	_ "embed"
	"sync"

	"go.mau.fi/util/configupgrade"

	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/commands"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-slack/config"
	"go.mau.fi/mautrix-slack/database"
)

// Information to find out exactly which commit the bridge was built from.
// These are filled at build time with the -X linker flag.
var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

//go:embed example-config.yaml
var ExampleConfig string

type SlackBridge struct {
	bridge.Bridge

	Config *config.Config
	DB     *database.Database

	provisioning *ProvisioningAPI

	//BackfillQueue          *BackfillQueue
	//historySyncLoopStarted bool

	usersByMXID     map[id.UserID]*User
	managementRooms map[id.RoomID]*User
	userTeamsByID   map[database.UserTeamKey]*UserTeam
	teamsByMXID     map[id.RoomID]*Team
	teamsByID       map[string]*Team
	userAndTeamLock sync.Mutex

	portalsByMXID map[id.RoomID]*Portal
	portalsByID   map[database.PortalKey]*Portal
	portalsLock   sync.Mutex

	puppets     map[database.UserTeamKey]*Puppet
	puppetsLock sync.Mutex
}

func (br *SlackBridge) GetExampleConfig() string {
	return ExampleConfig
}

func (br *SlackBridge) GetConfigPtr() interface{} {
	br.Config = &config.Config{
		BaseConfig: &br.Bridge.Config,
	}
	br.Config.BaseConfig.Bridge = &br.Config.Bridge
	return br.Config
}

func (br *SlackBridge) Init() {
	br.CommandProcessor = commands.NewProcessor(&br.Bridge)
	br.RegisterCommands()

	br.DB = database.New(br.Bridge.DB)
}

func (br *SlackBridge) Start() {
	if br.Config.Bridge.Provisioning.SharedSecret != "disable" {
		br.provisioning = newProvisioningAPI(br)
	}

	//br.BackfillQueue = &BackfillQueue{
	//	BackfillQuery:   br.DB.Backfill,
	//	reCheckChannels: []chan bool{},
	//	log:             br.Log.Sub("BackfillQueue"),
	//}

	br.WaitWebsocketConnected()
	go br.startUsers()
}

func (br *SlackBridge) Stop() {
	for _, userTeam := range br.userTeamsByID {
		userTeam.Log.Debug().Msg("Disconnecting team")
		userTeam.Disconnect()
	}
}

func (br *SlackBridge) GetIPortal(mxid id.RoomID) bridge.Portal {
	p := br.GetPortalByMXID(mxid)
	if p == nil {
		return nil
	}
	return p
}

func (br *SlackBridge) GetIUser(mxid id.UserID, create bool) bridge.User {
	p := br.GetUserByMXID(mxid)
	if p == nil {
		return nil
	}
	return p
}

func (br *SlackBridge) IsGhost(mxid id.UserID) bool {
	_, isGhost := br.ParsePuppetMXID(mxid)
	return isGhost
}

func (br *SlackBridge) GetIGhost(mxid id.UserID) bridge.Ghost {
	p := br.GetPuppetByMXID(mxid)
	if p == nil {
		return nil
	}
	return p
}

func (br *SlackBridge) CreatePrivatePortal(id id.RoomID, user bridge.User, ghost bridge.Ghost) {
	//TODO implement
}

func main() {
	br := &SlackBridge{
		usersByMXID:     make(map[id.UserID]*User),
		managementRooms: make(map[id.RoomID]*User),
		userTeamsByID:   make(map[database.UserTeamKey]*UserTeam),
		teamsByMXID:     make(map[id.RoomID]*Team),
		teamsByID:       make(map[string]*Team),

		portalsByMXID: make(map[id.RoomID]*Portal),
		portalsByID:   make(map[database.PortalKey]*Portal),

		puppets: make(map[database.UserTeamKey]*Puppet),
	}
	br.Bridge = bridge.Bridge{
		Name:              "mautrix-slack",
		URL:               "https://github.com/mautrix/slack",
		Description:       "A Matrix-Slack puppeting bridge.",
		Version:           "0.1.0",
		ProtocolName:      "Slack",
		BeeperServiceName: "slackgo",
		BeeperNetworkName: "slack",

		CryptoPickleKey: "maunium.net/go/mautrix-whatsapp",

		ConfigUpgrader: &configupgrade.StructUpgrader{
			SimpleUpgrader: configupgrade.SimpleUpgrader(config.DoUpgrade),
			Blocks:         config.SpacedBlocks,
			Base:           ExampleConfig,
		},

		Child: br,
	}
	br.InitVersion(Tag, Commit, BuildTime)

	br.Main()
}
