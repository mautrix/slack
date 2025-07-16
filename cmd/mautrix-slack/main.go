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
	"net/http"

	"github.com/lib/pq"
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

	"go.mau.fi/mautrix-slack/pkg/connector"
	"go.mau.fi/mautrix-slack/pkg/connector/slackdb"
)

var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

var c = &connector.SlackConnector{}
var m = mxmain.BridgeMain{
	Name:        "mautrix-slack",
	Description: "A Matrix-Slack puppeting bridge",
	URL:         "https://github.com/mautrix/slack",
	Version:     "0.2.2",
	Connector:   c,
}

func main() {
	slackdb.PostgresArrayWrapper = pq.Array
	m.PostInit = func() {
		m.CheckLegacyDB(
			16,
			"c565641",
			"v0.1.0",
			m.LegacyMigrateSimple(legacyMigrateRenameTables, legacyMigrateCopyData, 14),
			true,
		)
	}
	m.PostStart = func() {
		if m.Matrix.Provisioning != nil {
			m.Matrix.Provisioning.Router.HandleFunc("/v1/ping", legacyProvPing).Methods(http.MethodGet)
			m.Matrix.Provisioning.Router.HandleFunc("/v1/login", legacyProvLogin).Methods(http.MethodPost)
			m.Matrix.Provisioning.Router.HandleFunc("/v1/logout", legacyProvLogout).Methods(http.MethodPost)
		}
	}
	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}
