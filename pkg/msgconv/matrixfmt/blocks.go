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
	"strconv"
	"strings"

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"
	"golang.org/x/net/html"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-slack/pkg/connector/slackdb"
	"go.mau.fi/mautrix-slack/pkg/slackid"
)

const (
	ctxPortalKey          = "portal"
	ctxAllowedMentionsKey = "allowed_mentions"
)

type Context struct {
	Ctx        context.Context
	ReturnData map[string]any
	TagStack   format.TagStack
	Style      slack.RichTextSectionTextStyle
	Link       string

	PreserveWhitespace bool
}

func NewContext(ctx context.Context) Context {
	return Context{
		Ctx:        ctx,
		ReturnData: map[string]any{},
		TagStack:   make(format.TagStack, 0, 4),
	}
}

func (ctx Context) WithTag(tag string) Context {
	ctx.TagStack = append(ctx.TagStack, tag)
	return ctx
}

func (ctx Context) WithWhitespace() Context {
	ctx.PreserveWhitespace = true
	return ctx
}

func (ctx Context) StyleBold() Context {
	ctx.Style.Bold = true
	return ctx
}

func (ctx Context) StyleItalic() Context {
	ctx.Style.Italic = true
	return ctx
}

func (ctx Context) StyleStrike() Context {
	ctx.Style.Strike = true
	return ctx
}

func (ctx Context) StyleCode() Context {
	ctx.Style.Code = true
	return ctx
}

func (ctx Context) StylePtr() *slack.RichTextSectionTextStyle {
	if !ctx.Style.Bold && !ctx.Style.Italic && !ctx.Style.Strike && !ctx.Style.Code && !ctx.Style.Highlight && !ctx.Style.ClientHighlight && !ctx.Style.Unlink {
		return nil
	}
	return &ctx.Style
}

func (ctx Context) WithLink(link string) Context {
	ctx.Link = link
	return ctx
}

// HTMLParser is a somewhat customizable Matrix HTML parser.
type HTMLParser struct {
	br *bridgev2.Bridge
	db *slackdb.SlackDB
}

func New2(br *bridgev2.Bridge, db *slackdb.SlackDB) *HTMLParser {
	return &HTMLParser{br: br, db: db}
}

func (parser *HTMLParser) GetMentionedUserID(mxid id.UserID, ctx Context) string {
	allowedMentions := ctx.ReturnData[ctxAllowedMentionsKey].(*event.Mentions)
	if allowedMentions != nil && !slices.Contains(allowedMentions.UserIDs, id.UserID(mxid)) {
		// If `m.mentions` is set and doesn't contain this user, don't convert the mention
		// TODO does slack have some way to do silent mentions?
		return ""
	}
	ghostID, ok := parser.br.Matrix.ParseGhostMXID(mxid)
	if ok {
		_, userID := slackid.ParseUserID(ghostID)
		return userID
	}
	user, err := parser.br.GetExistingUserByMXID(ctx.Ctx, mxid)
	if err != nil {
		zerolog.Ctx(ctx.Ctx).Err(err).Msg("Failed to get user by MXID to convert mention")
	} else if user != nil {
		portal := ctx.ReturnData[ctxPortalKey].(*bridgev2.Portal)
		portalTeamID, _ := slackid.ParsePortalID(portal.ID)
		for _, userLoginID := range user.GetUserLoginIDs() {
			userTeamID, userID := slackid.ParseUserLoginID(userLoginID)
			if userTeamID == portalTeamID {
				return userID
			}
		}
	}
	return ""
}

func (parser *HTMLParser) GetMentionedChannelID(mxid id.RoomID, ctx Context) string {
	portal, err := parser.br.GetPortalByMXID(ctx.Ctx, mxid)
	if err != nil {
		zerolog.Ctx(ctx.Ctx).Err(err).Msg("Failed to get portal by MXID to convert mention")
	} else if portal != nil {
		_, channelID := slackid.ParsePortalID(portal.ID)
		return channelID
	}
	return ""
}

func (parser *HTMLParser) GetMentionedEventLink(roomID id.RoomID, eventID id.EventID, ctx Context) string {
	message, err := parser.br.DB.Message.GetPartByMXID(ctx.Ctx, eventID)
	if err != nil {
		zerolog.Ctx(ctx.Ctx).Err(err).Msg("Failed to get message by MXID to convert link")
		return ""
	} else if message == nil {
		return ""
	}
	teamID, channelID, timestamp, ok := slackid.ParseMessageID(message.ID)
	if !ok {
		return ""
	}
	teamPortal, err := parser.br.GetPortalByKey(ctx.Ctx, networkid.PortalKey{ID: slackid.MakeTeamPortalID(teamID)})
	if err != nil {
		zerolog.Ctx(ctx.Ctx).Err(err).Msg("Failed to get team portal to convert message link")
		return ""
	}
	teamDomain := teamPortal.Metadata.(*slackid.PortalMetadata).TeamDomain
	timestampWithoutDot := strings.ReplaceAll(timestamp, ".", "")
	return fmt.Sprintf("https://%s.slack.com/archives/%s/p%s", teamDomain, channelID, timestampWithoutDot)
}

func (parser *HTMLParser) maybeGetAttribute(node *html.Node, attribute string) (string, bool) {
	for _, attr := range node.Attr {
		if attr.Key == attribute {
			return attr.Val, true
		}
	}
	return "", false
}

func (parser *HTMLParser) getAttribute(node *html.Node, attribute string) string {
	val, _ := parser.maybeGetAttribute(node, attribute)
	return val
}

func listDepth(ts format.TagStack) (depth int) {
	for _, tag := range ts {
		if tag == "ol" || tag == "ul" {
			depth++
		}
	}
	return
}

func (parser *HTMLParser) listToElement(node *html.Node, ctx Context) []slack.RichTextElement {
	style := slack.RTEListBullet
	offset := 0
	depth := listDepth(ctx.TagStack) - 1
	if node.Data == "ol" {
		style = slack.RTEListOrdered
		startStr := parser.getAttribute(node, "start")
		if len(startStr) > 0 {
			var err error
			offset, err = strconv.Atoi(startStr)
			if err == nil {
				offset--
			}
		}
	}
	border := 0
	if ctx.TagStack.Has("blockquote") {
		border = 1
	}
	var output []slack.RichTextElement
	var elements []slack.RichTextSection
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode || child.Data != "li" {
			continue
		}
		item, sublists := parser.nodeAndSiblingsToElement(child.FirstChild, ctx)
		if len(item) > 0 {
			elements = append(elements, *slack.NewRichTextSection(item...))
		}
		if len(sublists) > 0 {
			if len(elements) > 0 {
				output = append(output, slack.NewRichTextList(style, depth, offset, border, elements...))
			}
			offset += len(elements)
			elements = nil
			output = append(output, sublists...)
		}
	}
	if len(elements) > 0 {
		output = append(output, slack.NewRichTextList(style, depth, offset, border, elements...))
	}
	return output
}

func (parser *HTMLParser) applyBasicFormat(node *html.Node, ctx Context) Context {
	switch node.Data {
	case "b", "strong":
		ctx = ctx.StyleBold()
	case "i", "em":
		ctx = ctx.StyleItalic()
	case "s", "del", "strike":
		ctx = ctx.StyleStrike()
	case "tt", "code":
		ctx = ctx.StyleCode()
	case "a":
		ctx = ctx.WithLink(parser.getAttribute(node, "href"))
	}
	return ctx
}

func (parser *HTMLParser) tagToElement(node *html.Node, ctx Context) ([]slack.RichTextSectionElement, []slack.RichTextElement) {
	ctx = ctx.WithTag(node.Data)
	switch node.Data {
	case "br":
		return []slack.RichTextSectionElement{slack.NewRichTextSectionTextElement("\n", ctx.StylePtr())}, nil
	case "hr":
		return nil, []slack.RichTextElement{slack.NewRichTextSection(slack.NewRichTextSectionTextElement("---", ctx.StylePtr()))}
	case "b", "strong", "i", "em", "s", "strike", "del", "u", "ins", "tt", "code", "a", "span", "font":
		ctx = parser.applyBasicFormat(node, ctx)
		return parser.nodeAndSiblingsToElement(node.FirstChild, ctx)
	case "img":
		src := parser.getAttribute(node, "src")
		dbEmoji, err := parser.db.Emoji.GetByMXC(ctx.Ctx, src)
		if err != nil {
			zerolog.Ctx(ctx.Ctx).Err(err).Msg("Failed to get emoji by MXC to convert image")
		} else if dbEmoji != nil {
			return []slack.RichTextSectionElement{slack.NewRichTextSectionEmojiElement(dbEmoji.EmojiID, 0, ctx.StylePtr())}, nil
		}
		if alt := parser.getAttribute(node, "alt"); alt != "" {
			return []slack.RichTextSectionElement{slack.NewRichTextSectionTextElement(alt, ctx.StylePtr())}, nil
		} else {
			return nil, nil
		}
	case "h1", "h2", "h3", "h4", "h5", "h6":
		length := int(node.Data[1] - '0')
		prefix := strings.Repeat("#", length) + " "
		ctx = ctx.StyleBold()
		sectionElems, elems := parser.nodeAndSiblingsToElement(node.FirstChild, ctx)
		sectionElems = append([]slack.RichTextSectionElement{slack.NewRichTextSectionTextElement(prefix, ctx.StylePtr())}, sectionElems...)
		elems = append([]slack.RichTextElement{slack.NewRichTextSection(sectionElems...)}, elems...)
		return nil, elems
	case "p", "blockquote":
		sectionElems, elems := parser.nodeAndSiblingsToElement(node.FirstChild, ctx)
		if len(sectionElems) > 0 {
			var firstElem slack.RichTextElement
			if ctx.TagStack.Has("blockquote") {
				border := 0
				if node.Data == "blockquote" && slices.Index(ctx.TagStack, "blockquote") < len(ctx.TagStack)-1 {
					border = 1
				}
				firstElem = slack.NewRichTextQuote(border, sectionElems...)
			} else {
				firstElem = slack.NewRichTextSection(sectionElems...)
			}
			elems = append([]slack.RichTextElement{firstElem}, elems...)
		}
		return nil, elems
	case "ol", "ul":
		return nil, parser.listToElement(node, ctx)
	case "pre":
		//var language string
		if node.FirstChild != nil && node.FirstChild.Type == html.ElementNode && node.FirstChild.Data == "code" {
			//class := parser.getAttribute(node.FirstChild, "class")
			//if strings.HasPrefix(class, "language-") {
			//	language = class[len("language-"):]
			//}
			node = node.FirstChild
		}
		sectionElems, elems := parser.nodeAndSiblingsToElement(node.FirstChild, ctx.WithWhitespace())
		border := 0
		if ctx.TagStack.Has("blockquote") {
			border = 1
		}
		elems = append([]slack.RichTextElement{slack.NewRichTextPreformatted(border, sectionElems...)}, elems...)
		return nil, elems
	default:
		return parser.nodeAndSiblingsToElement(node.FirstChild, ctx)
	}
}

func (parser *HTMLParser) textToElement(text string, ctx Context) slack.RichTextSectionElement {
	if ctx.Link != "" {
		parsedMatrix, _ := id.ParseMatrixURIOrMatrixToURL(ctx.Link)
		if parsedMatrix != nil {
			if parsedMatrix.Sigil1 == '@' {
				userID := parser.GetMentionedUserID(parsedMatrix.UserID(), ctx)
				if userID != "" {
					return slack.NewRichTextSectionUserElement(userID, ctx.StylePtr())
				}
			} else if parsedMatrix.Sigil1 == '!' && parsedMatrix.Sigil2 == 0 {
				channelID := parser.GetMentionedChannelID(parsedMatrix.RoomID(), ctx)
				if channelID != "" {
					return slack.NewRichTextSectionChannelElement(channelID, ctx.StylePtr())
				}
			} else if parsedMatrix.Sigil1 == '!' && parsedMatrix.Sigil2 == '$' {
				eventLink := parser.GetMentionedEventLink(parsedMatrix.RoomID(), parsedMatrix.EventID(), ctx)
				if eventLink != "" {
					return slack.NewRichTextSectionLinkElement(ctx.Link, text, ctx.StylePtr())
				}
			}
			// TODO add aliases for rooms so they can be mentioned easily
			//else if parsedMatrix.Sigil1 == '#' {
		}
		return slack.NewRichTextSectionLinkElement(ctx.Link, text, ctx.StylePtr())
	}
	return slack.NewRichTextSectionTextElement(text, ctx.StylePtr())
}

func (parser *HTMLParser) textToElements(text string, ctx Context) []slack.RichTextSectionElement {
	if !ctx.PreserveWhitespace {
		text = strings.Replace(text, "\n", "", -1)
	}
	if text == "" {
		return nil
	}
	mentions := ctx.ReturnData[ctxAllowedMentionsKey].(*event.Mentions)
	if mentions != nil && mentions.Room && strings.Contains(text, "@room") && !ctx.TagStack.Has("code") {
		parts := strings.Split(text, "@room")
		elems := make([]slack.RichTextSectionElement, len(parts)*2-1)
		for i, part := range parts {
			elems[i*2] = parser.textToElement(part, ctx)
			if i < len(parts)-1 {
				elems[i*2+1] = slack.NewRichTextSectionBroadcastElement(slack.RichTextBroadcastRangeChannel)
			}
		}
	}
	return []slack.RichTextSectionElement{parser.textToElement(text, ctx)}
}

func (parser *HTMLParser) nodeToElement(node *html.Node, ctx Context) ([]slack.RichTextSectionElement, []slack.RichTextElement) {
	switch node.Type {
	case html.TextNode:
		return parser.textToElements(node.Data, ctx), nil
	case html.ElementNode:
		return parser.tagToElement(node, ctx)
	case html.DocumentNode:
		return parser.nodeAndSiblingsToElement(node.FirstChild, ctx)
	default:
		return nil, nil
	}
}

func (parser *HTMLParser) nodeAndSiblingsToElement(node *html.Node, ctx Context) (sectionElems []slack.RichTextSectionElement, elems []slack.RichTextElement) {
	sectionElemsLocked := false
	var sectionCollector []slack.RichTextSectionElement
	for ; node != nil; node = node.NextSibling {
		se, e := parser.nodeToElement(node, ctx)
		if len(se) > 0 {
			if sectionElemsLocked {
				sectionCollector = append(sectionCollector, se...)
			} else {
				sectionElems = append(sectionElems, se...)
			}
		}
		if len(e) > 0 {
			sectionElemsLocked = true
			if len(sectionCollector) > 0 {
				elems = append(elems, slack.NewRichTextSection(sectionCollector...))
				sectionCollector = nil
			}
			elems = append(elems, e...)
		}
	}
	if len(sectionCollector) > 0 {
		elems = append(elems, slack.NewRichTextSection(sectionCollector...))
	}
	return
}

func (parser *HTMLParser) nodeToBlock(node *html.Node, ctx Context) *slack.RichTextBlock {
	sectionElems, elems := parser.nodeToElement(node, ctx)
	if len(sectionElems) > 0 {
		elems = append([]slack.RichTextElement{slack.NewRichTextSection(sectionElems...)}, elems...)
	}
	return slack.NewRichTextBlock("", elems...)
}

// Parse converts Matrix HTML into text using the settings in this parser.
func (parser *HTMLParser) Parse(ctx context.Context, htmlData string, mentions *event.Mentions, portal *bridgev2.Portal) *slack.RichTextBlock {
	formatCtx := NewContext(ctx)
	formatCtx.ReturnData[ctxPortalKey] = portal
	formatCtx.ReturnData[ctxAllowedMentionsKey] = mentions
	node, _ := html.Parse(strings.NewReader(htmlData))
	return parser.nodeToBlock(node, formatCtx)
}
