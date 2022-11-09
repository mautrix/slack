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

type Reaction struct {
	db  *Database
	log log.Logger

	Channel PortalKey

	SlackMessageID string
	MatrixEventID  id.EventID

	// The slack ID of who create this reaction
	AuthorID string

	MatrixName string
	MatrixURL  string // Used for custom emoji

	SlackName string // The id or unicode of the emoji for slack
}

func (r *Reaction) Scan(row dbutil.Scannable) *Reaction {
	var slackName sql.NullString

	err := row.Scan(
		&r.Channel.TeamID, &r.Channel.ChannelID,
		&r.SlackMessageID, &r.MatrixEventID,
		&r.AuthorID,
		&r.MatrixName, &r.MatrixURL,
		&slackName)

	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			r.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	r.SlackName = slackName.String

	return r
}

func (r *Reaction) Insert(txn dbutil.Transaction) {
	query := "INSERT INTO reaction" +
		" (team_id, channel_id, slack_message_id, matrix_event_id," +
		"  author_id, matrix_name, matrix_url, slack_name)" +
		" VALUES($1, $2, $3, $4, $5, $6, $7, $8);"

	var slackName sql.NullString

	if r.SlackName != "" {
		slackName = sql.NullString{String: r.SlackName, Valid: true}
	}

	args := []interface{}{r.Channel.TeamID, r.Channel.ChannelID,
		r.SlackMessageID, r.MatrixEventID,
		r.AuthorID,
		r.MatrixName, r.MatrixURL,
		slackName,
	}

	var err error
	if txn != nil {
		_, err = txn.Exec(query, args...)
	} else {
		_, err = r.db.Exec(query, args...)
	}

	if err != nil {
		r.log.Warnfln("Failed to insert reaction for %s@%s: %v", r.Channel, r.SlackMessageID, err)
	}
}

func (r *Reaction) Update() {
	// TODO: determine if we need this. The only scenario I can think of that
	// would require this is if we insert a custom emoji before uploading to
	// the homeserver?
}

func (r *Reaction) Delete() {
	query := "DELETE FROM reaction WHERE" +
		" team_id=$1 AND channel_id=$2 AND slack_message_id=$3 AND author_id=$4 AND slack_name=$5"

	var slackName sql.NullString
	if r.SlackName != "" {
		slackName = sql.NullString{String: r.SlackName, Valid: true}
	}

	_, err := r.db.Exec(
		query,
		r.Channel.TeamID, r.Channel.ChannelID,
		r.SlackMessageID, r.AuthorID,
		slackName,
	)

	if err != nil {
		r.log.Warnfln("Failed to delete reaction for %s@%s: %v", r.Channel, r.SlackMessageID, err)
	}
}
