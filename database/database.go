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

package database

import (
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/util/dbutil"

	"go.mau.fi/mautrix-slack/database/upgrades"
)

type Database struct {
	*dbutil.Database

	User       *UserQuery
	UserTeam   *UserTeamQuery
	Portal     *PortalQuery
	Puppet     *PuppetQuery
	Message    *MessageQuery
	Reaction   *ReactionQuery
	TeamPortal *TeamPortalQuery
	Backfill   *BackfillQuery
	Emoji      *EmojiQuery
}

func New(db *dbutil.Database) *Database {
	db.UpgradeTable = upgrades.Table
	return &Database{
		Database: db,

		User:       &UserQuery{dbutil.MakeQueryHelper(db, newUser)},
		UserTeam:   &UserTeamQuery{dbutil.MakeQueryHelper(db, newUserTeam)},
		Portal:     &PortalQuery{dbutil.MakeQueryHelper(db, newPortal)},
		Puppet:     &PuppetQuery{dbutil.MakeQueryHelper(db, newPuppet)},
		Message:    &MessageQuery{dbutil.MakeQueryHelper(db, newMessage)},
		Reaction:   &ReactionQuery{dbutil.MakeQueryHelper(db, newReaction)},
		TeamPortal: &TeamPortalQuery{dbutil.MakeQueryHelper(db, newTeamPortal)},
		Backfill:   &BackfillQuery{dbutil.MakeQueryHelper(db, newBackfill)},
		Emoji:      &EmojiQuery{dbutil.MakeQueryHelper(db, newEmoji)},
	}
}
