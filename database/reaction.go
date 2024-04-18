// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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
	"context"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/id"
)

type ReactionQuery struct {
	*dbutil.QueryHelper[*Reaction]
}

func newReaction(qh *dbutil.QueryHelper[*Reaction]) *Reaction {
	return &Reaction{qh: qh}
}

const (
	getReactionBaseQuery = `
		SELECT team_id, channel_id, message_id, msg_first_part_id, author_id, emoji_id, mxid FROM reaction
	`
	getReactionBySlackIDQuery = getReactionBaseQuery + `
		WHERE team_id=$1 AND channel_id=$2 AND message_id=$3 AND author_id=$4 AND emoji_id=$5
	`
	getReactionByMXIDQuery = getReactionBaseQuery + `
		WHERE mxid=$1
	`
	insertReactionQuery = `
		INSERT INTO reaction (team_id, channel_id, message_id, msg_first_part_id, author_id, emoji_id, mxid)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	deleteReactionQuery = `
		DELETE FROM reaction WHERE team_id=$1 AND channel_id=$2 AND message_id=$3 AND author_id=$4 AND emoji_id=$5
	`
)

func (rq *ReactionQuery) GetBySlackID(ctx context.Context, key PortalKey, messageID, authorID, emojiID string) (*Reaction, error) {
	return rq.QueryOne(ctx, getReactionBySlackIDQuery, key.ChannelID, key.TeamID, messageID, authorID, emojiID)
}

func (rq *ReactionQuery) GetByMXID(ctx context.Context, eventID id.EventID) (*Reaction, error) {
	return rq.QueryOne(ctx, getReactionByMXIDQuery, eventID)
}

type Reaction struct {
	qh *dbutil.QueryHelper[*Reaction]

	PortalKey
	MessageID        string
	MessageFirstPart PartID
	AuthorID         string
	EmojiID          string
	MXID             id.EventID
}

func (r *Reaction) Scan(row dbutil.Scannable) (*Reaction, error) {
	return dbutil.ValueOrErr(r, row.Scan(
		&r.TeamID,
		&r.ChannelID,
		&r.MessageID,
		&r.MessageFirstPart,
		&r.AuthorID,
		&r.EmojiID,
		&r.MXID,
	))
}

func (r *Reaction) sqlVariables() []any {
	return []any{r.TeamID, r.ChannelID, r.MessageID, r.MessageFirstPart, r.AuthorID, r.EmojiID, r.MXID}
}

func (r *Reaction) Insert(ctx context.Context) error {
	return r.qh.Exec(ctx, insertReactionQuery, r.sqlVariables()...)
}

func (r *Reaction) Delete(ctx context.Context) error {
	return r.qh.Exec(ctx, deleteReactionQuery, r.TeamID, r.ChannelID, r.MessageID, r.AuthorID, r.EmojiID)
}
