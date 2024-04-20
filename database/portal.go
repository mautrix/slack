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

type PortalQuery struct {
	*dbutil.QueryHelper[*Portal]
}

func newPortal(qh *dbutil.QueryHelper[*Portal]) *Portal {
	return &Portal{qh: qh}
}

const (
	getAllPortalsQuery = `
		SELECT team_id, channel_id, receiver, mxid, type, dm_user_id,
		       plain_name, name, name_set, topic, topic_set, avatar, avatar_mxc, avatar_set,
		       encrypted, in_space, first_slack_id, more_to_backfill
		FROM portal
	`
	getPortalByIDQuery        = getAllPortalsQuery + `WHERE team_id=$1 AND channel_id=$2`
	getPortalByMXIDQuery      = getAllPortalsQuery + `WHERE mxid=$1`
	getDMPortalsWithUserQuery = getAllPortalsQuery + `WHERE team_id=$1 AND dm_user_id=$2 AND type=$3`
	getUserTeamPortalSubquery = `
		SELECT 1 FROM user_team_portal
			WHERE user_team_portal.user_mxid=$1
			  AND user_team_portal.user_id=$2
			  AND user_team_portal.team_id=$3
			  AND user_team_portal.channel_id=portal.channel_id
	`
	getAllPortalsForUserQuery = getAllPortalsQuery + "WHERE EXISTS(" + getUserTeamPortalSubquery + ")"
	insertPortalQuery         = `
		INSERT INTO portal (
			team_id, channel_id, receiver, mxid, type, dm_user_id,
			plain_name, name, name_set, topic, topic_set, avatar, avatar_mxc, avatar_set,
			encrypted, in_space, first_slack_id
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`
	updatePortalQuery = `
		UPDATE portal SET
			receiver=$3, mxid=$4, type=$5, dm_user_id=$6,
			plain_name=$7, name=$8, name_set=$9, topic=$10, topic_set=$11,
			avatar=$12, avatar_mxc=$13, avatar_set=$14,
			encrypted=$15, in_space=$16, first_slack_id=$17
		WHERE team_id=$1 AND channel_id=$2
	`
	deletePortalQuery = `
		DELETE FROM portal WHERE team_id=$1 AND channel_id=$2
	`
)

func (pq *PortalQuery) GetAll(ctx context.Context) ([]*Portal, error) {
	return pq.QueryMany(ctx, getAllPortalsQuery)
}

func (pq *PortalQuery) GetByID(ctx context.Context, key PortalKey) (*Portal, error) {
	return pq.QueryOne(ctx, getPortalByIDQuery, key.TeamID, key.ChannelID)
}

func (pq *PortalQuery) GetByMXID(ctx context.Context, mxid id.RoomID) (*Portal, error) {
	return pq.QueryOne(ctx, getPortalByMXIDQuery, mxid)
}

func (pq *PortalQuery) GetAllForUserTeam(ctx context.Context, utk UserTeamMXIDKey) ([]*Portal, error) {
	return pq.QueryMany(ctx, getAllPortalsForUserQuery, utk.UserMXID, utk.UserID, utk.TeamID)
}

func (pq *PortalQuery) FindPrivateChatsWith(ctx context.Context, utk UserTeamKey) ([]*Portal, error) {
	return pq.QueryMany(ctx, getDMPortalsWithUserQuery, utk.TeamID, utk.UserID, ChannelTypeDM)
}

type ChannelType int64

const (
	ChannelTypeUnknown ChannelType = iota
	ChannelTypeChannel
	ChannelTypeDM
	ChannelTypeGroupDM
)

func (ct ChannelType) String() string {
	switch ct {
	case ChannelTypeChannel:
		return "channel"
	case ChannelTypeDM:
		return "dm"
	case ChannelTypeGroupDM:
		return "group-dm"
	default:
		return "unknown"
	}
}

type Portal struct {
	qh *dbutil.QueryHelper[*Portal]

	PortalKey
	Receiver string
	MXID     id.RoomID

	Type     ChannelType
	DMUserID string

	PlainName string
	Name      string
	NameSet   bool
	Topic     string
	TopicSet  bool
	Avatar    string
	AvatarMXC id.ContentURI
	AvatarSet bool
	Encrypted bool
	InSpace   bool

	OldestSlackMessageID string
	MoreToBackfill       bool
}

func (p *Portal) Scan(row dbutil.Scannable) (*Portal, error) {
	var mxid, dmUserID, avatarMXC, firstSlackID sql.NullString
	err := row.Scan(
		&p.TeamID,
		&p.ChannelID,
		&p.Receiver,
		&mxid,
		&p.Type,
		&dmUserID,
		&p.PlainName,
		&p.Name,
		&p.NameSet,
		&p.Topic,
		&p.TopicSet,
		&p.Avatar,
		&avatarMXC,
		&p.AvatarSet,
		&p.Encrypted,
		&p.InSpace,
		&firstSlackID,
		&p.MoreToBackfill,
	)
	if err != nil {
		return nil, err
	}
	p.MXID = id.RoomID(mxid.String)
	p.DMUserID = dmUserID.String
	p.AvatarMXC, _ = id.ParseContentURI(avatarMXC.String)
	p.OldestSlackMessageID = firstSlackID.String
	return p, nil
}

func (p *Portal) sqlVariables() []any {
	return []any{
		p.TeamID, p.ChannelID, p.Receiver, dbutil.StrPtr(p.MXID), p.Type, dbutil.StrPtr(p.DMUserID),
		p.PlainName, p.Name, p.NameSet, p.Topic, p.TopicSet, p.Avatar, dbutil.StrPtr(p.AvatarMXC.String()), p.AvatarSet,
		p.Encrypted, p.InSpace, p.OldestSlackMessageID, p.MoreToBackfill,
	}

}

func (p *Portal) Insert(ctx context.Context) error {
	return p.qh.Exec(ctx, insertPortalQuery, p.sqlVariables()...)
}

func (p *Portal) Update(ctx context.Context) error {
	return p.qh.Exec(ctx, updatePortalQuery, p.sqlVariables()...)
}

func (p *Portal) Delete(ctx context.Context) error {
	return p.qh.Exec(ctx, deletePortalQuery, p.TeamID, p.ChannelID)
}
