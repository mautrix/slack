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
	"context"
	"database/sql"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/id"
)

type UserTeamQuery struct {
	*dbutil.QueryHelper[*UserTeam]
}

func newUserTeam(qh *dbutil.QueryHelper[*UserTeam]) *UserTeam {
	return &UserTeam{qh: qh}
}

const (
	getAllUserTeamsForUserQuery = `
		SELECT team_id, user_id, user_mxid, email, token, cookie_token, in_space FROM user_team WHERE user_mxid = $1
	`
	getAllUserTeamsByTeamIDQuery = `
		SELECT team_id, user_id, user_mxid, email, token, cookie_token, in_space FROM user_team WHERE team_id=$1
	`
	getUserTeamByIDQuery = `
		SELECT team_id, user_id, user_mxid, email, token, cookie_token, in_space FROM user_team WHERE team_id=$1 AND user_id = $2
	`
	getAllUserTeamsWithTokenQuery = `
		SELECT team_id, user_id, user_mxid, email, token, cookie_token, in_space FROM user_team WHERE token<>''
	`
	getFirstUserTeamForPortalQuery = `
		SELECT ut.team_id, ut.user_id, ut.user_mxid, ut.email, ut.token, ut.cookie_token, ut.in_space FROM user_team ut
		JOIN user_team_portal utp ON utp.team_id = ut.team_id AND utp.user_id = ut.user_id
		WHERE utp.team_id = $1
			AND utp.channel_id = $2
			AND ut.token<>''
		LIMIT 1
	`
	insertUserTeamQuery = `
		INSERT INTO user_team (team_id, user_id, user_mxid, email, token, cookie_token, in_space)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (team_id, user_id) DO UPDATE
			SET user_mxid=excluded.user_mxid,
			    email=excluded.email,
			    token=excluded.token,
			    cookie_token=excluded.cookie_token,
			    in_space=excluded.in_space
	`
	updateUserTeamQuery = `
		UPDATE user_team
		SET email=$4,
			token=$5,
			cookie_token=$6,
			in_space=$7
		WHERE team_id=$1 AND user_id=$2 AND user_mxid=$3
	`
	deleteUserTeamQuery = `
		DELETE FROM user_team WHERE team_id=$1 AND user_id=$2
	`
)

func (utq *UserTeamQuery) GetByID(ctx context.Context, key UserTeamKey) (*UserTeam, error) {
	return utq.QueryOne(ctx, getUserTeamByIDQuery, key.TeamID, key.UserID)
}

func (utq *UserTeamQuery) GetAllForUser(ctx context.Context, userID id.UserID) ([]*UserTeam, error) {
	return utq.QueryMany(ctx, getAllUserTeamsForUserQuery, userID)
}

func (utq *UserTeamQuery) GetAllWithToken(ctx context.Context) ([]*UserTeam, error) {
	return utq.QueryMany(ctx, getAllUserTeamsWithTokenQuery)
}

func (utq *UserTeamQuery) GetAllInTeam(ctx context.Context, teamID string) ([]*UserTeam, error) {
	return utq.QueryMany(ctx, getAllUserTeamsByTeamIDQuery, teamID)
}

func (utq *UserTeamQuery) GetFirstUserTeamForPortal(ctx context.Context, portal *PortalKey) (*UserTeam, error) {
	return utq.QueryOne(ctx, getFirstUserTeamForPortalQuery, portal.TeamID, portal.ChannelID)
}

type UserTeamKey struct {
	TeamID string
	UserID string
}

func (utk UserTeamKey) MarshalZerologObject(e *zerolog.Event) {
	e.Str("team_id", utk.TeamID).Str("user_id", utk.UserID)
}

type UserTeamMXIDKey struct {
	UserTeamKey
	UserMXID id.UserID
}

func (utk UserTeamMXIDKey) MarshalZerologObject(e *zerolog.Event) {
	e.Str("team_id", utk.TeamID).Str("user_id", utk.UserID).Stringer("user_mxid", utk.UserMXID)
}

type UserTeam struct {
	qh *dbutil.QueryHelper[*UserTeam]

	UserTeamMXIDKey
	Email       string
	Token       string
	CookieToken string
	InSpace     bool
}

func (ut *UserTeam) Scan(row dbutil.Scannable) (*UserTeam, error) {
	var token sql.NullString
	var cookieToken sql.NullString

	err := row.Scan(&ut.TeamID, &ut.UserID, &ut.UserMXID, &ut.Email, &token, &cookieToken, &ut.InSpace)
	if err != nil {
		return nil, err
	}

	ut.Token = token.String
	ut.CookieToken = cookieToken.String
	return ut, nil
}

func (ut *UserTeam) sqlVariables() []any {
	return []any{
		ut.TeamID, ut.UserID, ut.UserMXID, ut.Email, ut.Token, ut.CookieToken, ut.InSpace,
	}
}

func (ut *UserTeam) Insert(ctx context.Context) error {
	return ut.qh.Exec(ctx, insertUserTeamQuery, ut.sqlVariables()...)
}

func (ut *UserTeam) Update(ctx context.Context) error {
	return ut.qh.Exec(ctx, updateUserTeamQuery, ut.sqlVariables()...)
}

func (ut *UserTeam) Delete(ctx context.Context) error {
	return ut.qh.Exec(ctx, deleteUserTeamQuery, ut.TeamID, ut.UserID)
}
