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
	db  *Database
	log log.Logger

	Key  PortalKey
	MXID id.RoomID

	Type     ChannelType
	DMUserID string

	PlainName string
	Name      string
	NameSet   bool
	Topic     string
	TopicSet  bool
	Encrypted bool
	Avatar    string
	AvatarURL id.ContentURI
	AvatarSet bool

	FirstEventID id.EventID
	NextBatchID  id.BatchID
	FirstSlackID string
}

func (p *Portal) Scan(row dbutil.Scannable) *Portal {
	var mxid, dmUserID, avatarURL, firstEventID, nextBatchID, firstSlackID sql.NullString

	err := row.Scan(&p.Key.TeamID, &p.Key.ChannelID, &mxid,
		&p.Type, &dmUserID, &p.PlainName, &p.Name, &p.NameSet, &p.Topic,
		&p.TopicSet, &p.Avatar, &avatarURL, &p.AvatarSet, &firstEventID,
		&p.Encrypted, &nextBatchID, &firstSlackID)

	if err != nil {
		if err != sql.ErrNoRows {
			p.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	p.MXID = id.RoomID(mxid.String)
	p.DMUserID = dmUserID.String
	p.AvatarURL, _ = id.ParseContentURI(avatarURL.String)
	p.FirstEventID = id.EventID(firstEventID.String)
	p.NextBatchID = id.BatchID(nextBatchID.String)
	p.FirstSlackID = firstSlackID.String

	return p
}

func (p *Portal) mxidPtr() *id.RoomID {
	if p.MXID != "" {
		return &p.MXID
	}

	return nil
}

func (p *Portal) Insert() {
	query := "INSERT INTO portal" +
		" (team_id, channel_id, mxid, type, dm_user_id, plain_name," +
		" name, name_set, topic, topic_set, avatar, avatar_url, avatar_set," +
		" first_event_id, encrypted, next_batch_id, first_slack_id)" +
		" VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)"

	_, err := p.db.Exec(query, p.Key.TeamID, p.Key.ChannelID,
		p.mxidPtr(), p.Type, p.DMUserID, p.PlainName, p.Name, p.NameSet,
		p.Topic, p.TopicSet, p.Avatar, p.AvatarURL.String(), p.AvatarSet,
		p.FirstEventID.String(), p.Encrypted, p.NextBatchID.String(), p.FirstSlackID)

	if err != nil {
		p.log.Warnfln("Failed to insert %s: %v", p.Key, err)
	}
}

func (p *Portal) Update() {
	query := "UPDATE portal SET" +
		" mxid=$1, type=$2, dm_user_id=$3, plain_name=$4, name=$5, name_set=$6," +
		" topic=$7, topic_set=$8, avatar=$9, avatar_url=$10, avatar_set=$11," +
		" first_event_id=$12, encrypted=$13, next_batch_id=$14, first_slack_id=$15" +
		" WHERE team_id=$16 AND channel_id=$17"

	_, err := p.db.Exec(query, p.mxidPtr(), p.Type, p.DMUserID, p.PlainName,
		p.Name, p.NameSet, p.Topic, p.TopicSet, p.Avatar, p.AvatarURL.String(),
		p.AvatarSet, p.FirstEventID.String(), p.Encrypted, p.NextBatchID.String(), p.FirstSlackID,
		p.Key.TeamID, p.Key.ChannelID)

	if err != nil {
		p.log.Warnfln("Failed to update %s: %v", p.Key, err)
	}
}

func (p *Portal) Delete() {
	query := "DELETE FROM portal WHERE team_id=$1 AND channel_id=$2"
	_, err := p.db.Exec(query, p.Key.TeamID, p.Key.ChannelID)
	if err != nil {
		p.log.Warnfln("Failed to delete %s: %v", p.Key, err)
	}
}

func (p *Portal) InsertUser(utk UserTeamKey) {
	query := "INSERT INTO user_team_portal" +
		" (matrix_user_id, slack_user_id, slack_team_id, portal_channel_id)" +
		" VALUES ($1, $2, $3, $4)" +
		" ON CONFLICT DO NOTHING"

	_, err := p.db.Exec(query, utk.MXID, utk.SlackID, utk.TeamID, p.Key.ChannelID)
	if err != nil {
		p.log.Warnfln("Failed to insert userteam %s: %v", utk, err)
	}
}

func (p *Portal) DeleteUser(utk UserTeamKey) {
	query := "DELETE FROM user_team_portal WHERE matrix_user_id=$1 AND slack_user_id=$2" +
		" slack_team_id=$3 AND portal_channel_id=$4"
	_, err := p.db.Exec(query, utk.MXID, utk.SlackID, utk.TeamID, p.Key.ChannelID)
	if err != nil {
		p.log.Warnfln("Failed to delete userteam %s: %v", utk, err)
	}
}
