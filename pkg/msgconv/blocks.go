// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2022 Max Sandholm
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

package msgconv

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"go.mau.fi/mautrix-slack/pkg/msgconv/mrkdwn"
)

func (mc *MessageConverter) renderImageBlock(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, block slack.ImageBlock) (*bridgev2.ConvertedMessagePart, error) {
	log := zerolog.Ctx(ctx)
	client := http.Client{}
	resp, err := client.Get(block.ImageURL)
	if err != nil {
		log.Err(err).Msg("Failed to fetch image block")
		return nil, err
	} else if resp.StatusCode != 200 {
		log.Error().Int("status_code", resp.StatusCode).Msg("Unexpected status code fetching image block")
		return nil, fmt.Errorf(resp.Status)
	}
	bytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Err(err).Msg("Failed to read image block get response")
		return nil, err
	}
	filename := path.Base(resp.Request.URL.Path)
	mimetype := http.DetectContentType(bytes)
	content := event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    filename,
		Info: &event.FileInfo{
			MimeType: mimetype,
			Size:     len(bytes),
		},
	}
	err = mc.uploadMedia(ctx, portal, intent, bytes, &content)
	if err != nil {
		log.Err(err).Msg("Failed to reupload image block media")
		return nil, err
	}
	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: &content,
	}, nil
}

func (mc *MessageConverter) mrkdwnToMatrixHtml(ctx context.Context, inputMrkdwn string, mentions *event.Mentions) string {
	output, _ := mc.SlackMrkdwnParser.Parse(ctx, inputMrkdwn, mentions)
	return output
}

func (mc *MessageConverter) renderSlackTextBlock(ctx context.Context, block slack.TextBlockObject, mentions *event.Mentions) string {
	if block.Type == slack.PlainTextType {
		return event.TextToHTML(block.Text)
	} else if block.Type == slack.MarkdownType {
		return mc.mrkdwnToMatrixHtml(ctx, block.Text, mentions)
	} else {
		return ""
	}
}

func openingTags(out io.StringWriter, style *slack.RichTextSectionTextStyle) {
	if style == nil {
		return
	}
	if style.Bold {
		_, _ = out.WriteString("<strong>")
	}
	if style.Italic {
		_, _ = out.WriteString("<em>")
	}
	if style.Strike {
		_, _ = out.WriteString("<del>")
	}
	if style.Code {
		_, _ = out.WriteString("<code>")
	}
}

func closingTags(out io.StringWriter, style *slack.RichTextSectionTextStyle) {
	if style == nil {
		return
	}
	if style.Code {
		_, _ = out.WriteString("</code>")
	}
	if style.Strike {
		_, _ = out.WriteString("</del>")
	}
	if style.Italic {
		_, _ = out.WriteString("</em>")
	}
	if style.Bold {
		_, _ = out.WriteString("</strong>")
	}
}

func (mc *MessageConverter) renderRichTextSectionElements(ctx context.Context, elements []slack.RichTextSectionElement, mentions *event.Mentions) string {
	var htmlText strings.Builder
	for _, element := range elements {
		switch e := element.(type) {
		case *slack.RichTextSectionTextElement:
			openingTags(&htmlText, e.Style)
			htmlText.WriteString(event.TextToHTML(e.Text))
			closingTags(&htmlText, e.Style)
		case *slack.RichTextSectionUserElement:
			mxid, name := mc.GetMentionedUserInfo(ctx, e.UserID)
			mentions.Add(mxid)
			openingTags(&htmlText, e.Style)
			mrkdwn.UserMentionToHTML(&htmlText, e.UserID, mxid, name)
			closingTags(&htmlText, e.Style)
		case *slack.RichTextSectionChannelElement:
			mxid, alias, name := mc.GetMentionedRoomInfo(ctx, e.ChannelID)
			openingTags(&htmlText, e.Style)
			mrkdwn.RoomMentionToHTML(&htmlText, e.ChannelID, mxid, alias, name, mc.ServerName)
			closingTags(&htmlText, e.Style)
		case *slack.RichTextSectionLinkElement:
			var linkText string
			if e.Text != "" {
				linkText = e.Text
			} else {
				linkText = e.URL
			}
			openingTags(&htmlText, e.Style)
			_, _ = fmt.Fprintf(&htmlText, `<a href="%s">%s</a>`, html.EscapeString(e.URL), event.TextToHTML(linkText))
			closingTags(&htmlText, e.Style)
		case *slack.RichTextSectionBroadcastElement:
			mentions.Room = true
			htmlText.WriteString("@room")
		case *slack.RichTextSectionEmojiElement:
			openingTags(&htmlText, e.Style)
			if e.Unicode != "" {
				codepoints := strings.Split(e.Unicode, "-")
				for _, codepoint := range codepoints {
					codepointInt, _ := strconv.ParseInt(codepoint, 16, 32)
					htmlText.WriteRune(rune(codepointInt))
				}
			} else {
				sc := ctx.Value(contextKeySource).(*bridgev2.UserLogin).Client.(SlackClientProvider)
				emoji, isImage := sc.GetEmoji(ctx, e.Name)
				if isImage {
					htmlText.WriteString(fmt.Sprintf(`<img data-mx-emoticon src="%[1]s" alt=":%[2]s:" title=":%[2]s:" height="32"/>`, emoji, e.Name))
				} else {
					htmlText.WriteString(emoji)
				}
			}
			closingTags(&htmlText, e.Style)
		case *slack.RichTextSectionColorElement:
			htmlText.WriteString(e.Value)
		case *slack.RichTextSectionDateElement:
			htmlText.WriteString(e.Timestamp.String())
		default:
			zerolog.Ctx(ctx).Debug().
				Type("section_type", e).
				Str("section_type_name", string(e.RichTextSectionElementType())).
				Msg("Unsupported Slack rich text section")
		}
	}
	return htmlText.String()
}

func (mc *MessageConverter) renderSlackBlock(ctx context.Context, block slack.Block, mentions *event.Mentions) (string, bool) {
	switch b := block.(type) {
	case *slack.HeaderBlock:
		return fmt.Sprintf("<h1>%s</h1>", mc.renderSlackTextBlock(ctx, *b.Text, mentions)), false
	case *slack.DividerBlock:
		return "<hr>", false
	case *slack.SectionBlock:
		var htmlParts []string
		if b.Text != nil {
			htmlParts = append(htmlParts, mc.renderSlackTextBlock(ctx, *b.Text, mentions))
		}
		if len(b.Fields) > 0 {
			var fieldTable strings.Builder
			fieldTable.WriteString("<table>")
			for i, field := range b.Fields {
				if i%2 == 0 {
					fieldTable.WriteString("<tr>")
				}
				fieldTable.WriteString(fmt.Sprintf("<td>%s</td>", mc.mrkdwnToMatrixHtml(ctx, field.Text, mentions)))
				if i%2 != 0 || i == len(b.Fields)-1 {
					fieldTable.WriteString("</tr>")
				}
			}
			fieldTable.WriteString("</table>")
			htmlParts = append(htmlParts, fieldTable.String())
		}
		return strings.Join(htmlParts, "<br>"), false
	case *slack.RichTextBlock:
		var htmlText strings.Builder
		for _, element := range b.Elements {
			htmlText.WriteString(mc.renderSlackRichTextElement(ctx, len(b.Elements), element, mentions))
		}
		return format.UnwrapSingleParagraph(htmlText.String()), false
	case *slack.ContextBlock:
		var htmlText strings.Builder
		var unsupported bool = false
		for _, element := range b.ContextElements.Elements {
			if mrkdwnElem, ok := element.(*slack.TextBlockObject); ok {
				htmlText.WriteString(fmt.Sprintf("<sup>%s</sup>", mc.mrkdwnToMatrixHtml(ctx, mrkdwnElem.Text, mentions)))
			} else {
				zerolog.Ctx(ctx).Debug().
					Type("element_type", element).
					Type("element_type_name", element.MixedElementType()).
					Msg("Unsupported Slack block element")
				htmlText.WriteString("<i>Slack message contains unsupported elements.</i>")
				unsupported = true
			}
		}
		return htmlText.String(), unsupported
	default:
		zerolog.Ctx(ctx).Debug().
			Type("block_type", b).
			Type("block_type_name", b.BlockType()).
			Msg("Unsupported Slack block")
		return "<i>Slack message contains unsupported elements.</i>", true
	}
}

func (mc *MessageConverter) renderSlackRichTextElement(ctx context.Context, numElements int, element slack.RichTextElement, mentions *event.Mentions) string {
	switch e := element.(type) {
	case *slack.RichTextSection:
		var htmlTag string
		var htmlCloseTag string
		if e.RichTextElementType() == slack.RTEPreformatted {
			htmlTag = "<pre>"
			htmlCloseTag = "</pre>"
		} else if e.RichTextElementType() == slack.RTEQuote {
			htmlTag = "<blockquote>"
			htmlCloseTag = "</blockquote>"
		} else if numElements != 1 {
			htmlTag = "<p>"
			htmlCloseTag = "</p>"
		}
		return fmt.Sprintf("%s%s%s", htmlTag, mc.renderRichTextSectionElements(ctx, e.Elements, mentions), htmlCloseTag)
	case *slack.RichTextList:
		var htmlText strings.Builder
		var htmlTag string
		var htmlCloseTag string
		if e.Style == "ordered" {
			htmlTag = "<ol>"
			htmlCloseTag = "</ol>"
		} else {
			htmlTag = "<ul>"
			htmlCloseTag = "</ul>"
		}
		htmlText.WriteString(htmlTag)
		for _, e := range e.Elements {
			htmlText.WriteString(fmt.Sprintf("<li>%s</li>", mc.renderSlackRichTextElement(ctx, 1, &e, mentions)))
		}
		htmlText.WriteString(htmlCloseTag)
		return htmlText.String()
	default:
		zerolog.Ctx(ctx).Debug().Type("element_type", e).Msg("Unsupported Slack rich text element")
		return fmt.Sprintf("<i>Unsupported section %s in Slack text.</i>", e.RichTextElementType())
	}
}

func (mc *MessageConverter) blocksToHTML(ctx context.Context, blocks slack.Blocks, alwaysWrap bool, mentions *event.Mentions) string {
	var htmlText strings.Builder

	if len(blocks.BlockSet) == 1 && !alwaysWrap {
		// don't wrap in <p> tag if there's only one block
		text, _ := mc.renderSlackBlock(ctx, blocks.BlockSet[0], mentions)
		htmlText.WriteString(text)
	} else {
		var lastBlockWasUnsupported bool = false
		for _, block := range blocks.BlockSet {
			text, unsupported := mc.renderSlackBlock(ctx, block, mentions)
			if !(unsupported && lastBlockWasUnsupported) {
				htmlText.WriteString(fmt.Sprintf("<p>%s</p>", text))
			}
			lastBlockWasUnsupported = unsupported
		}
	}

	return htmlText.String()
}

func (mc *MessageConverter) trySlackBlocksToMatrix(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, blocks slack.Blocks, attachments []slack.Attachment) *bridgev2.ConvertedMessagePart {
	converted, err := mc.slackBlocksToMatrix(ctx, portal, intent, blocks, attachments)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to render Slack blocks")
		return &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgNotice,
				Body:    "Failed to convert Slack message blocks",
			},
		}
	}
	return converted
}

func (mc *MessageConverter) slackBlocksToMatrix(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, blocks slack.Blocks, attachments []slack.Attachment) (*bridgev2.ConvertedMessagePart, error) {
	// Special case for bots like the Giphy bot which send images in a specific format
	if len(blocks.BlockSet) == 2 &&
		blocks.BlockSet[0].BlockType() == slack.MBTImage &&
		blocks.BlockSet[1].BlockType() == slack.MBTContext {
		imageBlock := blocks.BlockSet[0].(*slack.ImageBlock)
		return mc.renderImageBlock(ctx, portal, intent, *imageBlock)
	}

	mentions := &event.Mentions{}
	var htmlText strings.Builder

	htmlText.WriteString(mc.blocksToHTML(ctx, blocks, false, mentions))

	if len(attachments) > 0 && htmlText.String() != "" {
		htmlText.WriteString("<br>")
	}

	for _, attachment := range attachments {
		if attachment.IsMsgUnfurl {
			for _, message_block := range attachment.MessageBlocks {
				renderedAttachment := mc.blocksToHTML(ctx, message_block.Message.Blocks, true, mentions)
				htmlText.WriteString(fmt.Sprintf("<blockquote><b>%s</b><br>%s<a href=\"%s\"><i>%s</i></a><br></blockquote>",
					attachment.AuthorName, renderedAttachment, attachment.FromURL, attachment.Footer))
			}
		} else if len(attachment.Blocks.BlockSet) > 0 {
			for _, message_block := range attachment.Blocks.BlockSet {
				renderedAttachment, _ := mc.renderSlackBlock(ctx, message_block, mentions)
				htmlText.WriteString(fmt.Sprintf("<blockquote>%s</blockquote>", renderedAttachment))
			}
		} else {
			if len(attachment.Pretext) > 0 {
				htmlText.WriteString(fmt.Sprintf("<p>%s</p>", mc.mrkdwnToMatrixHtml(ctx, attachment.Pretext, mentions)))
			}
			var attachParts []string
			if len(attachment.AuthorName) > 0 {
				if len(attachment.AuthorLink) > 0 {
					attachParts = append(attachParts, fmt.Sprintf("<b><a href=\"%s\">%s</a></b>",
						attachment.AuthorLink, attachment.AuthorName))
				} else {
					attachParts = append(attachParts, fmt.Sprintf("<b>%s</b>", attachment.AuthorName))
				}
			}
			if len(attachment.Title) > 0 {
				if len(attachment.TitleLink) > 0 {
					attachParts = append(attachParts, fmt.Sprintf("<b><a href=\"%s\">%s</a></b>",
						attachment.TitleLink, mc.mrkdwnToMatrixHtml(ctx, attachment.Title, mentions)))
				} else {
					attachParts = append(attachParts, fmt.Sprintf("<b>%s</b>", mc.mrkdwnToMatrixHtml(ctx, attachment.Title, mentions)))
				}
			}
			if len(attachment.Text) > 0 {
				attachParts = append(attachParts, mc.mrkdwnToMatrixHtml(ctx, attachment.Text, mentions))
			} else if len(attachment.Fallback) > 0 {
				attachParts = append(attachParts, mc.mrkdwnToMatrixHtml(ctx, attachment.Fallback, mentions))
			}
			htmlText.WriteString(fmt.Sprintf("<blockquote>%s", strings.Join(attachParts, "<br>")))
			if len(attachment.Fields) > 0 {
				var fieldBody string
				var short = false
				for _, field := range attachment.Fields {
					if !short {
						fieldBody += "<tr>"
					}
					fieldBody += fmt.Sprintf("<td><strong>%s</strong><br>%s</td>",
						field.Title, mc.mrkdwnToMatrixHtml(ctx, field.Value, mentions))
					short = !short && field.Short
					if !short {
						fieldBody += "</tr>"
					}
				}
				htmlText.WriteString(fmt.Sprintf("<table>%s</table>", fieldBody))
			} else {
				htmlText.WriteString("<br>")
			}
			var footerParts []string
			if len(attachment.Footer) > 0 {
				footerParts = append(footerParts, mc.mrkdwnToMatrixHtml(ctx, attachment.Footer, mentions))
			}
			if len(attachment.Ts) > 0 {
				ts, _ := attachment.Ts.Int64()
				t := time.Unix(ts, 0)
				footerParts = append(footerParts, t.Local().Format("Jan 02, 2006 15:04:05 MST"))
			}
			if len(footerParts) > 0 {
				htmlText.WriteString(fmt.Sprintf("<sup>%s</sup>", strings.Join(footerParts, " | ")))
			}
			htmlText.WriteString("</blockquote>")
		}
	}

	content := format.HTMLToContent(htmlText.String())
	content.Mentions = mentions
	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: &content,
	}, nil
}
