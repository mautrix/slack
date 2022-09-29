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
	portalSelect = "SELECT team_id, user_id, channel_id, mxid, type, " +
		" dm_user_id, plain_name, name, name_set, topic, topic_set," +
		" avatar, avatar_url, avatar_set, first_event_id," +
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
	return pq.get(portalSelect+" WHERE team_id=$1 AND channel_id=$2 AND (type!=2 OR user_id=$3)", key.TeamID, key.ChannelID, key.UserID)
}

func (pq *PortalQuery) GetByMXID(mxid id.RoomID) *Portal {
	return pq.get(portalSelect+" WHERE mxid=$1", mxid)
}

// func (pq *PortalQuery) GetAllByID(teamID, userID string) []*Portal {
// 	return pq.getAll(portalSelect+" WHERE team_id=$1 AND user_id=$2", teamID, userID)
// }

func (pq *PortalQuery) GetAllForUserTeam(utk UserTeamKey) []*Portal {
	return pq.getAll(portalSelect+" WHERE EXISTS (SELECT * FROM user_team_portal WHERE"+
		" user_team_portal.matrix_user_id=$1 AND user_team_portal.slack_user_id=$2 AND user_team_portal.slack_team_id=$3"+
		" user_team_portal.portal_user_id=portal.user_id AND user_team_portal.portal_channel_id=portal.channel_id)",
		utk.MXID, utk.SlackID, utk.TeamID)
}

func (pq *PortalQuery) FindPrivateChatsWith(id string) []*Portal {
	return pq.getAll(portalSelect+" WHERE dm_user_id=$1 AND type=$2", id, ChannelTypeDM)
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
