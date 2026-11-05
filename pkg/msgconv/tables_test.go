package msgconv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/slack-go/slack"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-slack/pkg/msgconv/matrixfmt"
	"go.mau.fi/mautrix-slack/pkg/slackid"
)

const tableHTML = `<p>Antes</p><table><thead><tr><th>Cliente</th><th>Importe</th></tr></thead>` +
	`<tbody><tr><td>Lana</td><td>120</td></tr><tr><td><a href="https://example.com">Astrokube</a></td><td>4.500</td></tr></tbody></table><p>Después</p>`

// toSlackValues sends the message to a fake Slack API and returns the method and form it got.
func toSlackValues(t *testing.T, opt slack.MsgOption) (string, url.Values) {
	t.Helper()
	var endpoint string
	var values url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		endpoint, values = r.URL.Path, r.PostForm
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"1700000000.000300"}`))
	}))
	defer srv.Close()
	_, _, err := slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/")).PostMessageContext(context.Background(), "C1", opt)
	if err != nil {
		t.Fatal(err)
	}
	return endpoint, values
}

func convertToSlack(t *testing.T, threadRoot, editTarget *database.Message) *ConvertedSlackMessage {
	t.Helper()
	mc := &MessageConverter{MatrixHTMLParser: matrixfmt.New2(nil, nil)}
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: "Antes ...", Format: event.FormatHTML, FormattedBody: tableHTML}
	conv, err := mc.ToSlack(context.Background(), nil, nil, content, &event.Event{Type: event.EventMessage}, threadRoot, editTarget, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return conv
}

func blockTypesOf(t *testing.T, blocksJSON string) []string {
	t.Helper()
	var blocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(blocksJSON), &blocks); err != nil {
		t.Fatal(err)
	}
	types := make([]string, len(blocks))
	for i, block := range blocks {
		types[i] = block.Type
	}
	return types
}

func TestToSlackNativeTableWithFallback(t *testing.T) {
	conv := convertToSlack(t, nil, nil)
	endpoint, values := toSlackValues(t, conv.SendReq)
	if !strings.HasSuffix(endpoint, "chat.postMessage") {
		t.Fatalf("unexpected endpoint %s", endpoint)
	}
	if got := strings.Join(blockTypesOf(t, values.Get("blocks")), ","); got != "rich_text,table,rich_text" {
		t.Fatalf("unexpected blocks %s", got)
	}
	if conv.FallbackSendReq == nil {
		t.Fatal("expected a fallback request for a message with tables")
	}
	_, fallback := toSlackValues(t, conv.FallbackSendReq)
	if got := strings.Join(blockTypesOf(t, fallback.Get("blocks")), ","); got != "rich_text" {
		t.Fatalf("fallback should be a single rich_text block, got %s", got)
	}
	if !strings.Contains(fallback.Get("blocks"), `\nAstrokube (https://example.com) | 4.500`) {
		t.Fatalf("fallback should have the aligned table, got %s", fallback.Get("blocks"))
	}
}

func TestToSlackWithoutTablesHasNoFallback(t *testing.T) {
	mc := &MessageConverter{MatrixHTMLParser: matrixfmt.New2(nil, nil)}
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: "hola", Format: event.FormatHTML, FormattedBody: "<b>hola</b>"}
	conv, err := mc.ToSlack(context.Background(), nil, nil, content, &event.Event{Type: event.EventMessage}, nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if conv.FallbackSendReq != nil {
		t.Fatal("unexpected fallback for a message without tables")
	}
	_, values := toSlackValues(t, conv.SendReq)
	if got := strings.Join(blockTypesOf(t, values.Get("blocks")), ","); got != "rich_text" {
		t.Fatalf("unexpected blocks %s", got)
	}
}

func TestToSlackTableKeepsThreadAndEdit(t *testing.T) {
	threadRoot := &database.Message{ID: slackid.MakeMessageID("T1", "C1", "1700000000.000100")}
	conv := convertToSlack(t, threadRoot, nil)
	for _, opt := range []slack.MsgOption{conv.SendReq, conv.FallbackSendReq} {
		_, values := toSlackValues(t, opt)
		if values.Get("thread_ts") != "1700000000.000100" {
			t.Fatalf("thread_ts lost: %v", values)
		}
	}

	editTarget := &database.Message{ID: slackid.MakeMessageID("T1", "C1", "1700000000.000200")}
	conv = convertToSlack(t, nil, editTarget)
	for _, opt := range []slack.MsgOption{conv.SendReq, conv.FallbackSendReq} {
		endpoint, values := toSlackValues(t, opt)
		if !strings.HasSuffix(endpoint, "chat.update") || values.Get("ts") != "1700000000.000200" {
			t.Fatalf("edit lost: %s %v", endpoint, values)
		}
	}
}

// A message as Slack sends it: text, a table with every cell type, more text and a second table.
const slackTableMessage = `[
	{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"Antes"}]}]},
	{"type":"table","column_settings":[{"is_wrapped":true},{"align":"right"}],"rows":[
		[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"Cliente","style":{"bold":true}}]}]},
		 {"type":"raw_text","text":"Importe"}],
		[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"link","url":"https://example.com","text":"Astrokube"}]}]},
		 {"type":"raw_number","value":4500}],
		[{"type":"raw_text","text":"Lana & co"},null]
	]},
	{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"Entre"}]}]},
	{"type":"table","rows":[[{"type":"raw_text","text":"A"},{"type":"raw_text","text":"B"}],[{"type":"raw_text","text":"1"},{"type":"raw_text","text":"2"}]]}
]`

func TestSlackTableToMatrix(t *testing.T) {
	var blocks slack.Blocks
	if err := json.Unmarshal([]byte(slackTableMessage), &blocks); err != nil {
		t.Fatal(err)
	}
	part, err := testMessageConverter().slackBlocksToMatrix(context.Background(), nil, nil, blocks, nil)
	if err != nil {
		t.Fatal(err)
	}
	content := part.Content
	if strings.Contains(content.FormattedBody, "unsupported") || strings.Contains(content.Body, "unsupported") {
		t.Fatalf("table rendered as unsupported: %q", content.FormattedBody)
	}
	wantHTML := `<p>Antes</p><table><thead><tr><th><strong>Cliente</strong></th><th>Importe</th></tr></thead><tbody>` +
		`<tr><td><a href="https://example.com">Astrokube</a></td><td>4500</td></tr><tr><td>Lana &amp; co</td><td></td></tr></tbody></table>` +
		`<p>Entre</p><table><thead><tr><th>A</th><th>B</th></tr></thead><tbody><tr><td>1</td><td>2</td></tr></tbody></table>`
	if content.FormattedBody != wantHTML {
		t.Fatalf("unexpected HTML:\n%s\nwant:\n%s", content.FormattedBody, wantHTML)
	}
	for _, want := range []string{
		"Antes",
		"Cliente   | Importe\n----------+--------\nAstrokube | 4500\nLana & co |",
		"Entre",
		"A | B\n--+--\n1 | 2",
	} {
		if !strings.Contains(content.Body, want) {
			t.Fatalf("body %q doesn't contain %q", content.Body, want)
		}
	}
}

func TestSlackSingleTableToMatrix(t *testing.T) {
	var blocks slack.Blocks
	if err := json.Unmarshal([]byte(`[{"type":"table","rows":[[{"type":"raw_text","text":"solo"}]]}]`), &blocks); err != nil {
		t.Fatal(err)
	}
	part, err := testMessageConverter().slackBlocksToMatrix(context.Background(), nil, nil, blocks, nil)
	if err != nil {
		t.Fatal(err)
	}
	if part.Content.FormattedBody != "<table><tr><td>solo</td></tr></table>" || !strings.Contains(part.Content.Body, "solo") {
		t.Fatalf("unexpected content %q / %q", part.Content.FormattedBody, part.Content.Body)
	}
}
