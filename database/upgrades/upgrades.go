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

package upgrades

import (
	"embed"
	"errors"

	"maunium.net/go/mautrix/util/dbutil"
)

var Table dbutil.UpgradeTable

//go:embed *.sql
var rawUpgrades embed.FS

func init() {
	Table.Register(-1, 7, 0, "Unsupported version", false, func(tx dbutil.Execable, database *dbutil.Database) error {
		return errors.New("data from old dev version of mautrix-slack not supported, please delete the bridge database and set up bridge again")
	})
	Table.RegisterFS(rawUpgrades)
}
