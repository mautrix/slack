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

package main

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-slack/database"
)

const BackfillErrorDelay = 30 * time.Minute
const BackfillNoTasksDelay = 15 * time.Minute
const BackfillErrorCooldown = 1 * time.Hour

func (br *SlackBridge) BackfillLoop() {
	log := br.ZLog.With().Str("component", "backfill loop").Logger()
	ctx := log.WithContext(context.Background())
	for {
		task, err := br.DB.Backfill.GetNextTask(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to get backfill task from database")
			time.Sleep(BackfillErrorDelay)
			continue
		} else if task == nil {
			time.Sleep(BackfillNoTasksDelay)
			continue
		}
		br.DoBackfillTask(ctx, task)
		time.Sleep(time.Duration(br.Config.Bridge.Backfill.Incremental.PostBatchDelay) * time.Second)
	}
}

func (br *SlackBridge) DoBackfillTask(ctx context.Context, task *database.UserPortal) {
	log := zerolog.Ctx(ctx).With().
		Str("team_id", task.TeamID).
		Str("user_id", task.UserID).
		Str("channel_id", task.ChannelID).
		Stringer("user_mxid", task.UserMXID).
		Logger()
	ctx = log.WithContext(ctx)

	task.DispatchedAt = time.Now()
	task.CompletedAt = time.Time{}
	err := task.Update(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to set task dispatch time")
		return
	}
	defer func() {
		task.CompletedAt = time.Now()
		err = task.Update(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to set task completion time")
		}
	}()
	ut := br.GetUserTeamByID(database.UserTeamKey{
		TeamID: task.TeamID,
		UserID: task.UserID,
	}, "")
	if ut == nil {
		log.Error().Msg("User team not found")
		task.CooldownUntil = time.Now().Add(8 * time.Hour)
		return
	} else if ut.Token == "" {
		log.Debug().Msg("User team is not logged in (no token)")
		task.CooldownUntil = time.Now().Add(24 * time.Hour)
		return
	} else if ut.Client == nil {
		log.Debug().Msg("User team is not connected (no client)")
		task.CooldownUntil = time.Now().Add(24 * time.Hour)
		return
	}
	portal := br.GetPortalByID(database.PortalKey{
		TeamID:    task.TeamID,
		ChannelID: task.ChannelID,
	})
	if portal == nil {
		log.Error().Msg("Portal not found")
		task.CooldownUntil = time.Now().Add(8 * time.Hour)
		return
	} else if !portal.MoreToBackfill {
		log.Info().Msg("Portal is marked as not having more to backfill, marking backfill task as finished")
		task.Finished = true
		return
	}
}
