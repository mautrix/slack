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

type PuppetQuery struct {
	*dbutil.QueryHelper[*Puppet]
}

func newPuppet(qh *dbutil.QueryHelper[*Puppet]) *Puppet {
	return &Puppet{qh: qh}
}

const (
	insertPuppetQuery = `
		INSERT INTO puppet (
			team_id, user_id,
			name, avatar, avatar_mxc, is_bot,
			name_set, avatar_set, contact_info_set
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	updatePuppetQuery = `
		UPDATE puppet
		SET name=$3, avatar=$4, avatar_mxc=$5, is_bot=$6,
			name_set=$7, avatar_set=$8, contact_info_set=$9
		WHERE team_id=$1 AND user_id=$2
	`
	getAllPuppetsQuery = `
		SELECT team_id, user_id, name, avatar, avatar_mxc, is_bot,
		       name_set, avatar_set, contact_info_set
		FROM puppet
	`
	getAllPuppetsForTeamQuery = getAllPuppetsQuery + `WHERE team_id=$1`
	getPuppetByIDQuery        = getAllPuppetsForTeamQuery + ` AND user_id=$2`
)

func (pq *PuppetQuery) Get(ctx context.Context, key UserTeamKey) (*Puppet, error) {
	return pq.QueryOne(ctx, getPuppetByIDQuery, key.TeamID, key.UserID)
}

func (pq *PuppetQuery) GetAll(ctx context.Context) ([]*Puppet, error) {
	return pq.QueryMany(ctx, getAllPuppetsQuery)
}

func (pq *PuppetQuery) GetAllForTeam(ctx context.Context, teamID string) ([]*Puppet, error) {
	return pq.QueryMany(ctx, getAllPuppetsForTeamQuery, teamID)
}

type Puppet struct {
	qh *dbutil.QueryHelper[*Puppet]

	UserTeamKey

	Name           string
	Avatar         string
	AvatarMXC      id.ContentURI
	IsBot          bool
	NameSet        bool
	AvatarSet      bool
	ContactInfoSet bool
}

func (p *Puppet) Scan(row dbutil.Scannable) (*Puppet, error) {
	var avatarURL sql.NullString
	err := row.Scan(
		&p.TeamID, &p.UserID,
		&p.Name, &p.NameSet, &p.Avatar, &avatarURL, &p.IsBot,
		&p.NameSet, &p.AvatarSet, &p.ContactInfoSet,
	)
	if err != nil {
		return nil, err
	}

	p.AvatarMXC, _ = id.ParseContentURI(avatarURL.String)
	return p, nil
}

func (p *Puppet) sqlVariables() []any {
	return []any{
		p.TeamID, p.UserID,
		p.Name, p.Avatar, dbutil.StrPtr(p.AvatarMXC.String()), p.IsBot,
		p.NameSet, p.AvatarSet, p.ContactInfoSet,
	}
}

func (p *Puppet) Insert(ctx context.Context) error {
	return p.qh.Exec(ctx, insertPuppetQuery, p.sqlVariables()...)
}

func (p *Puppet) Update(ctx context.Context) error {
	return p.qh.Exec(ctx, updatePuppetQuery, p.sqlVariables()...)
}
