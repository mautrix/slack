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

package database

import (
	"database/sql"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
)

type User struct {
	db  *Database
	log log.Logger

	MXID id.UserID
	ID   string

	ManagementRoom id.RoomID

	Token string
}

func (u *User) Scan(row dbutil.Scannable) *User {
	var token sql.NullString
	var discordID sql.NullString

	err := row.Scan(&u.MXID, &discordID, &u.ManagementRoom, &token)
	if err != nil {
		if err != sql.ErrNoRows {
			u.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	if token.Valid {
		u.Token = token.String
	}

	if discordID.Valid {
		u.ID = discordID.String
	}

	return u
}

func (u *User) Insert() {
	query := "INSERT INTO \"user\"" +
		" (mxid, id, management_room, token)" +
		" VALUES ($1, $2, $3, $4);"

	var token sql.NullString
	var discordID sql.NullString

	if u.Token != "" {
		token.String = u.Token
		token.Valid = true
	}

	if u.ID != "" {
		discordID.String = u.ID
		discordID.Valid = true
	}

	_, err := u.db.Exec(query, u.MXID, discordID, u.ManagementRoom, token)

	if err != nil {
		u.log.Warnfln("Failed to insert %s: %v", u.MXID, err)
	}
}

func (u *User) Update() {
	query := "UPDATE \"user\" SET" +
		" id=$1, management_room=$2, token=$3" +
		" WHERE mxid=$4;"

	var token sql.NullString
	var discordID sql.NullString

	if u.Token != "" {
		token.String = u.Token
		token.Valid = true
	}

	if u.ID != "" {
		discordID.String = u.ID
		discordID.Valid = true
	}

	_, err := u.db.Exec(query, discordID, u.ManagementRoom, token, u.MXID)

	if err != nil {
		u.log.Warnfln("Failed to update %q: %v", u.MXID, err)
	}
}
