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
	"fmt"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/id"

	"github.com/slack-go/slack"
	"go.mau.fi/mautrix-slack/database"
	"go.mau.fi/mautrix-slack/msgconv"
)

type convertContextKey int

const (
	convertContextKeySource convertContextKey = iota
	convertContextKeyIntent
)

var _ msgconv.PortalMethods = (*Portal)(nil)

func (portal *Portal) GetEmoji(ctx context.Context, shortcode string) string {
	return ctx.Value(convertContextKeySource).(*UserTeam).GetEmoji(ctx, shortcode)
}

func (portal *Portal) GetMessageInfo(ctx context.Context, eventID id.EventID) (*database.Message, error) {
	msg, err := portal.bridge.DB.Message.GetByMXID(ctx, eventID)
	if msg != nil && msg.PortalKey != portal.PortalKey {
		return nil, errMessageInWrongRoom
	}
	return msg, err
}

func (portal *Portal) GetClient(ctx context.Context) *slack.Client {
	return ctx.Value(convertContextKeySource).(*UserTeam).Client
}

func (portal *Portal) GetData(ctx context.Context) *database.Portal {
	return portal.Portal
}

func (portal *Portal) UploadMatrixMedia(ctx context.Context, data []byte, fileName, contentType string) (id.ContentURIString, error) {
	intent := ctx.Value(convertContextKeyIntent).(*appservice.IntentAPI)
	req := mautrix.ReqUploadMedia{
		ContentBytes: data,
		ContentType:  contentType,
	}
	if portal.bridge.Config.Homeserver.AsyncMedia {
		uploaded, err := intent.UploadAsync(ctx, req)
		if err != nil {
			return "", err
		}
		return uploaded.ContentURI.CUString(), nil
	} else {
		uploaded, err := intent.UploadMedia(ctx, req)
		if err != nil {
			return "", err
		}
		return uploaded.ContentURI.CUString(), nil
	}
}

func (portal *Portal) DownloadMatrixMedia(ctx context.Context, uri id.ContentURIString) ([]byte, error) {
	parsedURI, err := uri.Parse()
	if err != nil {
		return nil, err
	}
	return portal.MainIntent().DownloadBytes(ctx, parsedURI)
}

func (portal *Portal) GetMentionedChannelID(_ context.Context, roomID id.RoomID) string {
	mentionedPortal := portal.bridge.GetPortalByMXID(roomID)
	if mentionedPortal != nil {
		return mentionedPortal.ChannelID
	}
	return ""
}

func (portal *Portal) GetMentionedUserID(_ context.Context, userID id.UserID) string {
	utk, ok := portal.bridge.ParsePuppetMXID(userID)
	if ok {
		return utk.UserID
	}
	userTeam := portal.Team.GetCachedUserByMXID(userID)
	if userTeam != nil {
		return userTeam.UserID
	}
	return ""
}

func (portal *Portal) GetMentionedUserInfo(ctx context.Context, userID string) (mxid id.UserID, name string) {
	user := portal.Team.GetCachedUserByID(userID)
	puppet := portal.Team.GetPuppetByID(userID)
	if puppet == nil {
		return
	} else if user != nil {
		name = puppet.Name
		if name == "" {
			name = user.UserMXID.String()
		}
		return user.UserMXID, name
	} else {
		if puppet.Name == "" {
			ut, ok := ctx.Value(convertContextKeySource).(*UserTeam)
			if ok {
				puppet.UpdateInfoIfNecessary(ctx, ut)
			}
		}
		return puppet.MXID, puppet.Name
	}
}

func (portal *Portal) GetMentionedRoomInfo(ctx context.Context, channelID string) (mxid id.RoomID, alias id.RoomAlias, name string) {
	mentionedPortal := portal.Team.GetPortalByID(channelID)
	if mentionedPortal == nil {
		return
	}
	mxid = mentionedPortal.MXID
	if mentionedPortal.Name == "" {
		ut, ok := ctx.Value(convertContextKeySource).(*UserTeam)
		if ok {
			mentionedPortal.UpdateInfo(ctx, ut, nil, false)
		}
	}
	name = mentionedPortal.Name
	if name == "" {
		name = fmt.Sprintf("#%s", channelID)
	}
	return
}
