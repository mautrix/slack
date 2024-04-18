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

	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

type Params struct {
	GetUserID    func(context.Context, id.UserID) string
	GetChannelID func(context.Context, id.RoomID) string
}

type MatrixHTMLParser struct {
	parser *format.HTMLParser
	*Params
}

func (mhp *MatrixHTMLParser) pillConverter(displayname, mxid, eventID string, ctx format.Context) string {
	switch mxid[0] {
	case '@':
		userID := mhp.GetUserID(ctx.Ctx, id.UserID(mxid))
		if userID != "" {
			return fmt.Sprintf("<@%s>", userID)
		}
	case '!':
		channelID := mhp.GetChannelID(ctx.Ctx, id.RoomID(mxid))
		if channelID != "" {
			return fmt.Sprintf("<#%s>", channelID)
		}
	case '#':
		// TODO add aliases for rooms so they can be mentioned easily
	}
	return displayname
}

func New(params *Params) *MatrixHTMLParser {
	mhp := &MatrixHTMLParser{
		Params: params,
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

func (mhp *MatrixHTMLParser) Parse(ctx context.Context, htmlData string) string {
	return mhp.parser.Parse(htmlData, format.NewContext(ctx))
}
