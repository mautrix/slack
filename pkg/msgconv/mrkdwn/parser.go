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
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/util"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/format/mdext"

	"go.mau.fi/mautrix-slack/pkg/emoji"
)

// indentableParagraphParser is the default paragraph parser with CanAcceptIndentedLine.
// Used when disabling CodeBlockParser (as disabling it without a replacement will make indented blocks disappear).
type indentableParagraphParser struct {
	parser.BlockParser
}

var defaultIndentableParagraphParser = &indentableParagraphParser{BlockParser: parser.NewParagraphParser()}

func (b *indentableParagraphParser) CanAcceptIndentedLine() bool {
	return true
}

type SlackMrkdwnParser struct {
	Params   *Params
	Markdown goldmark.Markdown
}

var removeFeatures = []any{
	parser.NewListParser(), parser.NewListItemParser(), parser.NewHTMLBlockParser(), parser.NewRawHTMLParser(),
	parser.NewSetextHeadingParser(), parser.NewThematicBreakParser(),
	parser.NewCodeBlockParser(), parser.NewLinkParser(), parser.NewEmphasisParser(),
}
var fixIndentedParagraphs = goldmark.WithParserOptions(parser.WithBlockParsers(util.Prioritized(defaultIndentableParagraphParser, 500)))

func New(options *Params) *SlackMrkdwnParser {
	return &SlackMrkdwnParser{
		Markdown: goldmark.New(
			goldmark.WithParser(mdext.ParserWithoutFeatures(removeFeatures...)),
			fixIndentedParagraphs,
			format.HTMLOptions,
			goldmark.WithExtensions(mdext.ShortStrike, mdext.ShortEmphasis, &slackTag{Params: options}),
		),
	}
}

var escapeFixer = regexp.MustCompile(`\\(__[^_]|\*\*[^*])`)

func (smp *SlackMrkdwnParser) Parse(ctx context.Context, input string, mentions *event.Mentions) (string, error) {
	parserCtx := parser.NewContext()
	parserCtx.Set(ContextKeyContext, ctx)
	parserCtx.Set(ContextKeyMentions, mentions)

	input = emoji.ReplaceShortcodesWithUnicode(input)
	// TODO is this actually needed or was it just blindly copied from Discord?
	input = escapeFixer.ReplaceAllStringFunc(input, func(s string) string {
		return s[:2] + `\` + s[2:]
	})

	var buf strings.Builder
	err := smp.Markdown.Convert([]byte(input), &buf, parser.WithContext(parserCtx))
	if err != nil {
		return "", err
	}

	return format.UnwrapSingleParagraph(buf.String()), nil
}
