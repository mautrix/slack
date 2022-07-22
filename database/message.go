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
	"errors"
	"time"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
)

type Message struct {
	db  *Database
	log log.Logger

	Channel PortalKey

	SlackID  string
	MatrixID id.EventID

	AuthorID  string
	Timestamp time.Time
}

func (m *Message) Scan(row dbutil.Scannable) *Message {
	var ts int64

	err := row.Scan(&m.Channel.TeamID, &m.Channel.UserID, &m.Channel.ChannelID, &m.SlackID, &m.MatrixID, &m.AuthorID, &ts)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			m.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	if ts != 0 {
		m.Timestamp = time.Unix(ts, 0)
	}

	return m
}

func (m *Message) Insert() {
	query := "INSERT INTO message" +
		" (team_id, user_id, channel_id, slack_message_id, matrix_message_id," +
		" author_id, timestamp) VALUES ($1, $2, $3, $4, $5, $6, $7)"

	_, err := m.db.Exec(query, m.Channel.TeamID, m.Channel.UserID,
		m.Channel.ChannelID, m.SlackID, m.MatrixID, m.AuthorID,
		m.Timestamp.Unix())

	if err != nil {
		m.log.Warnfln("Failed to insert %s@%s: %v", m.Channel, m.SlackID, err)
	}
}

func (m *Message) Delete() {
	query := "DELETE FROM message" +
		" WHERE team_id=$1 AND user_id=$2 AND channel_id=$3 AND slack_message_id=$4 AND matrix_message_id=$5"

	_, err := m.db.Exec(query, m.Channel.TeamID, m.Channel.UserID, m.Channel.ChannelID, m.SlackID, m.MatrixID)

	if err != nil {
		m.log.Warnfln("Failed to delete %s@%s: %v", m.Channel, m.SlackID, err)
	}
}

func (m *Message) UpdateMatrixID(mxid id.EventID) {
	query := "UPDATE message SET matrix_message_id=$1 WHERE team_id=$2 AND user_id=$3 AND channel_id=$4 AND slack_message_id=$5"
	m.MatrixID = mxid

	_, err := m.db.Exec(query, m.MatrixID, m.Channel.TeamID, m.Channel.UserID, m.Channel.ChannelID, m.SlackID)
	if err != nil {
		m.log.Warnfln("Failed to update %s@%s: %v", m.Channel, m.SlackID, err)
	}
}
