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

package slackid

import (
	"go.mau.fi/util/jsontime"
)

type PortalMetadata struct {
	// Only present for team portals, not channels
	TeamDomain  string `json:"team_domain,omitempty"`
	EditMaxAge  *int   `json:"edit_max_age,omitempty"`
	AllowDelete *bool  `json:"allow_delete,omitempty"`
}

type GhostMetadata struct {
	SlackUpdatedTS int64         `json:"slack_updated_ts"`
	LastSync       jsontime.Unix `json:"last_sync"`
	LastTimezone   string        `json:"last_timezone,omitempty"`
}

type UserLoginMetadata struct {
	Email       string `json:"email"`
	Token       string `json:"token"`
	CookieToken string `json:"cookie_token,omitempty"`
	AppToken    string `json:"app_token,omitempty"`
}

type MessageMetadata struct {
	CaptionMerged bool   `json:"caption_merged"`
	LastEditTS    string `json:"last_edit_ts"`
}
