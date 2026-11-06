package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/slack-go/slack"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-slack/pkg/msgconv"
	"go.mau.fi/mautrix-slack/pkg/msgconv/matrixfmt"
)

func TestIsBlocksError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{slack.SlackErrorResponse{Err: "invalid_blocks"}, true},
		{slack.SlackErrorResponse{Err: "invalid_blocks_format"}, true},
		{fmt.Errorf("wrapped: %w", slack.SlackErrorResponse{Err: "msg_blocks_too_long"}), true},
		{slack.SlackErrorResponse{Err: "channel_not_found"}, false},
		{errors.New("invalid_blocks"), false},
	} {
		if got := isBlocksError(tc.err); got != tc.want {
			t.Errorf("isBlocksError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// fakeSlack answers chat.postMessage with the given errors in order, then with success.
func fakeSlack(t *testing.T, errs ...string) (*slack.Client, *[]string) {
	t.Helper()
	var sentBlocks []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sentBlocks = append(sentBlocks, r.PostForm.Get("blocks"))
		if len(sentBlocks) <= len(errs) {
			_, _ = fmt.Fprintf(w, `{"ok":false,"error":%q}`, errs[len(sentBlocks)-1])
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"1700000000.000300"}`))
	}))
	t.Cleanup(srv.Close)
	return slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/")), &sentBlocks
}

func tableMessage(t *testing.T) *msgconv.ConvertedSlackMessage {
	t.Helper()
	mc := &msgconv.MessageConverter{MatrixHTMLParser: matrixfmt.New2(nil, nil)}
	content := &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          "tabla",
		Format:        event.FormatHTML,
		FormattedBody: `<p>Antes</p><table><tr><th>A</th></tr><tr><td>1</td></tr></table>`,
	}
	conv, err := mc.ToSlack(context.Background(), nil, nil, content, &event.Event{Type: event.EventMessage}, nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return conv
}

func TestSendToSlackRetriesWithoutTables(t *testing.T) {
	client, sent := fakeSlack(t, "invalid_blocks")
	ts, err := (&SlackClient{Client: client}).sendToSlack(context.Background(), "C1", tableMessage(t), nil)
	if err != nil || ts != "1700000000.000300" {
		t.Fatalf("expected the retry to succeed, got %q, %v", ts, err)
	}
	if len(*sent) != 2 || !strings.Contains((*sent)[0], `"type":"table"`) || strings.Contains((*sent)[1], `"type":"table"`) {
		t.Fatalf("expected a native table first and a retry without it, got %q", *sent)
	}
	if !strings.Contains((*sent)[1], `"type":"rich_text_preformatted"`) {
		t.Fatalf("retry should carry the table as a code block, got %s", (*sent)[1])
	}
}

func TestSendToSlackDoesNotRetryOtherErrors(t *testing.T) {
	client, sent := fakeSlack(t, "channel_not_found")
	_, err := (&SlackClient{Client: client}).sendToSlack(context.Background(), "C1", tableMessage(t), nil)
	if err == nil || len(*sent) != 1 {
		t.Fatalf("expected one failed request, got %d requests, err %v", len(*sent), err)
	}
}
