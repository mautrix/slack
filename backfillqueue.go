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

func (bq *BackfillQueue) GetNextBackfill(reCheckChannel chan bool) *database.BackfillState {
	for {
		if backfill := bq.BackfillQuery.GetNextUnfinishedBackfillState(); backfill != nil {
			bq.log.Debugfln("Found unfinished backfill state for %s", backfill.Portal)
			backfill.SetDispatched(true)
			return backfill
		}
		bq.log.Debugfln("Didn't find unfinished backfills, waiting")

		select {
		case <-reCheckChannel:
			bq.log.Debugln("Rechecking backfills on request")
		case <-time.After(time.Minute):
			bq.log.Debugln("Rechecking backfills after one minute")
		}
	}
}

func (bridge *SlackBridge) HandleBackfillRequestsLoop() {
	reCheckChannel := make(chan bool)
	bridge.BackfillQueue.reCheckChannels = append(bridge.BackfillQueue.reCheckChannels, reCheckChannel)

	for {
		state := bridge.BackfillQueue.GetNextBackfill(reCheckChannel)
		bridge.Log.Infofln("Handling backfill %s", state)

		portal := bridge.GetPortalByID(*state.Portal)

		bridge.backfillInChunks(state, portal)
		//req.MarkDone()
	}
}
