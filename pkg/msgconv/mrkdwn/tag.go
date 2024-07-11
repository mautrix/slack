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

package mrkdwn

import (
	"context"
	"fmt"
	"html"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	goldmarkUtil "github.com/yuin/goldmark/util"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type astSlackTag struct {
	ast.BaseInline

	label string
}

var _ ast.Node = (*astSlackTag)(nil)
var astKindSlackTag = ast.NewNodeKind("SlackTag")

func (n *astSlackTag) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

func (n *astSlackTag) Kind() ast.NodeKind {
	return astKindSlackTag
}

type astSlackUserMention struct {
	astSlackTag

	userID string
	mxid   id.UserID
	name   string
}

func (n *astSlackUserMention) String() string {
	if n.label != "" {
		return fmt.Sprintf("<@%s|%s>", n.userID, n.label)
	} else {
		return fmt.Sprintf("<@%s>", n.userID)
	}
}

type astSlackChannelMention struct {
	astSlackTag

	serverName string
	channelID  string
	mxid       id.RoomID
	alias      id.RoomAlias
	name       string
}

func (n *astSlackChannelMention) String() string {
	if n.label != "" {
		return fmt.Sprintf("<#%s|%s>", n.channelID, n.label)
	} else {
		return fmt.Sprintf("<#%s>", n.channelID)
	}
}

type astSlackURL struct {
	astSlackTag

	url string
}

func (n *astSlackURL) String() string {
	if n.label != n.url {
		return fmt.Sprintf("<%s|%s>", n.url, n.label)
	} else {
		return fmt.Sprintf("<%s>", n.url)
	}
}

type astSlackSpecialMention struct {
	astSlackTag

	content string
}

func (n *astSlackSpecialMention) String() string {
	if n.label != "" {
		return fmt.Sprintf("<!%s|%s>", n.content, n.label)
	} else {
		return fmt.Sprintf("<!%s>", n.content)
	}
}

type Params struct {
	ServerName     string
	GetUserInfo    func(ctx context.Context, userID string) (mxid id.UserID, name string)
	GetChannelInfo func(ctx context.Context, channelID string) (mxid id.RoomID, alias id.RoomAlias, name string)
}

type slackTagParser struct {
	*Params
}

// Regex matching Slack docs at https://api.slack.com/reference/surfaces/formatting#retrieving-messages
var slackTagRegex = regexp.MustCompile(`<(#|@|!|)([^|>]+)(\|([^|>]*))?>`)

func (s *slackTagParser) Trigger() []byte {
	return []byte{'<'}
}

var (
	ContextKeyContext  = parser.NewContextKey()
	ContextKeyMentions = parser.NewContextKey()
)

func (s *slackTagParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	//before := block.PrecendingCharacter()
	line, _ := block.PeekLine()
	match := slackTagRegex.FindSubmatch(line)
	if match == nil {
		return nil
	}
	//seg := segment.WithStop(segment.Start + len(match[0]))
	block.Advance(len(match[0]))

	sigil := string(match[1])
	content := string(match[2])
	text := string(match[4])

	ctx := pc.Get(ContextKeyContext).(context.Context)

	tag := astSlackTag{label: text}
	switch sigil {
	case "@":
		mxid, name := s.GetUserInfo(ctx, content)
		pc.Get(ContextKeyMentions).(*event.Mentions).Add(mxid)
		return &astSlackUserMention{astSlackTag: tag, userID: content, mxid: mxid, name: name}
	case "#":
		mxid, alias, name := s.GetChannelInfo(ctx, content)
		return &astSlackChannelMention{astSlackTag: tag, channelID: content, serverName: s.ServerName, mxid: mxid, alias: alias, name: name}
	case "!":
		switch content {
		case "channel", "everyone", "here":
			pc.Get(ContextKeyMentions).(*event.Mentions).Room = true
		default:
		}
		return &astSlackSpecialMention{astSlackTag: tag, content: content}
	case "":
		return &astSlackURL{astSlackTag: tag, url: content}
	default:
		return nil
	}
}

func (s *slackTagParser) CloseBlock(parent ast.Node, pc parser.Context) {
	// nothing to do
}

type slackTagHTMLRenderer struct{}

var defaultSlackTagHTMLRenderer = &slackTagHTMLRenderer{}

func (r *slackTagHTMLRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(astKindSlackTag, r.renderSlackTag)
}

func UserMentionToHTML(out io.Writer, userID string, mxid id.UserID, name string) {
	if mxid != "" {
		_, _ = fmt.Fprintf(out, `<a href="%s">%s</a>`, mxid.URI().MatrixToURL(), html.EscapeString(name))
	} else {
		_, _ = fmt.Fprintf(out, "&lt;@%s&gt;", userID)
	}
}

func RoomMentionToHTML(out io.Writer, channelID string, mxid id.RoomID, alias id.RoomAlias, name, serverName string) {
	if alias != "" {
		_, _ = fmt.Fprintf(out, `<a href="%s">%s</a>`, alias.URI().MatrixToURL(), html.EscapeString(name))
	} else if mxid != "" {
		_, _ = fmt.Fprintf(out, `<a href="%s">%s</a>`, mxid.URI(serverName).MatrixToURL(), html.EscapeString(name))
	} else if name != "" {
		_, _ = fmt.Fprintf(out, "%s", name)
	} else {
		_, _ = fmt.Fprintf(out, "&lt;#%s&gt;", channelID)
	}
}

func (r *slackTagHTMLRenderer) renderSlackTag(w goldmarkUtil.BufWriter, source []byte, n ast.Node, entering bool) (status ast.WalkStatus, err error) {
	status = ast.WalkContinue
	if !entering {
		return
	}
	switch node := n.(type) {
	case *astSlackUserMention:
		UserMentionToHTML(w, node.userID, node.mxid, node.name)
		return
	case *astSlackChannelMention:
		RoomMentionToHTML(w, node.channelID, node.mxid, node.alias, node.name, node.serverName)
		return
	case *astSlackSpecialMention:
		parts := strings.Split(node.content, "^")
		switch parts[0] {
		case "date":
			timestamp, converr := strconv.ParseInt(parts[1], 10, 64)
			if converr != nil {
				return
			}
			t := time.Unix(timestamp, 0)

			mapping := map[string]string{
				"{date_num}":          t.Local().Format("2006-01-02"),
				"{date}":              t.Local().Format("January 2, 2006"),
				"{date_pretty}":       t.Local().Format("January 2, 2006"),
				"{date_short}":        t.Local().Format("Jan 2, 2006"),
				"{date_short_pretty}": t.Local().Format("Jan 2, 2006"),
				"{date_long}":         t.Local().Format("Monday, January 2, 2006"),
				"{date_long_pretty}":  t.Local().Format("Monday, January 2, 2006"),
				"{time}":              t.Local().Format("15:04 MST"),
				"{time_secs}":         t.Local().Format("15:04:05 MST"),
			}

			for k, v := range mapping {
				parts[2] = strings.ReplaceAll(parts[2], k, v)
			}

			if len(parts) > 3 {
				_, _ = fmt.Fprintf(w, `<a href="%s">%s</a>`, html.EscapeString(parts[3]), html.EscapeString(parts[2]))
			} else {
				_, _ = w.WriteString(html.EscapeString(parts[2]))
			}
			return
		case "channel", "everyone", "here":
			// do @room mentions?
			return
		case "subteam":
			// do subteam handling? more spaces?
			return
		default:
			return
		}
	case *astSlackURL:
		label := node.label
		if label == "" {
			label = node.url
		}
		_, _ = fmt.Fprintf(w, `<a href="%s">%s</a>`, html.EscapeString(node.url), html.EscapeString(label))
		return
	}
	stringifiable, ok := n.(fmt.Stringer)
	if ok {
		_, _ = w.WriteString(stringifiable.String())
	} else {
		_, _ = w.Write(source)
	}
	return
}

type slackTag struct {
	*Params
}

func (e *slackTag) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithInlineParsers(
		goldmarkUtil.Prioritized(&slackTagParser{Params: e.Params}, 150),
	))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		goldmarkUtil.Prioritized(defaultSlackTagHTMLRenderer, 150),
	))
}
