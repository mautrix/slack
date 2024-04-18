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
	"maunium.net/go/mautrix/format"

	"go.mau.fi/mautrix-slack/msgconv/emoji"
)

type SlackMrkdwnParser struct {
	Params   *Params
	Markdown goldmark.Markdown
}

func New(options *Params) *SlackMrkdwnParser {
	return &SlackMrkdwnParser{
		Markdown: goldmark.New(
			format.Extensions, format.HTMLOptions,
			goldmark.WithExtensions(&slackTag{Params: options}),
		),
	}
}

var escapeFixer = regexp.MustCompile(`\\(__[^_]|\*\*[^*])`)

func (smp *SlackMrkdwnParser) Parse(ctx context.Context, input string) (string, error) {
	parserCtx := parser.NewContext()
	parserCtx.Set(ContextKeyContext, ctx)

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
