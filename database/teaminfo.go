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

type TeamInfoQuery struct {
	db  *Database
	log log.Logger
}

func (tiq *TeamInfoQuery) New() *TeamInfo {
	return &TeamInfo{
		db:  tiq.db,
		log: tiq.log,
	}
}

func (tiq *TeamInfoQuery) GetBySlackTeam(team string) *TeamInfo {
	query := `SELECT team_id, team_domain, team_url, team_name, avatar, avatar_url, space_room, name_set, avatar_set FROM team_info WHERE team_id=$1`

	row := tiq.db.QueryRow(query, team)
	if row == nil {
		return nil
	}

	return tiq.New().Scan(row)
}

func (tiq *TeamInfoQuery) GetByMXID(mxid id.RoomID) *TeamInfo {
	query := `SELECT team_id, team_domain, team_url, team_name, avatar, avatar_url, space_room, name_set, avatar_set FROM team_info WHERE space_room=$1`

	row := tiq.db.QueryRow(query, mxid)
	if row == nil {
		return nil
	}

	return tiq.New().Scan(row)
}

type TeamInfo struct {
	db  *Database
	log log.Logger

	TeamID     string
	TeamDomain string
	TeamUrl    string
	TeamName   string
	Avatar     string
	AvatarUrl  id.ContentURI
	SpaceRoom  id.RoomID
	NameSet    bool
	AvatarSet  bool
}

func (ti *TeamInfo) Scan(row dbutil.Scannable) *TeamInfo {
	var teamDomain sql.NullString
	var teamUrl sql.NullString
	var teamName sql.NullString
	var avatar sql.NullString
	var avatarUrl sql.NullString
	var spaceRoom sql.NullString

	err := row.Scan(&ti.TeamID, &teamDomain, &teamUrl, &teamName, &avatar, &avatarUrl, &spaceRoom, &ti.NameSet, &ti.AvatarSet)
	if err != nil {
		if err != sql.ErrNoRows {
			ti.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	if teamDomain.Valid {
		ti.TeamDomain = teamDomain.String
	}
	if teamUrl.Valid {
		ti.TeamUrl = teamUrl.String
	}
	if teamName.Valid {
		ti.TeamName = teamName.String
	}
	if avatar.Valid {
		ti.Avatar = avatar.String
	}
	if avatarUrl.Valid {
		ti.AvatarUrl, _ = id.ParseContentURI(avatarUrl.String)
	}
	if spaceRoom.Valid {
		ti.SpaceRoom = id.RoomID(spaceRoom.String)
	}

	return ti
}

func (ti *TeamInfo) Upsert() {
	query := `
		INSERT INTO team_info (team_id, team_domain, team_url, team_name, avatar, avatar_url, space_room, name_set, avatar_set)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (team_id) DO UPDATE
			SET team_domain=excluded.team_domain,
			team_url=excluded.team_url,
			team_name=excluded.team_name,
			avatar=excluded.avatar,
			avatar_url=excluded.avatar_url,
			space_room=excluded.space_room,
			name_set=excluded.name_set,
			avatar_set=excluded.avatar_set
	`

	teamDomain := sqlNullString(ti.TeamDomain)
	teamUrl := sqlNullString(ti.TeamUrl)
	teamName := sqlNullString(ti.TeamName)
	avatar := sqlNullString(ti.Avatar)
	avatarUrl := sqlNullString(ti.AvatarUrl.String())

	_, err := ti.db.Exec(query, ti.TeamID, teamDomain, teamUrl, teamName, avatar, avatarUrl, ti.NameSet, ti.AvatarSet)

	if err != nil {
		ti.log.Warnfln("Failed to upsert team %s: %v", ti.TeamID, err)
	}
}
