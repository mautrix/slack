package main

// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/slack-go/slack"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	goldmarkUtil "github.com/yuin/goldmark/util"
	"go.mau.fi/mautrix-slack/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util"
)

var escapeFixer = regexp.MustCompile(`\\(__[^_]|\*\*[^*])`)

const mentionedUsersContextKey = "fi.mau.slack.mentioned_users"

func (portal *Portal) renderSlackMarkdown(text string) *event.MessageEventContent {
	text = replaceShortcodesWithEmojis(text)

	text = escapeFixer.ReplaceAllStringFunc(text, func(s string) string {
		return s[:2] + `\` + s[2:]
	})

	mdRenderer := goldmark.New(
		format.Extensions, format.HTMLOptions,
		goldmark.WithExtensions(&SlackTag{portal}),
	)

	content := format.RenderMarkdownCustom(text, mdRenderer)
	return &content
}

func (portal *Portal) renderSlackFile(file slack.File) event.MessageEventContent {
	content := event.MessageEventContent{
		Info: &event.FileInfo{
			MimeType: file.Mimetype,
			Size:     int(file.Size),
		},
	}
	if file.OriginalW != 0 {
		content.Info.Width = file.OriginalW
	}
	if file.OriginalH != 0 {
		content.Info.Height = file.OriginalH
	}
	if file.Name != "" {
		content.Body = file.Name
	} else {
		mimeClass := strings.Split(file.Mimetype, "/")[0]
		switch mimeClass {
		case "application":
			content.Body = "file"
		default:
			content.Body = mimeClass
		}

		content.Body += util.ExtensionFromMimetype(file.Mimetype)
	}

	if strings.HasPrefix(file.Mimetype, "image") {
		content.MsgType = event.MsgImage
	} else if strings.HasPrefix(file.Mimetype, "video") {
		content.MsgType = event.MsgVideo
	} else if strings.HasPrefix(file.Mimetype, "audio") {
		content.MsgType = event.MsgAudio
	} else {
		content.MsgType = event.MsgFile
	}

	return content
}

func (bridge *SlackBridge) ParseMatrix(html string) string {
	ctx := format.NewContext()
	return bridge.MatrixHTMLParser.Parse(html, ctx)
}

func NewParser(bridge *SlackBridge) *format.HTMLParser {
	return &format.HTMLParser{
		TabsToSpaces: 4,
		Newline:      "\n",

		PillConverter: func(displayname, mxid, eventID string, _ format.Context) string {
			if mxid[0] == '@' {
				_, user, success := bridge.ParsePuppetMXID(id.UserID(mxid))
				if success {
					return fmt.Sprintf("<@%s>", strings.ToUpper(user))
				}
			}
			return fmt.Sprintf("@%s", displayname)
		},
		BoldConverter:           func(text string, _ format.Context) string { return fmt.Sprintf("*%s*", text) },
		ItalicConverter:         func(text string, _ format.Context) string { return fmt.Sprintf("_%s_", text) },
		StrikethroughConverter:  func(text string, _ format.Context) string { return fmt.Sprintf("~%s~", text) },
		MonospaceConverter:      func(text string, _ format.Context) string { return fmt.Sprintf("`%s`", text) },
		MonospaceBlockConverter: func(text, language string, _ format.Context) string { return fmt.Sprintf("```%s```", text) },
	}
}

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

	channelID string
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

type slackTagParser struct{}

// Regex matching Slack docs at https://api.slack.com/reference/surfaces/formatting#retrieving-messages
var slackTagRegex = regexp.MustCompile(`<(#|@|!|)([^|>]+)(\|([^|>]*))?>`)
var defaultSlackTagParser = &slackTagParser{}

func (s *slackTagParser) Trigger() []byte {
	return []byte{'<'}
}

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

	tag := astSlackTag{label: text}
	switch sigil {
	case "@":
		return &astSlackUserMention{astSlackTag: tag, userID: content}
	case "#":
		return &astSlackChannelMention{astSlackTag: tag, channelID: content}
	case "":
		return &astSlackURL{astSlackTag: tag, url: content}
	default:
		return nil
	}
}

func (s *slackTagParser) CloseBlock(parent ast.Node, pc parser.Context) {
	// nothing to do
}

type slackTagHTMLRenderer struct {
	portal *Portal
}

func (r *slackTagHTMLRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(astKindSlackTag, r.renderSlackTag)
}

type Stringifiable interface {
	String() string
}

func (r *slackTagHTMLRenderer) renderSlackTag(w goldmarkUtil.BufWriter, source []byte, n ast.Node, entering bool) (status ast.WalkStatus, err error) {
	status = ast.WalkContinue
	if !entering {
		return
	}
	switch node := n.(type) {
	case *astSlackUserMention:
		puppet := r.portal.bridge.GetPuppetByID(r.portal.Key.TeamID, node.userID)
		if puppet != nil && puppet.GetCustomOrGhostMXID() != "" {
			_, _ = fmt.Fprintf(w, `<a href="https://matrix.to/#/%s">%s</a>`, puppet.GetCustomOrGhostMXID(), puppet.Name)
		} else { // TODO: get puppet info if not exist
			if node.label != "" {
				_, _ = fmt.Fprintf(w, `@%s`, node.label)
			} else {
				_, _ = fmt.Fprintf(w, `@%s`, node.userID)
			}
		}
		return
	case *astSlackChannelMention:
		portal := r.portal.bridge.DB.Portal.GetByID(database.PortalKey{
			TeamID:    r.portal.Key.TeamID,
			ChannelID: node.channelID,
		})
		if portal != nil && portal.MXID != "" {
			_, _ = fmt.Fprintf(w, `<a href="https://matrix.to/#/%s?via=%s">%s</a>`, portal.MXID, r.portal.bridge.AS.HomeserverDomain, portal.Name)
		} else { // TODO: get portal info if not exist
			if node.label != "" {
				_, _ = fmt.Fprintf(w, `#%s`, node.label)
			} else {
				_, _ = fmt.Fprintf(w, `#%s`, node.channelID)
			}
		}
		return
	case *astSlackURL:
		label := node.label
		if label == "" {
			label = node.url
		}
		_, _ = fmt.Fprintf(w, `<a href="%s">%s</a>`, node.url, label)
		return
	}
	stringifiable, ok := n.(Stringifiable)
	if ok {
		_, _ = w.WriteString(stringifiable.String())
	} else {
		_, _ = w.Write(source)
	}
	return
}

type SlackTag struct {
	Portal *Portal
}

func (e *SlackTag) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithInlineParsers(
		goldmarkUtil.Prioritized(defaultSlackTagParser, 150),
	))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		goldmarkUtil.Prioritized(&slackTagHTMLRenderer{e.Portal}, 150),
	))
}
