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

type AttachmentQuery struct {
	db  *Database
	log log.Logger
}

const (
	attachmentSelect = "SELECT team_id, channel_id, " +
		" slack_message_id, slack_file_id, matrix_event_id, slack_thread_id" +
		" FROM attachment"
)

func (aq *AttachmentQuery) New() *Attachment {
	return &Attachment{
		db:  aq.db,
		log: aq.log,
	}
}

func (aq *AttachmentQuery) GetAllBySlackMessageID(key PortalKey, slackMessageID string) []*Attachment {
	query := attachmentSelect + " WHERE team_id=$1 AND channel_id=$2" +
		" AND slack_message_id=$3"

	return aq.getAll(query, key.TeamID, key.ChannelID, slackMessageID)
}

func (aq *AttachmentQuery) getAll(query string, args ...interface{}) []*Attachment {
	rows, err := aq.db.Query(query, args...)
	if err != nil {
		aq.log.Debugfln("getAll failed: %v", err)

		return nil
	}

	if rows == nil {
		return nil
	}

	attachments := []*Attachment{}
	for rows.Next() {
		attachments = append(attachments, aq.New().Scan(rows))
	}

	return attachments
}

func (aq *AttachmentQuery) GetBySlackFileID(key PortalKey, slackMessageID, slackFileID string) *Attachment {
	query := attachmentSelect + " WHERE team_id=$1 AND channel_id=$2" +
		" AND slack_message_id=$3 AND slack_file_id=$4"

	return aq.get(query, key.TeamID, key.ChannelID, slackMessageID, slackFileID)
}

func (aq *AttachmentQuery) GetByMatrixID(key PortalKey, matrixEventID id.EventID) *Attachment {
	query := attachmentSelect + " WHERE team_id=$1 AND channel_id=$2 AND matrix_event_id=$3"

	return aq.get(query, key.TeamID, key.ChannelID, matrixEventID)
}

func (aq *AttachmentQuery) get(query string, args ...interface{}) *Attachment {
	row := aq.db.QueryRow(query, args...)
	if row == nil {
		return nil
	}

	return aq.New().Scan(row)
}

func (aq *AttachmentQuery) GetLast(key PortalKey) *Attachment {
	query := attachmentSelect + " WHERE team_id=$1 AND channel_id=$2 ORDER BY slack_message_id DESC LIMIT 1"

	row := aq.db.QueryRow(query, key.TeamID, key.ChannelID)
	if row == nil {
		aq.log.Debugfln("failed to find last attachment for portal` %s", key)
		return nil
	}

	return aq.New().Scan(row)
}
