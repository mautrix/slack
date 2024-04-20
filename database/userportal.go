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
	"database/sql"
	"time"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/id"
)

type BackfillQuery struct {
	*dbutil.QueryHelper[*UserPortal]
}

func newBackfillTask(qh *dbutil.QueryHelper[*UserPortal]) *UserPortal {
	return &UserPortal{qh: qh}
}

const (
	insertUserTeamPortalQuery = `
		INSERT INTO user_team_portal (team_id, user_id, channel_id, user_mxid, backfill_finished)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT DO NOTHING
	`
	deleteUserTeamPortalQuery = `
		DELETE FROM user_team_portal WHERE team_id=$1 AND user_id=$2 AND channel_id=$3
	`
	getNextBackfillTaskQuery = `
		SELECT team_id, user_id, channel_id, user_mxid, backfill_finished, backfill_priority, backfilled_count, backfill_dispatched_at, backfill_completed_at
		FROM user_team_portal
		WHERE backfill_finished=false AND (
			backfill_completed_at IS NOT NULL
			OR backfill_dispatched_at IS NULL
			OR backfill_dispatched_at < $1
		) AND (backfill_cooldown_until IS NULL OR backfill_cooldown_until < $2)
		ORDER BY backfill_dispatched_at LIMIT 1
	`
	updateBackfillTaskQuery = `
		UPDATE user_team_portal
		SET backfill_finished=$5, backfill_priority=$6, backfilled_count=$7,
		    backfill_dispatched_at=$8, backfill_completed_at=$9
		WHERE team_id=$1 AND user_id=$2 AND channel_id=$3 AND user_mxid=$4
	`
)

func (p *Portal) InsertUser(ctx context.Context, utk UserTeamMXIDKey, backfillFinished bool) error {
	return p.qh.Exec(ctx, insertUserTeamPortalQuery, utk.TeamID, utk.UserID, p.ChannelID, utk.UserMXID, backfillFinished)
}

func (p *Portal) DeleteUser(ctx context.Context, utk UserTeamMXIDKey) error {
	return p.qh.Exec(ctx, deleteUserTeamPortalQuery, utk.TeamID, utk.UserID, p.ChannelID)
}

const unfinishedBackfillBackoff = 2 * time.Hour

func (btq *BackfillQuery) GetNextTask(ctx context.Context) (*UserPortal, error) {
	return btq.QueryOne(ctx, getNextBackfillTaskQuery, time.Now().Add(-unfinishedBackfillBackoff).UnixMilli(), time.Now().UnixMilli())
}

type UserPortal struct {
	qh *dbutil.QueryHelper[*UserPortal]

	TeamID    string
	UserID    string
	ChannelID string
	UserMXID  id.UserID

	Finished        bool
	Priority        int
	BackfilledCount int
	DispatchedAt    time.Time
	CompletedAt     time.Time
	CooldownUntil   time.Time
}

func (b *UserPortal) sqlVariables() []any {
	return []any{
		b.TeamID, b.UserID, b.ChannelID, b.UserMXID,
		b.Finished,
		b.Priority,
		b.BackfilledCount,
		dbutil.UnixMilliPtr(b.DispatchedAt),
		dbutil.UnixMilliPtr(b.CompletedAt),
		dbutil.UnixMilliPtr(b.CooldownUntil),
	}
}

func (b *UserPortal) Scan(row dbutil.Scannable) (*UserPortal, error) {
	var dispatchedAt, completedAt, cooldownUntil sql.NullInt64
	err := row.Scan(&b.TeamID, &b.UserID, &b.ChannelID, &b.UserMXID, &b.Finished, &b.Priority, &b.BackfilledCount, &dispatchedAt, &completedAt, &cooldownUntil)
	if err != nil {
		return nil, err
	}
	if dispatchedAt.Valid {
		b.DispatchedAt = time.UnixMilli(dispatchedAt.Int64)
	}
	if completedAt.Valid {
		b.CompletedAt = time.UnixMilli(completedAt.Int64)
	}
	if cooldownUntil.Valid {
		b.CooldownUntil = time.UnixMilli(cooldownUntil.Int64)
	}
	return b, nil
}

func (b *UserPortal) Update(ctx context.Context) error {
	return b.qh.Exec(ctx, updateBackfillTaskQuery, b.sqlVariables()...)
}
