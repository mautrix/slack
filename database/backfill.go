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
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/util/dbutil"
)

type BackfillType int

const (
	BackfillImmediate BackfillType = 0
	BackfillForward   BackfillType = 100
	BackfillDeferred  BackfillType = 200
)

func (bt BackfillType) String() string {
	switch bt {
	case BackfillImmediate:
		return "IMMEDIATE"
	case BackfillForward:
		return "FORWARD"
	case BackfillDeferred:
		return "DEFERRED"
	}
	return "UNKNOWN"
}

type BackfillQuery struct {
	db  *Database
	log log.Logger

	backfillQueryLock sync.Mutex
}

func (bq *BackfillQuery) New() *Backfill {
	return &Backfill{
		db:     bq.db,
		log:    bq.log,
		Portal: &PortalKey{},
	}
}

func (bq *BackfillQuery) NewWithValues(backfillType BackfillType, priority int, portal *PortalKey, maxBatchEvents, maxTotalEvents, batchDelay int) *Backfill {
	return &Backfill{
		db:             bq.db,
		log:            bq.log,
		BackfillType:   backfillType,
		Priority:       priority,
		Portal:         portal,
		MaxBatchEvents: maxBatchEvents,
		MaxTotalEvents: maxTotalEvents,
		BatchDelay:     batchDelay,
	}
}

const (
	getNextBackfillQuery = `
		SELECT queue_id, type, priority, team_id, channel_id, max_batch_events, max_total_events, batch_delay
		FROM backfill_queue
		WHERE type IN (%s)
			AND (
				dispatch_time IS NULL
				OR (
					dispatch_time < $1
					AND completed_at IS NULL
				)
			)
		ORDER BY type, priority, queue_id
		LIMIT 1
	`
	getUnstartedOrInFlightQuery = `
		SELECT 1
		FROM backfill_queue
		WHERE type IN (%s)
			AND (dispatch_time IS NULL OR completed_at IS NULL)
		LIMIT 1
	`
)

// GetNext returns the next backfill to perform
func (bq *BackfillQuery) GetNext(backfillTypes []BackfillType) (backfill *Backfill) {
	bq.backfillQueryLock.Lock()
	defer bq.backfillQueryLock.Unlock()

	var types []string
	for _, backfillType := range backfillTypes {
		types = append(types, strconv.Itoa(int(backfillType)))
	}
	rows, err := bq.db.Query(fmt.Sprintf(getNextBackfillQuery, strings.Join(types, ",")), time.Now().Add(-15*time.Minute))
	if err != nil || rows == nil {
		bq.log.Errorfln("Failed to query next backfill queue job: %v", err)
		return
	}
	defer rows.Close()
	if rows.Next() {
		backfill = bq.New().Scan(rows)
	}
	return
}

func (bq *BackfillQuery) HasUnstartedOrInFlightOfType(backfillTypes []BackfillType) bool {
	if len(backfillTypes) == 0 {
		return false
	}

	bq.backfillQueryLock.Lock()
	defer bq.backfillQueryLock.Unlock()

	types := []string{}
	for _, backfillType := range backfillTypes {
		types = append(types, strconv.Itoa(int(backfillType)))
	}
	rows, err := bq.db.Query(fmt.Sprintf(getUnstartedOrInFlightQuery, strings.Join(types, ",")))
	if err != nil || rows == nil {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			bq.log.Warnfln("Failed to query backfill queue jobs: %v", err)
		}
		// No rows means that there are no unstarted or in flight backfill
		// requests.
		return false
	}
	defer rows.Close()
	return rows.Next()
}

// func (bq *BackfillQuery) DeleteAll(userID id.UserID) {
// 	bq.backfillQueryLock.Lock()
// 	defer bq.backfillQueryLock.Unlock()
// 	_, err := bq.db.Exec("DELETE FROM backfill_queue WHERE user_mxid=$1", userID)
// 	if err != nil {
// 		bq.log.Warnfln("Failed to delete backfill queue items for %s: %v", userID, err)
// 	}
// }

func (bq *BackfillQuery) DeleteAllForPortal(portalKey PortalKey) {
	bq.backfillQueryLock.Lock()
	defer bq.backfillQueryLock.Unlock()
	_, err := bq.db.Exec(`
		DELETE FROM backfill_queue
		WHERE team_id=$1
			AND channel_id=$2
	`, portalKey.TeamID, portalKey.ChannelID)
	if err != nil {
		bq.log.Warnfln("Failed to delete backfill queue items for %s: %v", portalKey, err)
	}
}

type Backfill struct {
	db  *Database
	log log.Logger

	// Fields
	QueueID        int
	BackfillType   BackfillType
	Priority       int
	Portal         *PortalKey
	MaxBatchEvents int
	MaxTotalEvents int
	BatchDelay     int
	DispatchTime   *time.Time
	CompletedAt    *time.Time
}

func (b *Backfill) String() string {
	return fmt.Sprintf("Backfill{QueueID: %d, BackfillType: %s, Priority: %d, Portal: %s, MaxBatchEvents: %d, MaxTotalEvents: %d, BatchDelay: %d, DispatchTime: %s, CompletedAt: %s}",
		b.QueueID, b.BackfillType, b.Priority, b.Portal, b.MaxBatchEvents, b.MaxTotalEvents, b.BatchDelay, b.CompletedAt, b.DispatchTime,
	)
}

func (b *Backfill) Scan(row dbutil.Scannable) *Backfill {
	var maxTotalEvents, batchDelay sql.NullInt32
	err := row.Scan(&b.QueueID, &b.BackfillType, &b.Priority, &b.Portal.TeamID, &b.Portal.ChannelID, &b.MaxBatchEvents, &maxTotalEvents, &batchDelay)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			b.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	b.MaxTotalEvents = int(maxTotalEvents.Int32)
	b.BatchDelay = int(batchDelay.Int32)
	return b
}

func (b *Backfill) Insert() {
	b.db.Backfill.backfillQueryLock.Lock()
	defer b.db.Backfill.backfillQueryLock.Unlock()

	rows, err := b.db.Query(`
		INSERT INTO backfill_queue
			(type, priority, team_id, channel_id, max_batch_events, max_total_events, batch_delay, dispatch_time, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING queue_id
	`, b.BackfillType, b.Priority, b.Portal.TeamID, b.Portal.ChannelID, b.MaxBatchEvents, b.MaxTotalEvents, b.BatchDelay, b.DispatchTime, b.CompletedAt)
	if err != nil || !rows.Next() {
		b.log.Warnfln("Failed to insert %v/%s with priority %d: %v", b.BackfillType, b.Portal, b.Priority, err)
		return
	}
	defer rows.Close()
	err = rows.Scan(&b.QueueID)
	if err != nil {
		b.log.Warnfln("Failed to insert %s/%s with priority %s: %v", b.BackfillType, b.Portal, b.Priority, err)
	}
}

func (b *Backfill) MarkDispatched() {
	b.db.Backfill.backfillQueryLock.Lock()
	defer b.db.Backfill.backfillQueryLock.Unlock()

	if b.QueueID == 0 {
		b.log.Errorfln("Cannot mark backfill as dispatched without queue_id. Maybe it wasn't actually inserted in the database?")
		return
	}
	_, err := b.db.Exec("UPDATE backfill_queue SET dispatch_time=$1 WHERE queue_id=$2", time.Now(), b.QueueID)
	if err != nil {
		b.log.Warnfln("Failed to mark %s/%d as dispatched: %v", b.BackfillType, b.Priority, err)
	}
}

func (b *Backfill) MarkDone() {
	b.db.Backfill.backfillQueryLock.Lock()
	defer b.db.Backfill.backfillQueryLock.Unlock()

	if b.QueueID == 0 {
		b.log.Errorfln("Cannot mark backfill done without queue_id. Maybe it wasn't actually inserted in the database?")
		return
	}
	_, err := b.db.Exec("UPDATE backfill_queue SET completed_at=$1 WHERE queue_id=$2", time.Now(), b.QueueID)
	if err != nil {
		b.log.Warnfln("Failed to mark %s/%d as complete: %v", b.BackfillType, b.Priority, err)
	}
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
		SELECT team_id, channel_id, processing_batch, backfill_complete
		FROM backfill_state
		WHERE team_id=$1
			AND channel_id=$2
	`
)

type BackfillState struct {
	db  *Database
	log log.Logger

	// Fields
	Portal           *PortalKey
	ProcessingBatch  bool
	BackfillComplete bool
}

func (b *BackfillState) Scan(row dbutil.Scannable) *BackfillState {
	err := row.Scan(&b.Portal.TeamID, &b.Portal.ChannelID, &b.ProcessingBatch, &b.BackfillComplete)
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
			(team_id, channel_id, processing_batch, backfill_complete)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (team_id, channel_id)
		DO UPDATE SET
			processing_batch=EXCLUDED.processing_batch,
			backfill_complete=EXCLUDED.backfill_complete`,
		b.Portal.TeamID, b.Portal.ChannelID, b.ProcessingBatch, b.BackfillComplete)
	if err != nil {
		b.log.Warnfln("Failed to insert backfill state for %s: %v", b.Portal, err)
	}
}

func (b *BackfillState) SetProcessingBatch(processing bool) {
	b.ProcessingBatch = processing
	b.Upsert()
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
