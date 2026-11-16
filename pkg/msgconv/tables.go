// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2026 Intelia Technologies
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
	"html"
	"strconv"
	"strings"

	"github.com/slack-go/slack"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"go.mau.fi/mautrix-slack/pkg/msgconv/matrixfmt"
)

// renderedTable is a Slack table rendered as Matrix HTML, with the aligned text that
// replaces it in the plain text body.
type renderedTable struct {
	html string
	text string
}

// renderedTables collects the tables of a message while it's rendered (contextKeyTables).
type renderedTables struct {
	tables []renderedTable
}

// withTableBody replaces each rendered table in the HTML with a code block of its aligned
// text, so that the plain text body shows the table aligned instead of flattened cells.
func (rt *renderedTables) withTableBody(content *event.MessageEventContent, htmlText string) {
	if rt == nil || len(rt.tables) == 0 {
		return
	}
	for _, table := range rt.tables {
		htmlText = strings.Replace(htmlText, table.html, "<pre><code>"+html.EscapeString(table.text)+"</code></pre>", 1)
	}
	content.Body = format.HTMLToContent(htmlText).Body
}

// renderSlackTable renders a table block as a Matrix HTML table. The first row is the
// header, as in Slack.
func (mc *MessageConverter) renderSlackTable(ctx context.Context, table *slack.TableBlock, mentions *event.Mentions) string {
	var buf strings.Builder
	texts := make([][]string, len(table.Rows))
	buf.WriteString("<table>")
	for i, row := range table.Rows {
		tag := "td"
		if i == 0 && len(table.Rows) > 1 {
			tag = "th"
			buf.WriteString("<thead>")
		} else if i == 1 {
			buf.WriteString("<tbody>")
		}
		buf.WriteString("<tr>")
		for _, cell := range row {
			cellHTML := mc.renderSlackTableCell(ctx, cell, mentions)
			texts[i] = append(texts[i], matrixfmt.PlainText(cellHTML))
			buf.WriteString("<" + tag + ">" + cellHTML + "</" + tag + ">")
		}
		buf.WriteString("</tr>")
		if tag == "th" {
			buf.WriteString("</thead>")
		}
	}
	if len(table.Rows) > 1 {
		buf.WriteString("</tbody>")
	}
	buf.WriteString("</table>")
	headerRows := 0
	if len(table.Rows) > 1 {
		headerRows = 1
	}
	rendered := renderedTable{html: buf.String(), text: matrixfmt.AlignedTable(texts, headerRows)}
	if rt, ok := ctx.Value(contextKeyTables).(*renderedTables); ok {
		rt.tables = append(rt.tables, rendered)
	}
	return rendered.html
}

func (mc *MessageConverter) renderSlackTableCell(ctx context.Context, cell slack.TableCell, mentions *event.Mentions) string {
	switch c := cell.(type) {
	case *slack.TableRichTextCell:
		var buf strings.Builder
		mc.renderSlackRichTextElements(ctx, c.Elements, mentions, 0, &buf)
		return format.UnwrapSingleParagraph(buf.String())
	case *slack.TableRawTextCell:
		return html.EscapeString(c.Text)
	case *slack.TableRawNumberCell:
		if c.Text != "" {
			return html.EscapeString(c.Text)
		}
		return strconv.FormatFloat(c.Value, 'f', -1, 64)
	default:
		// nil: an empty cell
		return ""
	}
}
