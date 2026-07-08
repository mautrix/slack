package msgconv

import (
	"context"
	"testing"

	"github.com/slack-go/slack"

	"go.mau.fi/mautrix-slack/pkg/msgconv/mrkdwn"
)

func testMessageConverter() *MessageConverter {
	return &MessageConverter{
		SlackMrkdwnParser: mrkdwn.New(&mrkdwn.Params{}),
	}
}

func TestSlackBlocksToMatrixMessageUnfurlFallback(t *testing.T) {
	mc := testMessageConverter()
	part, err := mc.slackBlocksToMatrix(context.Background(), nil, nil, slack.Blocks{}, []slack.Attachment{{
		IsMsgUnfurl: true,
		AuthorID:    "U0123456789",
		AuthorName:  "alice",
		Text:        "hi",
		FromURL:     "https://example.com/archives/C123/p123",
		Footer:      "Posted in #general | Today at 8:26 AM | View message",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if part.Content.Body == "" {
		t.Fatal("expected fallback text, got empty body")
	}
	if part.Content.Body != "> **alice**\n> hi" {
		t.Fatalf("unexpected body: %q", part.Content.Body)
	}
}
