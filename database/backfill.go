// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2021, 2022 Tulir Asokan, Sumner Evans, Max Sandholm
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
)

type BackfillQuery struct {
	*dbutil.QueryHelper[*BackfillState]
}

func newBackfill(qh *dbutil.QueryHelper[*BackfillState]) *BackfillState {
	return &BackfillState{qh: qh}
}

const (
	getBackfillState = `
		SELECT team_id, channel_id, dispatched, backfill_complete, message_count, immediate_complete
		FROM backfill_state
		WHERE team_id=$1 AND channel_id=$2
	`
	getNextUnfinishedBackfillState = `
		SELECT team_id, channel_id, dispatched, backfill_complete, message_count, immediate_complete
		FROM backfill_state
		WHERE dispatched IS FALSE
		AND backfill_complete IS FALSE
		ORDER BY CASE WHEN immediate_complete THEN 0 ELSE 1 END, message_count
		LIMIT 1
	`
	undispatchAllBackfillsQuery = `UPDATE backfill_state SET dispatched=false`
	upsertBackfillStateQuery    = `
		INSERT INTO backfill_state
			(team_id, channel_id, dispatched, backfill_complete, message_count, immediate_complete)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (team_id, channel_id)
		DO UPDATE SET
			dispatched=EXCLUDED.dispatched,
			backfill_complete=EXCLUDED.backfill_complete,
			message_count=EXCLUDED.message_count,
			immediate_complete=EXCLUDED.immediate_complete
	`
)

// UndispatchAll undispatches backfills so they can be retried in case the bridge crashed/was stopped during backfill
// Sent messages are tracked in the message and portal tables so this shouldn't lead to duplicate backfills
func (bq *BackfillQuery) UndispatchAll(ctx context.Context) error {
	return bq.Exec(ctx, undispatchAllBackfillsQuery)
}

func (bq *BackfillQuery) GetBackfillState(ctx context.Context, portalKey PortalKey) (*BackfillState, error) {
	return bq.QueryOne(ctx, getBackfillState, portalKey.TeamID, portalKey.ChannelID)
}

func (bq *BackfillQuery) GetNextUnfinishedBackfillState(ctx context.Context) (*BackfillState, error) {
	return bq.QueryOne(ctx, getNextUnfinishedBackfillState)
}

type BackfillState struct {
	qh *dbutil.QueryHelper[*BackfillState]

	Portal            PortalKey
	Dispatched        bool
	BackfillComplete  bool
	MessageCount      int
	ImmediateComplete bool
}

func (b *BackfillState) Scan(row dbutil.Scannable) (*BackfillState, error) {
	return dbutil.ValueOrErr(b, row.Scan(
		&b.Portal.TeamID, &b.Portal.ChannelID, &b.Dispatched, &b.BackfillComplete, &b.MessageCount, &b.ImmediateComplete,
	))
}

func (b *BackfillState) sqlVariables() []any {
	return []any{b.Portal.TeamID, b.Portal.ChannelID, b.Dispatched, b.BackfillComplete, b.MessageCount, b.ImmediateComplete}
}

func (b *BackfillState) Upsert(ctx context.Context) error {
	return b.qh.Exec(ctx, upsertBackfillStateQuery, b.sqlVariables()...)
}

func (b *BackfillState) SetDispatched(ctx context.Context, d bool) error {
	b.Dispatched = d
	return b.Upsert(ctx)
}
