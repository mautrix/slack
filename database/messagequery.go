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
	messageSelect = "SELECT team_id, channel_id, slack_message_id," +
		" matrix_message_id, author_id, slack_thread_id FROM message"
)

func (mq *MessageQuery) New() *Message {
	return &Message{
		db:  mq.db,
		log: mq.log,
	}
}

func (mq *MessageQuery) GetAll(key PortalKey) []*Message {
	query := messageSelect + " WHERE team_id=$1 AND channel_id=$2"

	rows, err := mq.db.Query(query, key.TeamID, key.ChannelID)
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
	query := messageSelect + " WHERE team_id=$1" +
		" AND channel_id=$2 AND slack_message_id=$3"

	row := mq.db.QueryRow(query, key.TeamID, key.ChannelID, slackID)
	if row == nil {
		mq.log.Debugfln("failed to find existing message for slack_id` %s", slackID)
		return nil
	}

	return mq.New().Scan(row)
}

func (mq *MessageQuery) GetByMatrixID(key PortalKey, matrixID id.EventID) *Message {
	query := messageSelect + " WHERE team_id=$1 AND channel_id=$2 AND matrix_message_id=$3"

	row := mq.db.QueryRow(query, key.TeamID, key.ChannelID, matrixID)
	if row == nil {
		return nil
	}

	return mq.New().Scan(row)
}

func (mq *MessageQuery) GetLastInThread(key PortalKey, slackThreadId string) *Message {
	query := messageSelect + " WHERE team_id=$1 AND channel_id=$2 AND slack_thread_id=$3 ORDER BY slack_message_id DESC LIMIT 1"

	row := mq.db.QueryRow(query, key.TeamID, key.ChannelID, slackThreadId)
	if row == nil {
		return mq.GetBySlackID(key, slackThreadId)
	}

	message := mq.New().Scan(row)
	if message == nil {
		return mq.GetBySlackID(key, slackThreadId)
	}

	return message
}

func (mq *MessageQuery) GetFirst(key PortalKey) *Message {
	query := messageSelect + " WHERE team_id=$1 AND channel_id=$2 ORDER BY slack_message_id ASC LIMIT 1"

	row := mq.db.QueryRow(query, key.TeamID, key.ChannelID)
	if row == nil {
		mq.log.Debugfln("failed to find existing message for portal` %s", key)
		return nil
	}

	return mq.New().Scan(row)
}

func (mq *MessageQuery) GetLast(key PortalKey) *Message {
	query := messageSelect + " WHERE team_id=$1 AND channel_id=$2 ORDER BY slack_message_id DESC LIMIT 1"

	row := mq.db.QueryRow(query, key.TeamID, key.ChannelID)
	if row == nil {
		mq.log.Debugfln("failed to find existing message for portal` %s", key)
		return nil
	}

	return mq.New().Scan(row)
}

func (mq *MessageQuery) ClearAllForPortal(key PortalKey) error {
	query := "DELETE FROM message WHERE team_id=$1 AND channel_id=$2"

	resp, err := mq.db.Exec(query, key.TeamID, key.ChannelID)
	if err != nil {
		mq.log.Errorfln("failed to clear all message rows for portal: %#v", key)
		return err
	}
	rowCount, _ := resp.RowsAffected()
	mq.log.Debugfln("cleared %d message rows for portal: %#v", rowCount, key)
	return nil
}
