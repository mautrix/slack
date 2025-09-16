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
	"net"
	"net/http"
	"time"

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-slack/pkg/connector/slackdb"
	"go.mau.fi/mautrix-slack/pkg/msgconv/matrixfmt"
	"go.mau.fi/mautrix-slack/pkg/msgconv/mrkdwn"
	"go.mau.fi/mautrix-slack/pkg/slackid"
)

type MessageConverter struct {
	Bridge *bridgev2.Bridge
	HTTP   http.Client

	MatrixHTMLParser  *matrixfmt.HTMLParser
	SlackMrkdwnParser *mrkdwn.SlackMrkdwnParser

	ServerName  string
	MaxFileSize int
}

type contextKey int

const (
	contextKeyPortal contextKey = iota
	contextKeySource
)

type SlackClientProvider interface {
	GetClient() *slack.Client
	GetEmoji(context.Context, string) (string, bool)
	GetChannelInfoForMention(context.Context, string) (string, *bridgev2.Portal, error)
}

func (mc *MessageConverter) GetMentionedUserInfo(ctx context.Context, userID string) (mxid id.UserID, name string) {
	source := ctx.Value(contextKeySource).(*bridgev2.UserLogin)
	teamID, loggedInUserID := slackid.ParseUserLoginID(source.ID)
	ghost, err := mc.Bridge.GetGhostByID(ctx, slackid.MakeUserID(teamID, userID))
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get mentioned ghost")
	} else if ghost != nil {
		/*if ghost.Name == "" {
			// TODO update ghost info
		}*/
		name = ghost.Name
		mxid = ghost.Intent.GetMXID()
	}
	if userID == loggedInUserID {
		mxid = source.UserMXID
	} else if otherUserLogin := mc.Bridge.GetCachedUserLoginByID(slackid.MakeUserLoginID(teamID, userID)); otherUserLogin != nil {
		mxid = otherUserLogin.UserMXID
	}
	return
}

func (mc *MessageConverter) GetMentionedRoomInfo(ctx context.Context, channelID string) (mxid id.RoomID, alias id.RoomAlias, name string) {
	source := ctx.Value(contextKeySource).(*bridgev2.UserLogin)
	teamID, _ := slackid.ParseUserLoginID(source.ID)
	portal, err := mc.Bridge.GetExistingPortalByKey(ctx, networkid.PortalKey{
		ID:       slackid.MakePortalID(teamID, channelID),
		Receiver: source.ID,
	})
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get mentioned portal")
		return
	} else if portal == nil || portal.Name == "" {
		name, _, err = source.Client.(SlackClientProvider).GetChannelInfoForMention(ctx, channelID)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to get info of mentioned channel")
		}
		return
	} else {
		return portal.MXID, "", portal.Name
	}
}

func New(br *bridgev2.Bridge, db *slackdb.SlackDB) *MessageConverter {
	mc := &MessageConverter{
		Bridge: br,
		HTTP: http.Client{
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 20 * time.Second,
				ForceAttemptHTTP2:     true,
			},
			Timeout: 60 * time.Second,
		},

		MaxFileSize: 50 * 1024 * 1024,
		ServerName:  br.Matrix.ServerName(),

		MatrixHTMLParser: matrixfmt.New2(br, db),
	}
	mc.SlackMrkdwnParser = mrkdwn.New(&mrkdwn.Params{
		ServerName:     br.Matrix.ServerName(),
		GetUserInfo:    mc.GetMentionedUserInfo,
		GetChannelInfo: mc.GetMentionedRoomInfo,
	})
	return mc
}
