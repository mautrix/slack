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

	"github.com/slack-go/slack"
)

type UserTeamQuery struct {
	db  *Database
	log log.Logger
}

func (utq *UserTeamQuery) New() *UserTeam {
	return &UserTeam{
		db:  utq.db,
		log: utq.log,
	}
}

func (utq *UserTeamQuery) GetBySlackTeam(userID id.UserID, email, team string) *UserTeam {
	query := `SELECT mxid, slack_email, slack_id, team_name, team_id, token, cookie_token FROM user_team WHERE mxid=$1 AND slack_email=$2 AND team_name=$3`

	row := utq.db.QueryRow(query, userID, email, team)
	if row == nil {
		return nil
	}

	return utq.New().Scan(row)
}

func (utq *UserTeamQuery) GetAllByMXIDWithToken(userID id.UserID) []*UserTeam {
	query := `SELECT mxid, slack_email, slack_id, team_name, team_id, token, cookie_token FROM user_team WHERE mxid=$1 AND token IS NOT NULL`

	rows, err := utq.db.Query(query, userID)
	if err != nil || rows == nil {
		return nil
	}

	defer rows.Close()

	tokens := []*UserTeam{}
	for rows.Next() {
		tokens = append(tokens, utq.New().Scan(rows))
	}

	return tokens
}

func (utq *UserTeamQuery) GetAllBySlackTeamID(teamID string) []*UserTeam {
	query := `SELECT mxid, slack_email, slack_id, team_name, team_id, token, cookie_token FROM user_team WHERE team_id=$1`

	rows, err := utq.db.Query(query, teamID)
	if err != nil || rows == nil {
		return nil
	}

	defer rows.Close()

	tokens := []*UserTeam{}
	for rows.Next() {
		tokens = append(tokens, utq.New().Scan(rows))
	}

	return tokens
}

type UserTeamKey struct {
	MXID    id.UserID
	SlackID string
	TeamID  string
}

type UserTeam struct {
	db  *Database
	log log.Logger

	Key UserTeamKey

	SlackEmail string
	TeamName   string

	Token       string
	CookieToken string

	Client *slack.Client
	RTM    *slack.RTM
}

func (ut *UserTeam) GetMXID() id.UserID {
	return ut.Key.MXID
}

func (ut *UserTeam) GetRemoteID() string {
	return ut.Key.SlackID
}

func (ut *UserTeam) GetRemoteName() string {
	return ut.SlackEmail // TODO: maybe get a better name for this purpose
}

func (ut *UserTeam) Scan(row dbutil.Scannable) *UserTeam {
	var token sql.NullString
	var cookieToken sql.NullString

	err := row.Scan(&ut.Key.MXID, &ut.SlackEmail, &ut.Key.SlackID, &ut.TeamName, &ut.Key.TeamID, &token, &cookieToken)
	if err != nil {
		if err != sql.ErrNoRows {
			ut.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	if token.Valid {
		ut.Token = token.String
	}
	if cookieToken.Valid {
		ut.CookieToken = cookieToken.String
	}

	return ut
}

func (ut *UserTeam) Upsert() {
	query := `
		INSERT INTO user_team (mxid, slack_email, slack_id, team_name, team_id, token, cookie_token)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (mxid, slack_id, team_id) DO UPDATE
			SET slack_email=excluded.slack_email, team_name=excluded.team_name, token=excluded.token, cookie_token=excluded.cookie_token
	`

	var token sql.NullString
	var cookieToken sql.NullString

	if ut.Token != "" {
		token.String = ut.Token
		token.Valid = true
	}
	if ut.CookieToken != "" {
		cookieToken.String = ut.CookieToken
		cookieToken.Valid = true
	}

	_, err := ut.db.Exec(query, ut.Key.MXID, ut.SlackEmail, ut.Key.SlackID, ut.TeamName, ut.Key.TeamID, token, cookieToken)

	if err != nil {
		ut.log.Warnfln("Failed to upsert %s/%s/%s: %v", ut.Key.MXID, ut.Key.SlackID, ut.Key.TeamID, err)
	}
}
