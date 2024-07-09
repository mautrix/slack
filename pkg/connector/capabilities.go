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

package connector

import (
	"context"

	"maunium.net/go/mautrix/bridgev2"
)

func (s *SlackConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{}
}

var roomCaps = &bridgev2.NetworkRoomCapabilities{
	FormattedText:          true,
	UserMentions:           true,
	RoomMentions:           true,
	LocationMessages:       false,
	Captions:               true,
	MaxTextLength:          40000,
	MaxCaptionLength:       40000,
	Threads:                true,
	Replies:                false,
	Edits:                  true,
	EditMaxAge:             0, // TODO workspaces can have edit max age limits
	Deletes:                true,
	DefaultFileRestriction: &bridgev2.FileRestriction{MaxSize: 1 * 1000 * 1000 * 1000},
	ReadReceipts:           false,
	Reactions:              true,
	ReactionCount:          0, // unlimited
}

func (s *SlackClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *bridgev2.NetworkRoomCapabilities {
	return roomCaps
}
