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

type UserQuery struct {
	*dbutil.QueryHelper[*User]
}

func newUser(qh *dbutil.QueryHelper[*User]) *User {
	return &User{qh: qh}
}

const (
	getUserByMXIDQuery              = `SELECT mxid, management_room, space_room, access_token FROM "user" WHERE mxid=$1`
	getAllUsersWithAccessTokenQuery = `SELECT mxid, management_room, space_room, access_token FROM "user" WHERE access_token<>''`
	insertUserQuery                 = `INSERT INTO "user" (mxid, management_room, space_room, access_token) VALUES ($1, $2, $3, $4)`
	updateUserQuery                 = `UPDATE "user" SET management_room=$2, space_room=$3, access_token=$4 WHERE mxid=$1`
)

func (uq *UserQuery) GetByMXID(ctx context.Context, userID id.UserID) (*User, error) {
	return uq.QueryOne(ctx, getUserByMXIDQuery, userID)
}

func (uq *UserQuery) GetAllWithAccessToken(ctx context.Context) ([]*User, error) {
	return uq.QueryMany(ctx, getAllUsersWithAccessTokenQuery)
}

type User struct {
	qh *dbutil.QueryHelper[*User]

	MXID           id.UserID
	ManagementRoom id.RoomID
	SpaceRoom      id.RoomID
	AccessToken    string
}

func (u *User) Scan(row dbutil.Scannable) (*User, error) {
	var managementRoom, spaceRoom, accessToken sql.NullString

	err := row.Scan(&u.MXID, &managementRoom, &spaceRoom, &accessToken)
	if err != nil {
		return nil, err
	}

	u.SpaceRoom = id.RoomID(spaceRoom.String)
	u.ManagementRoom = id.RoomID(managementRoom.String)
	u.AccessToken = accessToken.String

	return u, err
}

func (u *User) sqlVariables() []any {
	return []any{u.MXID, dbutil.StrPtr(u.ManagementRoom), dbutil.StrPtr(u.SpaceRoom), dbutil.StrPtr(u.AccessToken)}
}

func (u *User) Insert(ctx context.Context) error {
	return u.qh.Exec(ctx, insertUserQuery, u.sqlVariables()...)
}

func (u *User) Update(ctx context.Context) error {
	return u.qh.Exec(ctx, updateUserQuery, u.sqlVariables()...)
}
