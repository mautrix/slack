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

package main

import (
	"time"

	"go.mau.fi/mautrix-slack/database"
	log "maunium.net/go/maulogger/v2"
)

type BackfillQueue struct {
	BackfillQuery   *database.BackfillQuery
	reCheckChannels []chan bool
	log             log.Logger
}

func (bq *BackfillQueue) ReCheck() {
	bq.log.Infofln("Sending re-checks to %d channels", len(bq.reCheckChannels))
	for _, channel := range bq.reCheckChannels {
		go func(c chan bool) {
			c <- true
		}(channel)
	}
}

func (bq *BackfillQueue) GetNextBackfill(backfillTypes []database.BackfillType, waitForBackfillTypes []database.BackfillType, reCheckChannel chan bool) *database.Backfill {
	for {
		if !bq.BackfillQuery.HasUnstartedOrInFlightOfType(waitForBackfillTypes) {
			// check for immediate when dealing with deferred
			if backfill := bq.BackfillQuery.GetNext(backfillTypes); backfill != nil {
				backfill.MarkDispatched()
				return backfill
			}
		}

		select {
		case <-reCheckChannel:
		case <-time.After(time.Minute):
		}
	}
}

func (bridge *SlackBridge) HandleBackfillRequestsLoop(backfillTypes []database.BackfillType, waitForBackfillTypes []database.BackfillType) {
	reCheckChannel := make(chan bool)
	bridge.BackfillQueue.reCheckChannels = append(bridge.BackfillQueue.reCheckChannels, reCheckChannel)

	for {
		req := bridge.BackfillQueue.GetNextBackfill(backfillTypes, waitForBackfillTypes, reCheckChannel)
		bridge.Log.Infofln("Handling backfill request %s", req)

		portal := bridge.GetPortalByID(*req.Portal)

		bridge.backfillInChunks(req, portal)
		req.MarkDone()
	}
}
