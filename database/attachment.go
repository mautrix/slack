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

	SlackMessageID string
	SlackFileID    string
	MatrixEventID  id.EventID
	SlackThreadID  string
}

func (a *Attachment) Scan(row dbutil.Scannable) *Attachment {
	err := row.Scan(
		&a.Channel.TeamID, &a.Channel.ChannelID,
		&a.SlackMessageID, &a.SlackFileID,
		&a.MatrixEventID, &a.SlackThreadID)

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
		" (team_id, channel_id, slack_message_id, slack_file_id, " +
		" matrix_event_id, slack_thread_id) VALUES ($1, $2, $3, $4, $5, $6);"

	_, err := a.db.Exec(
		query,
		a.Channel.TeamID, a.Channel.ChannelID,
		a.SlackMessageID, a.SlackFileID,
		a.MatrixEventID, a.SlackThreadID,
	)

	if err != nil {
		a.log.Warnfln("Failed to insert attachment for %s@%s: %v", a.Channel, a.SlackMessageID, err)
	}
}

func (a *Attachment) Delete() {
	query := "DELETE FROM attachment WHERE" +
		" team_id=$1 AND channel_id=$2 AND slack_file_id=$3 AND" +
		" matrix_event_id=$4"

	_, err := a.db.Exec(
		query,
		a.Channel.TeamID, a.Channel.ChannelID,
		a.SlackFileID, a.MatrixEventID,
	)

	if err != nil {
		a.log.Warnfln("Failed to delete attachment for %s@%s: %v", a.Channel, a.SlackFileID, err)
	}
}
