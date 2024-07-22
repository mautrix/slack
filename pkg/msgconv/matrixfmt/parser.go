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

package matrixfmt

import (
	"context"
	"fmt"
	"slices"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-slack/pkg/slackid"
)

type MatrixHTMLParser struct {
	br     *bridgev2.Bridge
	parser *format.HTMLParser
}

const (
	ctxPortalKey          = "portal"
	ctxAllowedMentionsKey = "allowed_mentions"
)

func (mhp *MatrixHTMLParser) pillConverter(displayname, mxid, eventID string, ctx format.Context) string {
	switch mxid[0] {
	case '@':
		allowedMentions := ctx.ReturnData[ctxAllowedMentionsKey].(*event.Mentions)
		if allowedMentions != nil && !slices.Contains(allowedMentions.UserIDs, id.UserID(mxid)) {
			// If `m.mentions` is set and doesn't contain this user, don't convert the mention
			// TODO does slack have some way to do silent mentions?
			return displayname
		}
		ghostID, ok := mhp.br.Matrix.ParseGhostMXID(id.UserID(mxid))
		if ok {
			_, userID := slackid.ParseUserID(ghostID)
			return fmt.Sprintf("<@%s>", userID)
		}
		user, err := mhp.br.GetExistingUserByMXID(ctx.Ctx, id.UserID(mxid))
		if err != nil {
			zerolog.Ctx(ctx.Ctx).Err(err).Msg("Failed to get user by MXID to convert mention")
		} else if user != nil {
			portal := ctx.ReturnData[ctxPortalKey].(*bridgev2.Portal)
			portalTeamID, _ := slackid.ParsePortalID(portal.ID)
			for _, userLoginID := range user.GetUserLoginIDs() {
				userTeamID, userID := slackid.ParseUserLoginID(userLoginID)
				if userTeamID == portalTeamID {
					return fmt.Sprintf("<@%s>", userID)
				}
			}
		}
	case '!':
		portal, err := mhp.br.GetPortalByMXID(ctx.Ctx, id.RoomID(mxid))
		if err != nil {
			zerolog.Ctx(ctx.Ctx).Err(err).Msg("Failed to get portal by MXID to convert mention")
		} else if portal != nil {
			_, channelID := slackid.ParsePortalID(portal.ID)
			if channelID != "" {
				return fmt.Sprintf("<#%s>", channelID)
			}
		}
	case '#':
		// TODO add aliases for rooms so they can be mentioned easily
	}
	return displayname
}

func New(br *bridgev2.Bridge) *MatrixHTMLParser {
	mhp := &MatrixHTMLParser{
		br: br,
	}
	mhp.parser = &format.HTMLParser{
		TabsToSpaces: 4,
		Newline:      "\n",

		PillConverter:           mhp.pillConverter,
		BoldConverter:           func(text string, _ format.Context) string { return fmt.Sprintf("*%s*", text) },
		ItalicConverter:         func(text string, _ format.Context) string { return fmt.Sprintf("_%s_", text) },
		StrikethroughConverter:  func(text string, _ format.Context) string { return fmt.Sprintf("~%s~", text) },
		MonospaceConverter:      func(text string, _ format.Context) string { return fmt.Sprintf("`%s`", text) },
		MonospaceBlockConverter: func(text, language string, _ format.Context) string { return fmt.Sprintf("```%s```", text) },
	}
	return mhp
}

func (mhp *MatrixHTMLParser) Parse(ctx context.Context, htmlData string, mentions *event.Mentions, portal *bridgev2.Portal) string {
	formatCtx := format.NewContext(ctx)
	formatCtx.ReturnData[ctxPortalKey] = portal
	formatCtx.ReturnData[ctxAllowedMentionsKey] = mentions
	return mhp.parser.Parse(htmlData, formatCtx)
}
