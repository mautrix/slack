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
	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

const (
	portalSelect = "SELECT team_id, user_id, channel_id, mxid, plain_name," +
		" name, type, dm_user_id, topic, avatar, avatar_url, first_event_id," +
		" encrypted FROM portal"
)

type PortalQuery struct {
	db  *Database
	log log.Logger
}

func (pq *PortalQuery) New() *Portal {
	return &Portal{
		db:  pq.db,
		log: pq.log,
	}
}

func (pq *PortalQuery) GetAll() []*Portal {
	return pq.getAll(portalSelect)
}

func (pq *PortalQuery) GetByID(key PortalKey) *Portal {
	return pq.get(portalSelect+" WHERE team_id=$1 AND user_id=$2 AND channel_id=$3", key.TeamID, key.UserID, key.ChannelID)
}

func (pq *PortalQuery) GetByMXID(mxid id.RoomID) *Portal {
	return pq.get(portalSelect+" WHERE mxid=$1", mxid)
}

func (pq *PortalQuery) GetAllByID(teamID, userID string) []*Portal {
	return pq.getAll(portalSelect+" WHERE team_id=$1 AND user_id=$2", teamID, userID)
}

func (pq *PortalQuery) FindPrivateChats(receiver string) []*Portal {
	query := portalSelect + " portal WHERE receiver=$1;"

	return pq.getAll(query, receiver)
}

func (pq *PortalQuery) getAll(query string, args ...interface{}) []*Portal {
	rows, err := pq.db.Query(query, args...)
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()

	portals := []*Portal{}
	for rows.Next() {
		portals = append(portals, pq.New().Scan(rows))
	}

	return portals
}

func (pq *PortalQuery) get(query string, args ...interface{}) *Portal {
	row := pq.db.QueryRow(query, args...)
	if row == nil {
		return nil
	}

	return pq.New().Scan(row)
}
