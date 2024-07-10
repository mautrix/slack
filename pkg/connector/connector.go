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
	"context"

	"maunium.net/go/mautrix/bridgev2"

	"go.mau.fi/mautrix-slack/pkg/connector/slackdb"
	"go.mau.fi/mautrix-slack/pkg/msgconv"
)

type SlackConnector struct {
	br      *bridgev2.Bridge
	Config  Config
	DB      *slackdb.SlackDB
	MsgConv *msgconv.MessageConverter
}

var (
	_ bridgev2.NetworkConnector      = (*SlackConnector)(nil)
	_ bridgev2.MaxFileSizeingNetwork = (*SlackConnector)(nil)
)

func (s *SlackConnector) Init(bridge *bridgev2.Bridge) {
	s.br = bridge
	s.DB = slackdb.New(bridge.DB.Database, bridge.Log.With().Str("db_section", "slack").Logger())
	s.MsgConv = msgconv.New(bridge)
}

func (s *SlackConnector) SetMaxFileSize(maxSize int64) {
	s.MsgConv.MaxFileSize = int(maxSize)
}

func (s *SlackConnector) Start(ctx context.Context) error {
	return s.DB.Upgrade(ctx)
}

func (s *SlackConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:      "Slack",
		NetworkURL:       "https://slack.com",
		NetworkIcon:      "mxc://maunium.net/pVtzLmChZejGxLqmXtQjFxem",
		NetworkID:        "slack",
		BeeperBridgeType: "slackgo",
		DefaultPort:      29335,
	}
}
