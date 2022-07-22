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
	attachmentSelect = "SELECT team_id, user_id, channel_id, " +
		" discord_message_id, discord_attachment_id, matrix_event_id" +
		" FROM attachment"
)

func (aq *AttachmentQuery) New() *Attachment {
	return &Attachment{
		db:  aq.db,
		log: aq.log,
	}
}

func (aq *AttachmentQuery) GetAllByDiscordMessageID(key PortalKey, discordMessageID string) []*Attachment {
	query := attachmentSelect + " WHERE team_id=$1 AND user_id=$2 AND channel_id=$3" +
		" discord_message_id=$4"

	return aq.getAll(query, key.TeamID, key.UserID, key.ChannelID, discordMessageID)
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

func (aq *AttachmentQuery) GetByDiscordAttachmentID(key PortalKey, discordMessageID, discordID string) *Attachment {
	query := attachmentSelect + " WHERE team_id=$1 AND user_id=$2 AND channel_id=$3" +
		" AND discord_message_id=$4 AND discord_id=$5"

	return aq.get(query, key.TeamID, key.UserID, key.ChannelID, discordMessageID, discordID)
}

func (aq *AttachmentQuery) GetByMatrixID(key PortalKey, matrixEventID id.EventID) *Attachment {
	query := attachmentSelect + " WHERE team_id=$1 AND user_id=$2 AND channel_id=$3 AND matrix_event_id=$4"

	return aq.get(query, key.TeamID, key.UserID, key.ChannelID, matrixEventID)
}

func (aq *AttachmentQuery) get(query string, args ...interface{}) *Attachment {
	row := aq.db.QueryRow(query, args...)
	if row == nil {
		return nil
	}

	return aq.New().Scan(row)
}
