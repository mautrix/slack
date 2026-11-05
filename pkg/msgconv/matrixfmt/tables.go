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

package matrixfmt

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/slack-go/slack"
	"golang.org/x/net/html"
)

// Limits of the Slack table block (https://docs.slack.dev/reference/block-kit/blocks/table-block/).
const (
	MaxTableRows    = 100
	MaxTableColumns = 20
	// MaxTableChars is the limit of characters across all table cells of a single message.
	MaxTableChars = 10000
)

// TableBudget tracks the native tables of a single message, so that all of them together
// stay within MaxTableChars.
type TableBudget struct {
	Chars int
}

// nativeTable carries a Slack table block through the rich text element tree until
// splitTableBlocks lifts it out to the top level of the message. It is never sent as is.
type nativeTable struct {
	block *slack.TableBlock
}

func (nativeTable) RichTextElementType() slack.RichTextElementType {
	return "native_table"
}

type tableCell struct {
	header  bool
	text    string
	align   slack.ColumnAlignment
	rich    bool
	section []slack.RichTextSectionElement
	elems   []slack.RichTextElement
}

// Ancestors under which a table can still be lifted out into its own block.
var tableTopLevelTags = map[string]bool{"html": true, "body": true, "div": true}

func isTopLevelTable(ctx Context) bool {
	// The last tag in the stack is the table itself.
	for _, tag := range ctx.TagStack[:len(ctx.TagStack)-1] {
		if !tableTopLevelTags[tag] {
			return false
		}
	}
	return true
}

// tableToElements converts a <table> into a native table placeholder when it fits Slack's
// limits, or into an aligned code block otherwise.
func (parser *HTMLParser) tableToElements(node *html.Node, ctx Context) []slack.RichTextElement {
	canBeNative := ctx.Tables != nil && isTopLevelTable(ctx)
	rows, headerRows, simple := parser.readTable(node, ctx, canBeNative)
	if len(rows) == 0 {
		return nil
	}
	columns := 0
	chars := 0
	for _, row := range rows {
		columns = max(columns, len(row))
		for _, cell := range row {
			chars += utf8.RuneCountInString(cell.text)
		}
	}
	if canBeNative && simple && len(rows) <= MaxTableRows && columns <= MaxTableColumns &&
		ctx.Tables.Chars+chars <= MaxTableChars {
		ctx.Tables.Chars += chars
		return []slack.RichTextElement{&nativeTable{block: buildTableBlock(rows, headerRows, columns)}}
	}
	texts := make([][]string, len(rows))
	for i, row := range rows {
		texts[i] = make([]string, len(row))
		for j, cell := range row {
			texts[i][j] = cell.text
		}
	}
	border := 0
	if ctx.TagStack.Has("blockquote") {
		border = 1
	}
	return []slack.RichTextElement{slack.NewRichTextPreformatted(border, "",
		slack.NewRichTextSectionTextElement(strings.TrimSuffix(AlignedTable(texts, headerRows), "\n"), nil))}
}

// readTable collects the rows of a table. simple is false when the table can't be a native
// Slack table (merged cells or nested tables). Rich text of the cells is only parsed when
// parseCells is set.
func (parser *HTMLParser) readTable(node *html.Node, ctx Context, parseCells bool) (rows [][]tableCell, headerRows int, simple bool) {
	simple = true
	theadRows := 0
	readRow := func(tr *html.Node, inHead bool) {
		var row []tableCell
		for td := tr.FirstChild; td != nil; td = td.NextSibling {
			if td.Type != html.ElementNode || (td.Data != "td" && td.Data != "th") {
				continue
			}
			if spanAttr(parser.getAttribute(td, "colspan")) > 1 || spanAttr(parser.getAttribute(td, "rowspan")) > 1 || hasDescendant(td, "table") {
				simple = false
			}
			cell := tableCell{
				header: td.Data == "th" || inHead,
				text:   strings.Join(strings.Fields(nodeText(td)), " "),
				align:  parser.cellAlignment(td),
			}
			if parseCells && simple {
				cellCtx := ctx.WithTag(td.Data)
				if cell.header {
					cellCtx = cellCtx.StyleBold()
				}
				cell.section, cell.elems = parser.nodeAndSiblingsToElement(td.FirstChild, cellCtx)
				cell.rich = cell.header || len(cell.elems) > 0 || !isPlainText(cell.section)
			}
			row = append(row, cell)
		}
		if len(row) > 0 {
			rows = append(rows, row)
			if inHead {
				theadRows = len(rows)
			}
		}
	}
	var readRows func(parent *html.Node, inHead bool)
	readRows = func(parent *html.Node, inHead bool) {
		for child := parent.FirstChild; child != nil; child = child.NextSibling {
			if child.Type != html.ElementNode {
				continue
			}
			switch child.Data {
			case "thead":
				readRows(child, true)
			case "tbody", "tfoot":
				readRows(child, false)
			case "tr":
				readRow(child, inHead)
			}
		}
	}
	readRows(node, false)
	headerRows = theadRows
	if headerRows == 0 {
		// Without <thead>, the leading rows made only of <th> are the header.
		for _, row := range rows {
			allHeader := true
			for _, cell := range row {
				allHeader = allHeader && cell.header
			}
			if !allHeader {
				break
			}
			headerRows++
		}
	}
	return
}

func buildTableBlock(rows [][]tableCell, headerRows, columns int) *slack.TableBlock {
	block := slack.NewTableBlock("")
	for _, row := range rows {
		cells := make([]slack.TableCell, columns)
		for i := range cells {
			if i >= len(row) {
				cells[i] = slack.NewTableRawTextCell(" ")
				continue
			}
			cell := row[i]
			var elems []slack.RichTextElement
			if len(cell.section) > 0 {
				elems = append(elems, slack.NewRichTextSection(cell.section...))
			}
			elems = append(elems, cell.elems...)
			if cell.rich && len(elems) > 0 {
				cells[i] = slack.NewTableRichTextCell(elems...)
			} else if cell.text != "" {
				cells[i] = slack.NewTableRawTextCell(cell.text)
			} else {
				// Slack has no documented empty cell for publishing.
				cells[i] = slack.NewTableRawTextCell(" ")
			}
		}
		block.AddRow(cells...)
	}
	return block.WithColumnSettings(columnSettings(rows, headerRows, columns)...)
}

var numericCellRegex = regexp.MustCompile(`^[+\-−]?[€$£]?\s?(?:\d+|\d{1,3}(?:[.,  ]\d{3})+)(?:[.,]\d+)?\s?(?:%|€|\$|£)?$`)

// Cells that don't count against a column being numeric (missing values).
var emptyCellTexts = map[string]bool{"": true, "-": true, "–": true, "—": true, "n/a": true, "N/A": true}

// columnSettings aligns numeric columns to the right and wraps text columns. An explicit
// alignment in the HTML (align attribute or text-align style) wins over the guess.
func columnSettings(rows [][]tableCell, headerRows, columns int) []slack.ColumnSetting {
	settings := make([]slack.ColumnSetting, columns)
	for col := range settings {
		var explicit slack.ColumnAlignment
		numeric, values := true, 0
		for i, row := range rows {
			if col >= len(row) {
				continue
			}
			if explicit == "" {
				explicit = row[col].align
			}
			if i < headerRows || emptyCellTexts[row[col].text] {
				continue
			}
			values++
			numeric = numeric && numericCellRegex.MatchString(row[col].text)
		}
		numeric = numeric && values > 0
		switch {
		case explicit != "":
			settings[col] = slack.ColumnSetting{Align: explicit, IsWrapped: !numeric}
		case numeric:
			settings[col] = slack.ColumnSetting{Align: slack.ColumnAlignmentRight}
		default:
			settings[col] = slack.ColumnSetting{Align: slack.ColumnAlignmentLeft, IsWrapped: true}
		}
	}
	return settings
}

var textAlignRegex = regexp.MustCompile(`text-align:\s*(left|center|right)`)

func (parser *HTMLParser) cellAlignment(td *html.Node) slack.ColumnAlignment {
	align := parser.getAttribute(td, "align")
	if match := textAlignRegex.FindStringSubmatch(parser.getAttribute(td, "style")); match != nil {
		align = match[1]
	}
	switch align {
	case "left", "center", "right":
		return slack.ColumnAlignment(align)
	}
	return ""
}

func isPlainText(elems []slack.RichTextSectionElement) bool {
	for _, elem := range elems {
		text, ok := elem.(*slack.RichTextSectionTextElement)
		if !ok || text.Style != nil {
			return false
		}
	}
	return true
}

func spanAttr(val string) int {
	n, err := strconv.Atoi(strings.TrimSpace(val))
	if err != nil {
		return 1
	}
	return n
}

func hasDescendant(node *html.Node, tag string) bool {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if (child.Type == html.ElementNode && child.Data == tag) || hasDescendant(child, tag) {
			return true
		}
	}
	return false
}

// PlainText is the visible text of an HTML fragment on one line, without formatting.
func PlainText(htmlData string) string {
	node, err := html.Parse(strings.NewReader(htmlData))
	if err != nil {
		return htmlData
	}
	return strings.Join(strings.Fields(nodeText(node)), " ")
}

// nodeText is the visible text of a node: text nodes, image alt texts (emojis) and
// line breaks as spaces.
func nodeText(node *html.Node) string {
	var buf strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch {
		case n.Type == html.TextNode:
			buf.WriteString(n.Data)
		case n.Type == html.ElementNode && n.Data == "br":
			buf.WriteByte(' ')
		case n.Type == html.ElementNode && n.Data == "img":
			for _, attr := range n.Attr {
				if attr.Key == "alt" {
					buf.WriteString(attr.Val)
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return buf.String()
}

// AlignedTable renders rows as monospace text: cells padded to the column width and
// joined with " | ", and a "-+-" separator line after the header rows.
func AlignedTable(rows [][]string, headerRows int) string {
	columns := 0
	for _, row := range rows {
		columns = max(columns, len(row))
	}
	widths := make([]int, columns)
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], utf8.RuneCountInString(cell))
		}
	}
	lines := make([]string, 0, len(rows)+1)
	for i, row := range rows {
		if i == headerRows && headerRows > 0 {
			lines = append(lines, separatorLine(widths))
		}
		cells := make([]string, columns)
		for j := range cells {
			var cell string
			if j < len(row) {
				cell = row[j]
			}
			cells[j] = cell + strings.Repeat(" ", widths[j]-utf8.RuneCountInString(cell))
		}
		lines = append(lines, strings.TrimRight(strings.Join(cells, " | "), " "))
	}
	if headerRows > 0 && headerRows >= len(rows) {
		lines = append(lines, separatorLine(widths))
	}
	return strings.Join(lines, "\n") + "\n"
}

func separatorLine(widths []int) string {
	parts := make([]string, len(widths))
	for i, width := range widths {
		parts[i] = strings.Repeat("-", width)
	}
	return strings.Join(parts, "-+-")
}

// splitTableBlocks lifts the native tables of a rich text block out to the top level,
// keeping the content around them in rich text blocks in the same order. Empty sections
// left between tables are dropped.
func splitTableBlocks(block *slack.RichTextBlock) []slack.Block {
	if !slices.ContainsFunc(block.Elements, isNativeTable) {
		return []slack.Block{block}
	}
	var out []slack.Block
	var current []slack.RichTextElement
	flush := func() {
		if len(current) > 0 {
			out = append(out, slack.NewRichTextBlock("", current...))
			current = nil
		}
	}
	for _, elem := range block.Elements {
		if table, ok := elem.(*nativeTable); ok {
			flush()
			out = append(out, table.block)
		} else if section, ok := elem.(*slack.RichTextSection); !ok || len(section.Elements) > 0 {
			current = append(current, elem)
		}
	}
	flush()
	return out
}

func isNativeTable(elem slack.RichTextElement) bool {
	_, ok := elem.(*nativeTable)
	return ok
}
