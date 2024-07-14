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
	_ "embed"
	"fmt"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-slack/pkg/connector"
	"go.mau.fi/mautrix-slack/pkg/slackid"
)

const legacyMigrateRenameTables = `
ALTER TABLE portal RENAME TO portal_old;
ALTER TABLE puppet RENAME TO puppet_old;
ALTER TABLE "user" RENAME TO user_old;
ALTER TABLE user_team RENAME TO user_team_old;
ALTER TABLE user_team_portal RENAME TO user_team_portal_old;
ALTER TABLE message RENAME TO message_old;
ALTER TABLE reaction RENAME TO reaction_old;
ALTER TABLE attachment RENAME TO attachment_old;
ALTER TABLE team_info RENAME TO team_info_old;
ALTER TABLE backfill_state RENAME TO backfill_state_old;
ALTER TABLE emoji RENAME TO emoji_old;
`

//go:embed legacymigrate.sql
var legacyMigrateCopyData string

func postMigrate(ctx context.Context) error {
	wasMigrated, err := m.DB.TableExists(ctx, "database_was_migrated")
	if err != nil {
		return fmt.Errorf("failed to check if database_was_migrated table exists: %w", err)
	} else if !wasMigrated {
		return nil
	}
	zerolog.Ctx(ctx).Info().Msg("Doing post-migration updates to Matrix rooms")

	portals, err := m.Bridge.GetAllPortalsWithMXID(ctx)
	if err != nil {
		return fmt.Errorf("failed to get all portals: %w", err)
	}
	for _, portal := range portals {
		switch portal.RoomType {
		case database.RoomTypeDM:
			teamID, _ := slackid.ParsePortalID(portal.ID)
			otherUserID := portal.Metadata.(*connector.PortalMetadata).OtherUserID
			if otherUserID == "" {
				zerolog.Ctx(ctx).Warn().Msg("DM portal has no other user ID")
			} else {
				ghost, err := m.Bridge.GetGhostByID(ctx, slackid.MakeUserID(teamID, otherUserID))
				if err != nil {
					return fmt.Errorf("failed to get ghost for %s: %w", otherUserID, err)
				}
				mx := ghost.Intent.(*matrix.ASIntent).Matrix
				err = m.Matrix.Bot.EnsureJoined(ctx, portal.MXID, appservice.EnsureJoinedParams{
					BotOverride: mx.Client,
				})
				if err != nil {
					zerolog.Ctx(ctx).Warn().Err(err).Stringer("room_id", portal.MXID).Msg("Failed to ensure bot is joined to DM")
				}
				pls, err := mx.PowerLevels(ctx, portal.MXID)
				if err != nil {
					zerolog.Ctx(ctx).Warn().Err(err).Stringer("room_id", portal.MXID).Msg("Failed to get power levels in room")
				} else {
					userLevel := pls.GetUserLevel(mx.UserID)
					pls.EnsureUserLevel(m.Matrix.Bot.UserID, userLevel)
					if userLevel > 50 {
						pls.SetUserLevel(mx.UserID, 50)
					}
					_, err = mx.SetPowerLevels(ctx, portal.MXID, pls)
					if err != nil {
						zerolog.Ctx(ctx).Warn().Err(err).Stringer("room_id", portal.MXID).Msg("Failed to set power levels")
					}
				}
			}
		}
		_, err = m.Matrix.Bot.SendStateEvent(ctx, portal.MXID, event.StateElementFunctionalMembers, "", &event.ElementFunctionalMembersContent{
			ServiceMembers: []id.UserID{m.Matrix.Bot.UserID},
		})
		if err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Stringer("room_id", portal.MXID).Msg("Failed to set service members")
		}
	}

	_, err = m.DB.Exec(ctx, "DROP TABLE database_was_migrated")
	if err != nil {
		return fmt.Errorf("failed to drop database_was_migrated table: %w", err)
	}
	zerolog.Ctx(ctx).Info().Msg("Post-migration updates complete")
	return nil
}
