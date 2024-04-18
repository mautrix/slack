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

package msgconv

import (
	"context"

	"maunium.net/go/mautrix/id"

	"github.com/slack-go/slack"
	"go.mau.fi/mautrix-slack/database"
	"go.mau.fi/mautrix-slack/msgconv/matrixfmt"
	"go.mau.fi/mautrix-slack/msgconv/mrkdwn"
)

type PortalMethods interface {
	UploadMatrixMedia(ctx context.Context, data []byte, fileName, contentType string) (id.ContentURIString, error)
	DownloadMatrixMedia(ctx context.Context, uri id.ContentURIString) ([]byte, error)

	GetMentionedChannelID(ctx context.Context, roomID id.RoomID) string
	GetMentionedUserID(ctx context.Context, userID id.UserID) string
	GetMentionedRoomInfo(ctx context.Context, channelID string) (mxid id.RoomID, alias id.RoomAlias, name string)
	GetMentionedUserInfo(ctx context.Context, userID string) (mxid id.UserID, name string)

	GetMessageInfo(ctx context.Context, eventID id.EventID) (*database.Message, error)

	GetEmoji(ctx context.Context, emojiID string) string

	GetClient(ctx context.Context) *slack.Client
	GetData(ctx context.Context) *database.Portal
}

type MessageConverter struct {
	PortalMethods

	MatrixHTMLParser  *matrixfmt.MatrixHTMLParser
	SlackMrkdwnParser *mrkdwn.SlackMrkdwnParser

	ServerName  string
	MaxFileSize int
}

func New(pm PortalMethods, serverName string, maxFileSize int) *MessageConverter {
	return &MessageConverter{
		PortalMethods: pm,
		MaxFileSize:   maxFileSize,
		ServerName:    serverName,

		MatrixHTMLParser: matrixfmt.New(&matrixfmt.Params{
			GetUserID:    pm.GetMentionedUserID,
			GetChannelID: pm.GetMentionedChannelID,
		}),
		SlackMrkdwnParser: mrkdwn.New(&mrkdwn.Params{
			ServerName:     serverName,
			GetUserInfo:    pm.GetMentionedUserInfo,
			GetChannelInfo: pm.GetMentionedRoomInfo,
		}),
	}
}
