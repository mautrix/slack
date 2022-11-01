// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
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
	"database/sql"
	"errors"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/util/dbutil"
)

type BackfillQuery struct {
	db  *Database
	log log.Logger
}

func (bq *BackfillQuery) NewBackfillState(portalKey *PortalKey) *BackfillState {
	return &BackfillState{
		db:     bq.db,
		log:    bq.log,
		Portal: portalKey,
	}
}

const (
	getBackfillState = `
		SELECT team_id, channel_id, dispatched, backfill_complete, message_count, immediate_complete
		FROM backfill_state
		WHERE team_id=$1
			AND channel_id=$2
	`

	getNextUnfinishedBackfillState = `
		SELECT team_id, channel_id, dispatched, backfill_complete, message_count, immediate_complete
		FROM backfill_state
		WHERE dispatched IS FALSE
		AND backfill_complete IS FALSE
		LIMIT 1
	`
)

type BackfillState struct {
	db  *Database
	log log.Logger

	// Fields
	Portal            *PortalKey
	Dispatched        bool
	BackfillComplete  bool
	MessageCount      int
	ImmediateComplete bool
}

func (b *BackfillState) Scan(row dbutil.Scannable) *BackfillState {
	err := row.Scan(&b.Portal.TeamID, &b.Portal.ChannelID, &b.Dispatched, &b.BackfillComplete, &b.MessageCount, &b.ImmediateComplete)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			b.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	return b
}

func (b *BackfillState) Upsert() {
	_, err := b.db.Exec(`
		INSERT INTO backfill_state
			(team_id, channel_id, dispatched, backfill_complete, message_count, immediate_complete)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (team_id, channel_id)
		DO UPDATE SET
			dispatched=EXCLUDED.dispatched,
			backfill_complete=EXCLUDED.backfill_complete,
			message_count=EXCLUDED.message_count`,
		b.Portal.TeamID, b.Portal.ChannelID, b.Dispatched, b.BackfillComplete, b.MessageCount, b.ImmediateComplete)
	if err != nil {
		b.log.Warnfln("Failed to insert backfill state for %s: %v", b.Portal, err)
	}
}

func (b *BackfillState) SetDispatched(d bool) {
	b.Dispatched = d
	b.Upsert()
}

// Undispatch backfills so they can be retried in case the bridge crashed/was stopped during backfill
// Sent messages are tracked in the message and portal tables so this shouldn't lead to duplicate backfills
func (b *BackfillQuery) UndispatchAll() {
	_, err := b.db.Exec(`UPDATE backfill_state SET dispatched=FALSE`)
	if err != nil {
		b.log.Warnfln("Failed to undispatch all currently dispatched backfills")
	}
}

func (bq *BackfillQuery) GetBackfillState(portalKey *PortalKey) (backfillState *BackfillState) {
	rows, err := bq.db.Query(getBackfillState, portalKey.TeamID, portalKey.ChannelID)
	if err != nil || rows == nil {
		bq.log.Error(err)
		return
	}
	defer rows.Close()
	if rows.Next() {
		backfillState = bq.NewBackfillState(portalKey).Scan(rows)
	}
	return
}

func (bq *BackfillQuery) GetNextUnfinishedBackfillState() (backfillState *BackfillState) {
	rows, err := bq.db.Query(getNextUnfinishedBackfillState)
	if err != nil || rows == nil {
		bq.log.Error(err)
		return
	}
	defer rows.Close()
	if rows.Next() {
		backfillState = bq.NewBackfillState(&PortalKey{}).Scan(rows)
	}
	return
}
