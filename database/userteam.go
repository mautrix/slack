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
	"fmt"

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

const userTeamSelect = "SELECT ut.mxid, ut.slack_email, ut.slack_id, ut.team_name, ut.team_id, ut.token, ut.cookie_token FROM user_team ut "

func (utq *UserTeamQuery) GetBySlackDomain(userID id.UserID, email, domain string) *UserTeam {
	query := userTeamSelect + "WHERE ut.mxid=$1 AND ut.slack_email=$2 AND ut.team_id=(SELECT team_id FROM team_info WHERE team_domain=$3)"

	row := utq.db.QueryRow(query, userID, email, domain)
	if row == nil {
		return nil
	}

	return utq.New().Scan(row)
}

func (utq *UserTeamQuery) GetAllByMXIDWithToken(userID id.UserID) []*UserTeam {
	query := userTeamSelect + "WHERE ut.mxid=$1 AND ut.token IS NOT NULL"

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
	query := userTeamSelect + "WHERE ut.team_id=$1"

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

func (utq *UserTeamQuery) GetFirstUserTeamForPortal(portal *PortalKey) *UserTeam {
	query := userTeamSelect + `
		JOIN user_team_portal utp ON utp.matrix_user_id = ut.mxid
			AND utp.slack_team_id = ut.team_id
			AND utp.slack_user_id = ut.slack_id
		WHERE utp.slack_team_id = $1
			AND utp.portal_channel_id = $2
			AND ut.token IS NOT NULL
		LIMIT 1`

	row := utq.db.QueryRow(query, portal.TeamID, portal.ChannelID)
	if row == nil {
		return nil
	}

	return utq.New().Scan(row)
}

type UserTeamKey struct {
	MXID    id.UserID
	SlackID string
	TeamID  string
}

func (utk UserTeamKey) String() string {
	return fmt.Sprintf("%s-%s", utk.TeamID, utk.SlackID)
}

type UserTeam struct {
	db  *Database
	log log.Logger

	Key UserTeamKey

	SlackEmail string
	TeamName   string

	Token          string
	CookieToken    string
	SlackUserNames map[string]string

	Client *slack.Client
	RTM    *slack.RTM
}

func (ut *UserTeam) GetMXID() id.UserID {
	return ut.Key.MXID
}

func (ut *UserTeam) GetRemoteID() string {
	return ut.Key.TeamID
}

func (ut *UserTeam) GetRemoteName() string {
	return ut.TeamName
}

func (ut *UserTeam) IsLoggedIn() bool {
	return ut.Token != ""
}

func (ut *UserTeam) IsConnected() bool {
	return ut.Client != nil && ut.RTM != nil
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

	token := sqlNullString(ut.Token)
	cookieToken := sqlNullString(ut.CookieToken)

	_, err := ut.db.Exec(query, ut.Key.MXID, ut.SlackEmail, ut.Key.SlackID, ut.TeamName, ut.Key.TeamID, token, cookieToken)

	if err != nil {
		ut.log.Warnfln("Failed to upsert %s/%s/%s: %v", ut.Key.MXID, ut.Key.SlackID, ut.Key.TeamID, err)
	}
}

func (ut *UserTeam) GetSlackUserNameBySlackID(slackID string) string {
	if ut.SlackUserNames == nil {
		ut.SlackUserNames = make(map[string]string)
	}
	slackUserName, ok := ut.SlackUserNames[slackID]
	if !ok {
		userInfo, err := ut.Client.GetUserInfo(slackID)
		if err != nil {
			ut.log.Warnfln("Failed to get slack user info for slackID: %s", slackID)
		} else {
			if userInfo.RealName != "" {
				slackUserName = userInfo.RealName
			} else {
				slackUserName = userInfo.Name
			}

			ut.SlackUserNames[slackID] = slackUserName
		}
	}

	return slackUserName
}
