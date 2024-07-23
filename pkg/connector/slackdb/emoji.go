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

package slackdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"sync"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/id"
)

var PostgresArrayWrapper func(any) interface {
	driver.Valuer
	sql.Scanner
}

type EmojiQuery struct {
	*dbutil.QueryHelper[*Emoji]
	locks     map[string]*sync.Mutex
	locksLock sync.Mutex
}

func newEmoji(_ *dbutil.QueryHelper[*Emoji]) *Emoji {
	return &Emoji{}
}

const (
	getEmojiBySlackIDQuery = `
		SELECT team_id, emoji_id, value, alias, image_mxc FROM emoji WHERE team_id=$1 AND emoji_id=$2
	`
	getEmojiByMXCQuery = `
		SELECT team_id, emoji_id, value, alias, image_mxc FROM emoji WHERE image_mxc=$1 ORDER BY alias NULLS FIRST
	`
	getEmojiCountInTeamQuery = `
		SELECT COUNT(*) FROM emoji WHERE team_id=$1
	`
	upsertEmojiQuery = `
		INSERT INTO emoji (team_id, emoji_id, value, alias, image_mxc)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (team_id, emoji_id) DO UPDATE
			SET value = excluded.value, alias = excluded.alias, image_mxc = excluded.image_mxc
	`
	renameEmojiQuery         = `UPDATE emoji SET emoji_id=$3 WHERE team_id=$1 AND emoji_id=$2`
	saveEmojiMXCQuery        = `UPDATE emoji SET image_mxc=$3 WHERE team_id=$1 AND (emoji_id=$2 OR alias=$2)`
	deleteEmojiQueryPostgres = `DELETE FROM emoji WHERE team_id=$1 AND emoji_id=ANY($2)`
	deleteEmojiQuerySQLite   = `DELETE FROM emoji WHERE team_id=? AND emoji_id IN (?)`
	pruneEmojiQueryPostgres  = `DELETE FROM emoji WHERE team_id=$1 AND emoji_id<>ALL($2)`
	pruneEmojiQuerySQLite    = `DELETE FROM emoji WHERE team_id=? AND emoji_id NOT IN (?)`
)

func (eq *EmojiQuery) WithLock(teamID string) func() {
	lock := eq.GetLock(teamID)
	lock.Lock()
	return lock.Unlock
}

func (eq *EmojiQuery) GetLock(teamID string) *sync.Mutex {
	eq.locksLock.Lock()
	lock, ok := eq.locks[teamID]
	if !ok {
		lock = &sync.Mutex{}
		eq.locks[teamID] = lock
	}
	eq.locksLock.Unlock()
	return lock
}

func (eq *EmojiQuery) GetEmojiCount(ctx context.Context, teamID string) (count int, err error) {
	err = eq.GetDB().QueryRow(ctx, getEmojiCountInTeamQuery, teamID).Scan(&count)
	return
}

func (eq *EmojiQuery) GetBySlackID(ctx context.Context, teamID, emojiID string) (*Emoji, error) {
	return eq.QueryOne(ctx, getEmojiBySlackIDQuery, teamID, emojiID)
}

func (eq *EmojiQuery) GetByMXC(ctx context.Context, mxc string) (*Emoji, error) {
	return eq.QueryOne(ctx, getEmojiByMXCQuery, &mxc)
}

func buildSQLiteEmojiDeleteQuery(baseQuery string, teamID string, emojiIDs ...string) (string, []any) {
	args := make([]any, 1+len(emojiIDs))
	args[0] = teamID
	for i, emojiID := range emojiIDs {
		args[i+1] = emojiID
	}
	placeholderRepeat := strings.Repeat("?,", len(emojiIDs))
	inClause := fmt.Sprintf("IN (%s)", placeholderRepeat[:len(placeholderRepeat)-1])
	query := strings.Replace(baseQuery, "IN (?)", inClause, 1)
	return query, args
}

func (eq *EmojiQuery) DeleteMany(ctx context.Context, teamID string, emojiIDs ...string) error {
	switch eq.GetDB().Dialect {
	case dbutil.Postgres:
		return eq.Exec(ctx, deleteEmojiQueryPostgres, teamID, emojiIDs)
	default:
		query, args := buildSQLiteEmojiDeleteQuery(deleteEmojiQuerySQLite, teamID, emojiIDs...)
		return eq.Exec(ctx, query, args...)
	}
}

func (eq *EmojiQuery) Prune(ctx context.Context, teamID string, emojiIDs ...string) error {
	switch eq.GetDB().Dialect {
	case dbutil.Postgres:
		return eq.Exec(ctx, pruneEmojiQueryPostgres, teamID, PostgresArrayWrapper(emojiIDs))
	default:
		query, args := buildSQLiteEmojiDeleteQuery(pruneEmojiQuerySQLite, teamID, emojiIDs...)
		return eq.Exec(ctx, query, args...)
	}
}

func (eq *EmojiQuery) Put(ctx context.Context, emoji *Emoji) error {
	return eq.Exec(ctx, upsertEmojiQuery, emoji.sqlVariables()...)
}

func (eq *EmojiQuery) Rename(ctx context.Context, emoji *Emoji, newID string) error {
	return eq.Exec(ctx, renameEmojiQuery, emoji.TeamID, emoji.EmojiID, newID)
}

func (eq *EmojiQuery) SaveMXC(ctx context.Context, emoji *Emoji) error {
	return eq.Exec(ctx, saveEmojiMXCQuery, emoji.TeamID, emoji.EmojiID, emoji.ImageMXC)
}

type Emoji struct {
	TeamID   string
	EmojiID  string
	Value    string
	Alias    string
	ImageMXC id.ContentURIString
}

func (e *Emoji) Scan(row dbutil.Scannable) (*Emoji, error) {
	var alias sql.NullString
	var imageURL sql.NullString
	err := row.Scan(&e.TeamID, &e.EmojiID, &e.Value, &alias, &imageURL)
	if err != nil {
		return nil, err
	}
	e.ImageMXC = id.ContentURIString(imageURL.String)
	e.Alias = alias.String
	return e, nil
}

func (e *Emoji) sqlVariables() []any {
	return []any{e.TeamID, e.EmojiID, e.Value, dbutil.StrPtr(e.Alias), dbutil.StrPtr(e.ImageMXC)}
}
