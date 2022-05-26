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

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
)

type Attachment struct {
	db  *Database
	log log.Logger

	Channel PortalKey

	DiscordMessageID    string
	DiscordAttachmentID string
	MatrixEventID       id.EventID
}

func (a *Attachment) Scan(row dbutil.Scannable) *Attachment {
	err := row.Scan(
		&a.Channel.ChannelID, &a.Channel.Receiver,
		&a.DiscordMessageID, &a.DiscordAttachmentID,
		&a.MatrixEventID)

	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			a.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	return a
}

func (a *Attachment) Insert() {
	query := "INSERT INTO attachment" +
		" (channel_id, receiver, discord_message_id, discord_attachment_id, " +
		" matrix_event_id) VALUES ($1, $2, $3, $4, $5);"

	_, err := a.db.Exec(
		query,
		a.Channel.ChannelID, a.Channel.Receiver,
		a.DiscordMessageID, a.DiscordAttachmentID,
		a.MatrixEventID,
	)

	if err != nil {
		a.log.Warnfln("Failed to insert attachment for %s@%s: %v", a.Channel, a.DiscordMessageID, err)
	}
}

func (a *Attachment) Delete() {
	query := "DELETE FROM attachment WHERE" +
		" channel_id=$1 AND receiver=$2 AND discord_attachment_id=$3 AND" +
		" matrix_event_id=$4"

	_, err := a.db.Exec(
		query,
		a.Channel.ChannelID, a.Channel.Receiver,
		a.DiscordAttachmentID, a.MatrixEventID,
	)

	if err != nil {
		a.log.Warnfln("Failed to delete attachment for %s@%s: %v", a.Channel, a.DiscordAttachmentID, err)
	}
}
