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

type Portal struct {
	db  *Database
	log log.Logger

	Key  PortalKey
	MXID id.RoomID

	Name  string
	Topic string

	Encrypted bool

	Avatar    string
	AvatarURL id.ContentURI

	FirstEventID id.EventID
}

func (p *Portal) Scan(row dbutil.Scannable) *Portal {
	var mxid, avatarURL, firstEventID sql.NullString

	err := row.Scan(&p.Key.ChannelID, &p.Key.Receiver, &mxid, &p.Name,
		&p.Topic, &p.Avatar, &avatarURL, &firstEventID,
		&p.Encrypted)

	if err != nil {
		if err != sql.ErrNoRows {
			p.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	p.MXID = id.RoomID(mxid.String)
	p.AvatarURL, _ = id.ParseContentURI(avatarURL.String)
	p.FirstEventID = id.EventID(firstEventID.String)

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
		" (channel_id, receiver, mxid, name, topic, avatar, avatar_url," +
		" first_event_id, encrypted)" +
		" VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)"

	_, err := p.db.Exec(query, p.Key.ChannelID, p.Key.Receiver, p.mxidPtr(),
		p.Name, p.Topic, p.Avatar, p.AvatarURL.String(),
		p.FirstEventID.String(), p.Encrypted)

	if err != nil {
		p.log.Warnfln("Failed to insert %s: %v", p.Key, err)
	}
}

func (p *Portal) Update() {
	query := "UPDATE portal SET" +
		" mxid=$1, name=$2, topic=$3, avatar=$4, avatar_url=$5, " +
		" first_event_id=$7, encrypted=$8" +
		" WHERE channel_id=$9 AND receiver=$10"

	_, err := p.db.Exec(query, p.mxidPtr(), p.Name, p.Topic, p.Avatar,
		p.AvatarURL.String(), p.FirstEventID.String(),
		p.Encrypted,
		p.Key.ChannelID, p.Key.Receiver)

	if err != nil {
		p.log.Warnfln("Failed to update %s: %v", p.Key, err)
	}
}

func (p *Portal) Delete() {
	query := "DELETE FROM portal WHERE channel_id=$1 AND receiver=$2"
	_, err := p.db.Exec(query, p.Key.ChannelID, p.Key.Receiver)
	if err != nil {
		p.log.Warnfln("Failed to delete %s: %v", p.Key, err)
	}
}
