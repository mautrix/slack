package matrixfmt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

func parseBlocks(t *testing.T, htmlData string) []slack.Block {
	t.Helper()
	return New2(nil, nil).ParseBlocks(context.Background(), htmlData, nil, nil)
}

func blockTypes(blocks []slack.Block) []slack.MessageBlockType {
	types := make([]slack.MessageBlockType, len(blocks))
	for i, block := range blocks {
		types[i] = block.BlockType()
	}
	return types
}

func htmlTable(rows, columns int, cell func(row, col int) string) string {
	var buf strings.Builder
	buf.WriteString("<table><thead><tr>")
	for c := range columns {
		fmt.Fprintf(&buf, "<th>Col %d</th>", c)
	}
	buf.WriteString("</tr></thead><tbody>")
	for r := range rows - 1 {
		buf.WriteString("<tr>")
		for c := range columns {
			fmt.Fprintf(&buf, "<td>%s</td>", cell(r, c))
		}
		buf.WriteString("</tr>")
	}
	buf.WriteString("</tbody></table>")
	return buf.String()
}

func requireCodeBlock(t *testing.T, blocks []slack.Block, wantText string) {
	t.Helper()
	if len(blocks) != 1 || blocks[0].BlockType() != slack.MBTRichText {
		t.Fatalf("expected a single rich_text block, got %v", blockTypes(blocks))
	}
	for _, elem := range blocks[0].(*slack.RichTextBlock).Elements {
		pre, ok := elem.(*slack.RichTextPreformatted)
		if !ok {
			continue
		}
		text := pre.Elements[0].(*slack.RichTextSectionTextElement).Text
		if !strings.Contains(text, wantText) {
			t.Fatalf("code block %q doesn't contain %q", text, wantText)
		}
		return
	}
	t.Fatalf("no code block in %#v", blocks[0])
}

func TestTable3x3BetweenRichText(t *testing.T) {
	blocks := parseBlocks(t, `<p>Antes</p><table><thead><tr><th>Cliente</th><th>Estado</th><th>Importe</th></tr></thead>`+
		`<tbody><tr><td>Astrokube</td><td>pagada</td><td>4.500,00</td></tr><tr><td>Lana</td><td>pendiente</td><td>120</td></tr></tbody></table><p>Después</p>`)
	if got := blockTypes(blocks); len(got) != 3 || got[0] != slack.MBTRichText || got[1] != slack.MBTTable || got[2] != slack.MBTRichText {
		t.Fatalf("expected rich_text, table, rich_text; got %v", got)
	}
	table := blocks[1].(*slack.TableBlock)
	if len(table.Rows) != 3 || len(table.Rows[0]) != 3 || len(table.Rows[2]) != 3 {
		t.Fatalf("expected 3x3 rows, got %#v", table.Rows)
	}
	header, ok := table.Rows[0][0].(*slack.TableRichTextCell)
	if !ok {
		t.Fatalf("header cell should be rich_text, got %T", table.Rows[0][0])
	}
	headerText := header.Elements[0].(*slack.RichTextSection).Elements[0].(*slack.RichTextSectionTextElement)
	if headerText.Text != "Cliente" || headerText.Style == nil || !headerText.Style.Bold {
		t.Fatalf("header cell should be bold 'Cliente', got %#v", headerText)
	}
	if cell, ok := table.Rows[1][0].(*slack.TableRawTextCell); !ok || cell.Text != "Astrokube" {
		t.Fatalf("plain cell should be raw_text 'Astrokube', got %#v", table.Rows[1][0])
	}
	want := []slack.ColumnSetting{
		{Align: slack.ColumnAlignmentLeft, IsWrapped: true},
		{Align: slack.ColumnAlignmentLeft, IsWrapped: true},
		{Align: slack.ColumnAlignmentRight},
	}
	if fmt.Sprint(table.ColumnSettings) != fmt.Sprint(want) {
		t.Fatalf("unexpected column settings %v", table.ColumnSettings)
	}
	before := blocks[0].(*slack.RichTextBlock).Elements[0].(*slack.RichTextSection).Elements[0].(*slack.RichTextSectionTextElement)
	after := blocks[2].(*slack.RichTextBlock).Elements[0].(*slack.RichTextSection).Elements[0].(*slack.RichTextSectionTextElement)
	if before.Text != "Antes" || after.Text != "Después" {
		t.Fatalf("text around the table out of place: %q, %q", before.Text, after.Text)
	}
}

func TestTableWireFormat(t *testing.T) {
	blocks := parseBlocks(t, `<table><tr><th>A</th></tr><tr><td>x</td></tr></table>`)
	data, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"type":"table"`, `{"type":"raw_text","text":"x"}`, `"type":"rich_text"`, `"column_settings":[{"align":"left","is_wrapped":true}]`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("%s doesn't contain %s", data, want)
		}
	}
}

func TestTable21ColumnsIsCodeBlock(t *testing.T) {
	blocks := parseBlocks(t, `<p>Antes</p>`+htmlTable(3, 21, func(r, c int) string { return fmt.Sprintf("v%d", c) })+`<p>Después</p>`)
	requireCodeBlock(t, blocks, "Col 0 | Col 1 | Col 2")
}

func TestTable101RowsIsCodeBlock(t *testing.T) {
	requireCodeBlock(t, parseBlocks(t, htmlTable(101, 2, func(r, c int) string { return fmt.Sprint(r) })), "99")
	if got := blockTypes(parseBlocks(t, htmlTable(100, 20, func(r, c int) string { return "x" }))); len(got) != 1 || got[0] != slack.MBTTable {
		t.Fatalf("a 100x20 table should be native, got %v", got)
	}
}

func TestTableCharacterLimitAcrossMessage(t *testing.T) {
	// Each table has 2 columns x 50 rows of 60 characters: 6000 characters.
	long := strings.Repeat("a", 60)
	table := htmlTable(50, 2, func(r, c int) string { return long })
	blocks := parseBlocks(t, table+"<p>entre</p>"+table)
	if got := blockTypes(blocks); len(got) != 2 || got[0] != slack.MBTTable || got[1] != slack.MBTRichText {
		t.Fatalf("expected the first table native and the second as code, got %v", got)
	}
}

func TestTableLinkCellIsRichTextLink(t *testing.T) {
	blocks := parseBlocks(t, `<table><tr><th>PR</th></tr><tr><td><a href="https://github.com/x/y/pull/1">la PR</a></td></tr></table>`)
	table := blocks[0].(*slack.TableBlock)
	cell, ok := table.Rows[1][0].(*slack.TableRichTextCell)
	if !ok {
		t.Fatalf("link cell should be rich_text, got %T", table.Rows[1][0])
	}
	link, ok := cell.Elements[0].(*slack.RichTextSection).Elements[0].(*slack.RichTextSectionLinkElement)
	if !ok || link.URL != "https://github.com/x/y/pull/1" || link.Text != "la PR" {
		t.Fatalf("expected a link element, got %#v", cell.Elements[0].(*slack.RichTextSection).Elements[0])
	}
}

func TestTwoTablesInOneMessage(t *testing.T) {
	small := `<table><tr><th>A</th><th>B</th></tr><tr><td>1</td><td>2</td></tr></table>`
	blocks := parseBlocks(t, `<p>uno</p>`+small+`<p>dos</p>`+small)
	if got := blockTypes(blocks); fmt.Sprint(got) != "[rich_text table rich_text table]" {
		t.Fatalf("unexpected blocks %v", got)
	}
}

func TestTableNotTopLevelIsCodeBlock(t *testing.T) {
	requireCodeBlock(t, parseBlocks(t, `<blockquote><table><tr><th>A</th></tr><tr><td>x</td></tr></table></blockquote>`), "A\n-\nx")
	requireCodeBlock(t, parseBlocks(t, `<table><tr><td colspan="2">A</td></tr><tr><td>x</td><td>y</td></tr></table>`), "A |\nx | y")
}

func TestCodeBlockTableKeepsLinkURLs(t *testing.T) {
	requireCodeBlock(t, parseBlocks(t, `<blockquote><table><tr><th>PR</th></tr><tr><td><a href="https://github.com/x/y/pull/1">la PR</a></td></tr>`+
		`<tr><td><a href="https://example.com">https://example.com</a></td></tr></table></blockquote>`),
		"la PR (https://github.com/x/y/pull/1)\nhttps://example.com")
}

func TestTableCaptionIsKept(t *testing.T) {
	blocks := parseBlocks(t, `<table><caption>Facturas</caption><tr><th>A</th></tr><tr><td>1</td></tr></table>`)
	if got := fmt.Sprint(blockTypes(blocks)); got != "[rich_text table]" {
		t.Fatalf("expected the caption before the table, got %s", got)
	}
	caption := blocks[0].(*slack.RichTextBlock).Elements[0].(*slack.RichTextSection).Elements[0].(*slack.RichTextSectionTextElement)
	if caption.Text != "Facturas" {
		t.Fatalf("unexpected caption %q", caption.Text)
	}
}

func TestIndentedCellsAreTrimmed(t *testing.T) {
	blocks := parseBlocks(t, "<table>\n  <tr>\n    <th>\n      Nombre\n    </th>\n  </tr>\n  <tr><td> <b>x</b> </td></tr>\n</table>")
	table := blocks[0].(*slack.TableBlock)
	for _, row := range table.Rows {
		elems := row[0].(*slack.TableRichTextCell).Elements[0].(*slack.RichTextSection).Elements
		if len(elems) != 1 {
			t.Fatalf("expected one trimmed text element, got %#v", elems)
		}
		if text := elems[0].(*slack.RichTextSectionTextElement).Text; text != "Nombre" && text != "x" {
			t.Fatalf("cell not trimmed: %q", text)
		}
	}
}

func TestParseKeepsTablesAsCodeBlocks(t *testing.T) {
	block := New2(nil, nil).Parse(context.Background(), `<table><thead><tr><th>Nombre</th><th>N</th></tr></thead><tbody><tr><td>uno</td><td>1</td></tr></tbody></table>`, nil, nil)
	requireCodeBlock(t, []slack.Block{block}, "Nombre | N\n-------+--\nuno    | 1")
}

func TestRaggedRowsArePadded(t *testing.T) {
	blocks := parseBlocks(t, `<table><tr><th>A</th><th>B</th></tr><tr><td>x</td></tr></table>`)
	table := blocks[0].(*slack.TableBlock)
	if len(table.Rows[1]) != 2 {
		t.Fatalf("row should be padded to 2 cells, got %d", len(table.Rows[1]))
	}
}

func TestAlignedTableMatchesGateway(t *testing.T) {
	got := AlignedTable([][]string{{"Cliente", "Importe"}, {"Lana", "120"}, {"Astrokube", "4.500"}}, 1)
	want := "Cliente   | Importe\n----------+--------\nLana      | 120\nAstrokube | 4.500\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAlignedTableCompactsHugePadding(t *testing.T) {
	rows := [][]string{{strings.Repeat("x", 1000), "n"}}
	for range 99 {
		rows = append(rows, []string{"a", "1"})
	}
	got := AlignedTable(rows, 1)
	if len(got) > 2000 || !strings.HasPrefix(got, strings.Repeat("x", 1000)+" | n\n---\na | 1\n") {
		t.Fatalf("expected a compact table, got %d bytes: %.80q", len(got), got)
	}
}

func TestCellLineBreakIsKept(t *testing.T) {
	blocks := parseBlocks(t, `<table><tr><th>A</th></tr><tr><td>primera<br>segunda</td></tr></table>`)
	cell, ok := blocks[0].(*slack.TableBlock).Rows[1][0].(*slack.TableRichTextCell)
	if !ok {
		t.Fatalf("a cell with <br> should be rich_text, got %T", blocks[0].(*slack.TableBlock).Rows[1][0])
	}
	var text strings.Builder
	for _, elem := range cell.Elements[0].(*slack.RichTextSection).Elements {
		text.WriteString(elem.(*slack.RichTextSectionTextElement).Text)
	}
	if text.String() != "primera\nsegunda" {
		t.Fatalf("line break lost: %q", text.String())
	}
}

func TestCodeBlockSeparatesParagraphsAndItems(t *testing.T) {
	requireCodeBlock(t, parseBlocks(t, `<blockquote><table><tr><th>A</th></tr><tr><td><p>uno</p><p>dos</p></td></tr>`+
		`<tr><td><ul><li>tres</li><li>cuatro</li></ul></td></tr></table></blockquote>`), "uno dos\ntres cuatro")
	if got := PlainText("<p>uno</p><p>dos</p>"); got != "uno dos" {
		t.Fatalf("PlainText = %q", got)
	}
}
