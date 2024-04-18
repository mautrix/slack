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

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/id"
)

type TeamPortalQuery struct {
	*dbutil.QueryHelper[*TeamPortal]
}

func newTeamPortal(qh *dbutil.QueryHelper[*TeamPortal]) *TeamPortal {
	return &TeamPortal{qh: qh}
}

const (
	getTeamPortalBaseQuery = `
		SELECT id, mxid, domain, url, name, avatar, avatar_mxc, name_set, avatar_set FROM team_portal
	`
	getTeamPortalByIDQuery   = getTeamPortalBaseQuery + " WHERE id=$1"
	getTeamPortalByMXIDQuery = getTeamPortalBaseQuery + " WHERE mxid=$1"
	insertTeamPortalQuery    = `
		INSERT INTO team_portal (id, mxid, domain, url, name, avatar, avatar_mxc, name_set, avatar_set)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	updateTeamPortalQuery = `
		UPDATE team_portal
		SET mxid=$2, domain=$3, url=$4, name=$5, avatar=$6, avatar_mxc=$7, name_set=$8, avatar_set=$9
		WHERE id=$1
	`
)

func (tpq *TeamPortalQuery) GetBySlackID(ctx context.Context, teamID string) (*TeamPortal, error) {
	return tpq.QueryOne(ctx, getTeamPortalByIDQuery, teamID)
}

func (tpq *TeamPortalQuery) GetByMXID(ctx context.Context, mxid id.RoomID) (*TeamPortal, error) {
	return tpq.QueryOne(ctx, getTeamPortalByMXIDQuery, mxid)
}

type TeamPortal struct {
	qh *dbutil.QueryHelper[*TeamPortal]

	ID        string
	MXID      id.RoomID
	Domain    string
	URL       string
	Name      string
	Avatar    string
	AvatarMXC id.ContentURI
	NameSet   bool
	AvatarSet bool
}

func (tp *TeamPortal) Scan(row dbutil.Scannable) (*TeamPortal, error) {
	var mxid, avatarMXC sql.NullString
	err := row.Scan(&tp.ID, &mxid, &tp.Domain, &tp.URL, &tp.NameSet, &tp.AvatarSet, &avatarMXC, &tp.NameSet, &tp.AvatarSet)
	if err != nil {
		return nil, err
	}
	tp.MXID = id.RoomID(mxid.String)
	tp.AvatarMXC, _ = id.ParseContentURI(avatarMXC.String)
	return tp, nil
}

func (tp *TeamPortal) sqlVariables() []any {
	return []any{
		tp.ID,
		dbutil.StrPtr(tp.MXID),
		tp.Domain,
		tp.URL,
		tp.Name,
		tp.Avatar,
		dbutil.StrPtr(tp.AvatarMXC.String()),
		tp.NameSet,
		tp.AvatarSet,
	}
}

func (tp *TeamPortal) Insert(ctx context.Context) error {
	return tp.qh.Exec(ctx, insertTeamPortalQuery, tp.sqlVariables()...)
}

func (tp *TeamPortal) Update(ctx context.Context) error {
	return tp.qh.Exec(ctx, updateTeamPortalQuery, tp.sqlVariables()...)
}
