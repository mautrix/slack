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

type ReactionQuery struct {
	db  *Database
	log log.Logger
}

const (
	reactionSelect = "SELECT channel_id, receiver, discord_message_id," +
		" matrix_event_id, author_id, matrix_name, matrix_url, " +
		" discord_id FROM reaction"
)

func (rq *ReactionQuery) New() *Reaction {
	return &Reaction{
		db:  rq.db,
		log: rq.log,
	}
}

func (rq *ReactionQuery) GetAllByDiscordID(key PortalKey, discordMessageID string) []*Reaction {
	query := reactionSelect + " WHERE channel_id=$1 AND receiver=$2 AND" +
		" discord_message_id=$3"

	return rq.getAll(query, key.ChannelID, key.Receiver, discordMessageID)
}

func (rq *ReactionQuery) GetAllByMatrixID(key PortalKey, matrixEventID id.EventID) []*Reaction {
	query := reactionSelect + " WHERE channel_id=$1 AND receiver=$2 AND" +
		" matrix_event_id=$3"

	return rq.getAll(query, key.ChannelID, key.Receiver, matrixEventID)
}

func (rq *ReactionQuery) getAll(query string, args ...interface{}) []*Reaction {
	rows, err := rq.db.Query(query)
	if err != nil || rows == nil {
		return nil
	}

	reactions := []*Reaction{}
	for rows.Next() {
		reactions = append(reactions, rq.New().Scan(rows))
	}

	return reactions
}

func (rq *ReactionQuery) GetByDiscordID(key PortalKey, discordMessageID, discordID string) *Reaction {
	query := reactionSelect + " WHERE channel_id=$1 AND receiver=$2" +
		" AND discord_message_id=$3 AND discord_id=$4"

	return rq.get(query, key.ChannelID, key.Receiver, discordMessageID, discordID)
}

func (rq *ReactionQuery) GetByMatrixID(key PortalKey, matrixEventID id.EventID) *Reaction {
	query := reactionSelect + " WHERE channel_id=$1 AND receiver=$2" +
		" AND matrix_event_id=$3"

	return rq.get(query, key.ChannelID, key.Receiver, matrixEventID)
}

func (rq *ReactionQuery) get(query string, args ...interface{}) *Reaction {
	row := rq.db.QueryRow(query, args...)
	if row == nil {
		return nil
	}

	return rq.New().Scan(row)
}
