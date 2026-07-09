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

func TestSlackBlocksToMatrixMessageUnfurlWithPreviewFields(t *testing.T) {
	mc := testMessageConverter()
	attachment := slack.Attachment{
		IsMsgUnfurl: true,
		AuthorName:  "testbot",
		FromURL:     "https://example.slack.com/archives/C123/p123",
		OriginalURL: "https://example.slack.com/archives/C123/p123",
		Title:       "testbot in #general",
		Footer:      "Posted in #general | Yesterday at 10:38 AM | View message",
	}
	attachment.MessageBlocks = append(attachment.MessageBlocks, slack.MessageBlocks{})
	attachment.MessageBlocks[0].Message.Blocks = slack.Blocks{
		BlockSet: []slack.Block{
			slack.NewRichTextBlock("", slack.NewRichTextSection(
				slack.NewRichTextSectionTextElement("hi", nil),
			)),
		},
	}
	part, err := mc.slackBlocksToMatrix(context.Background(), nil, nil, slack.Blocks{}, []slack.Attachment{attachment})
	if err != nil {
		t.Fatal(err)
	}
	if len(part.Content.BeeperLinkPreviews) != 0 {
		t.Fatalf("expected no generic link previews, got %d", len(part.Content.BeeperLinkPreviews))
	}
	expectedBody := "> **testbot**\n> \n> hi\n> [_Posted in #general | Yesterday at 10:38 AM | View message_](https://example.slack.com/archives/C123/p123)"
	if part.Content.Body != expectedBody {
		t.Fatalf("unexpected body: %q", part.Content.Body)
	}
}

func TestSlackBlocksToMatrixMessageMention(t *testing.T) {
	mc := testMessageConverter()
	part, err := mc.slackBlocksToMatrix(context.Background(), nil, nil, slack.Blocks{
		BlockSet: []slack.Block{
			slack.NewRichTextBlock("", slack.NewRichTextSection(
				slack.NewRichTextSectionTextElement("take a look at ", nil),
				slack.NewRichTextSectionMessageMentionElement("C123", "1234567890.123456", "", "", "", nil),
				slack.NewRichTextSectionTextElement(" at", nil),
			)),
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if part.Content.Body != "take a look at message in #C123 at" {
		t.Fatalf("unexpected body: %q", part.Content.Body)
	}
}

func TestSlackBlocksToMatrixMessageMentionPermalink(t *testing.T) {
	mc := testMessageConverter()
	part, err := mc.slackBlocksToMatrix(context.Background(), nil, nil, slack.Blocks{
		BlockSet: []slack.Block{
			slack.NewRichTextBlock("", slack.NewRichTextSection(
				slack.NewRichTextSectionTextElement("take a look at ", nil),
				slack.NewRichTextSectionMessageMentionElement("", "", "", "testbot in #general", "https://example.slack.com/archives/C123/p1234567890123456", nil),
			)),
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if part.Content.Body != "take a look at [testbot in #general](https://example.slack.com/archives/C123/p1234567890123456)" {
		t.Fatalf("unexpected body: %q", part.Content.Body)
	}
	expectedHTML := `take a look at <a href="https://example.slack.com/archives/C123/p1234567890123456">testbot in #general</a>`
	if part.Content.FormattedBody != expectedHTML {
		t.Fatalf("unexpected formatted body: %q", part.Content.FormattedBody)
	}
}
