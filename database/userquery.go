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
	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

type UserQuery struct {
	db  *Database
	log log.Logger
}

func (uq *UserQuery) New() *User {
	return &User{
		db:    uq.db,
		log:   uq.log,
		Teams: map[string]*UserTeam{},
	}
}

func (uq *UserQuery) GetByMXID(userID id.UserID) *User {
	query := `SELECT mxid, management_room, space_room FROM "user" WHERE mxid=$1`
	row := uq.db.QueryRow(query, userID)
	if row == nil {
		return nil
	}

	return uq.New().Scan(row)
}

func (uq *UserQuery) GetBySlackID(teamID, userID string) *User {
	query := `SELECT u.mxid, u.management_room, u.space_room FROM "user" u` +
		` INNER JOIN user_team ut ON u.mxid = ut.mxid` +
		` WHERE ut.team_id=$1 AND ut.slack_id=$2`
	row := uq.db.QueryRow(query, teamID, userID)
	if row == nil {
		return nil
	}

	return uq.New().Scan(row)
}

func (uq *UserQuery) GetAll() []*User {
	rows, err := uq.db.Query(`SELECT mxid, management_room, space_room FROM "user"`)
	if err != nil || rows == nil {
		return nil
	}

	defer rows.Close()

	users := []*User{}
	for rows.Next() {
		users = append(users, uq.New().Scan(rows))
	}

	return users
}
