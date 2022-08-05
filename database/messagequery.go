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

type MessageQuery struct {
	db  *Database
	log log.Logger
}

const (
	messageSelect = "SELECT team_id, user_id, channel_id, slack_message_id," +
		" matrix_message_id, author_id FROM message"
)

func (mq *MessageQuery) New() *Message {
	return &Message{
		db:  mq.db,
		log: mq.log,
	}
}

func (mq *MessageQuery) GetAll(key PortalKey) []*Message {
	query := messageSelect + " WHERE team_id=$1 AND user_id=$2 AND channel_id=$3"

	rows, err := mq.db.Query(query, key.TeamID, key.UserID, key.ChannelID)
	if err != nil || rows == nil {
		return nil
	}

	messages := []*Message{}
	for rows.Next() {
		messages = append(messages, mq.New().Scan(rows))
	}

	return messages
}

func (mq *MessageQuery) GetBySlackID(key PortalKey, slackID string) *Message {
	query := messageSelect + " WHERE team_id=$1 AND user_id=$2" +
		" AND channel_id=$3 AND slack_message_id=$4"

	row := mq.db.QueryRow(query, key.TeamID, key.UserID, key.ChannelID, slackID)
	if row == nil {
		mq.log.Debugfln("failed to find existing message for slack_id` %s", slackID)
		return nil
	}

	return mq.New().Scan(row)
}

func (mq *MessageQuery) GetByMatrixID(key PortalKey, matrixID id.EventID) *Message {
	query := messageSelect + " WHERE team_id=$1 AND user_id=$2 AND channel_id=$3 AND matrix_message_id=$4"

	row := mq.db.QueryRow(query, key.TeamID, key.UserID, key.ChannelID, matrixID)
	if row == nil {
		return nil
	}

	return mq.New().Scan(row)
}
