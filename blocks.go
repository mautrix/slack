// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2022 Max Sandholm
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

package main

import (
	"fmt"
	"html"
	"io/ioutil"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/slack-go/slack"
	"github.com/yuin/goldmark"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"go.mau.fi/mautrix-slack/database"
)

func (portal *Portal) renderImageBlock(block slack.ImageBlock) (*event.MessageEventContent, error) {
	client := http.Client{}
	resp, err := client.Get(block.ImageURL)
	if err != nil {
		portal.log.Errorfln("Error fetching image: %v", err)
		return nil, err
	} else if resp.StatusCode != 200 {
		portal.log.Errorfln("HTTP error %d fetching image", resp.StatusCode)
		return nil, fmt.Errorf(resp.Status)
	}
	bytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		portal.log.Errorfln("Error fetching image: %v", err)
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
	err = portal.uploadMedia(portal.MainIntent(), bytes, &content)
	if err != nil {
		portal.log.Errorfln("Error uploading media: %v", err)
		return nil, err
	}
	return &content, nil
}

func (portal *Portal) mrkdwnToMatrixHtml(mrkdwn string) string {
	mrkdwn = replaceShortcodesWithEmojis(mrkdwn)

	mrkdwn = escapeFixer.ReplaceAllStringFunc(mrkdwn, func(s string) string {
		return s[:2] + `\` + s[2:]
	})

	mdRenderer := goldmark.New(
		format.Extensions, format.HTMLOptions,
		goldmark.WithExtensions(&SlackTag{portal}),
	)

	var buf strings.Builder
	mdRenderer.Convert([]byte(mrkdwn), &buf)

	return format.UnwrapSingleParagraph(buf.String())
}

func (portal *Portal) renderSlackTextBlock(block slack.TextBlockObject) string {
	if block.Type == slack.PlainTextType {
		return html.EscapeString(html.UnescapeString(block.Text))
	} else if block.Type == slack.MarkdownType {
		return portal.mrkdwnToMatrixHtml(block.Text)
	} else {
		return ""
	}
}

func (portal *Portal) renderRichTextSectionElements(elements []slack.RichTextSectionElement, userTeam *database.UserTeam) string {
	var htmlText strings.Builder
	for _, element := range elements {
		switch e := element.(type) {
		case *slack.RichTextSectionTextElement:
			if e.Style != nil {
				if e.Style.Bold {
					htmlText.WriteString("<b>")
				}
				if e.Style.Italic {
					htmlText.WriteString("<i>")
				}
				if e.Style.Strike {
					htmlText.WriteString("<del>")
				}
				if e.Style.Code {
					htmlText.WriteString("<code>")
				}
			}
			htmlText.WriteString(html.EscapeString(html.UnescapeString(e.Text)))
			if e.Style != nil {
				if e.Style.Code {
					htmlText.WriteString("</code>")
				}
				if e.Style.Strike {
					htmlText.WriteString("</del>")
				}
				if e.Style.Italic {
					htmlText.WriteString("</i>")
				}
				if e.Style.Bold {
					htmlText.WriteString("</b>")
				}
			}
		case *slack.RichTextSectionUserElement:
			puppet := portal.bridge.GetPuppetByID(portal.Key.TeamID, e.UserID)
			if puppet != nil && puppet.GetCustomOrGhostMXID() != "" {
				htmlText.WriteString(fmt.Sprintf(`<a href="https://matrix.to/#/%s">%s</a>`, puppet.GetCustomOrGhostMXID(), puppet.Name))
			} else { // TODO: register puppet and get info if not exist
				htmlText.WriteString(fmt.Sprintf("@%s", e.UserID))
			}
		case *slack.RichTextSectionChannelElement:
			p := portal.bridge.DB.Portal.GetByID(database.PortalKey{
				TeamID:    portal.Key.TeamID,
				ChannelID: e.ChannelID,
			})
			if p != nil && p.MXID != "" {
				htmlText.WriteString(fmt.Sprintf(`<a href="https://matrix.to/#/%s?via=%s">%s</a>`, p.MXID, portal.bridge.AS.HomeserverDomain, p.Name))
			} else { // TODO: get portal info if not exist
				htmlText.WriteString(fmt.Sprintf("#%s", e.ChannelID))
			}
		case *slack.RichTextSectionLinkElement:
			var linkText string
			if e.Text != "" {
				linkText = e.Text
			} else {
				linkText = e.URL
			}
			htmlText.WriteString(fmt.Sprintf(`<a href="%s">%s</a>`, e.URL, html.EscapeString(html.UnescapeString(linkText))))
		case *slack.RichTextSectionBroadcastElement:
			htmlText.WriteString("@room")
		case *slack.RichTextSectionEmojiElement:
			if e.Unicode != "" {
				codepoints := strings.Split(e.Unicode, "-")
				for _, codepoint := range codepoints {
					codepointInt, _ := strconv.ParseInt(codepoint, 16, 32)
					unquoted := string(rune(codepointInt))
					htmlText.WriteString(unquoted)
				}
			} else {
				emoji := portal.bridge.GetEmoji(e.Name, userTeam)
				if strings.HasPrefix(emoji, "mxc://") {
					htmlText.WriteString(fmt.Sprintf(`<img data-mx-emoticon src="%[1]s" alt="%[2]s" title="%[2]s" height="32"/>`, emoji, e.Name))
				} else if emoji != e.Name {
					htmlText.WriteString(emoji)
				} else {
					htmlText.WriteString(fmt.Sprintf(":%s:", e.Name))
				}
			}
		case *slack.RichTextSectionColorElement:
			htmlText.WriteString(e.Value)
		case *slack.RichTextSectionDateElement:
			htmlText.WriteString(e.Timestamp.String())
		default:
			portal.log.Warnfln("Slack rich text section contained unknown element %s", e.RichTextSectionElementType())
		}
	}
	return htmlText.String()
}

func (portal *Portal) renderSlackBlock(block slack.Block, userTeam *database.UserTeam) (string, bool) {
	switch b := block.(type) {
	case *slack.HeaderBlock:
		return fmt.Sprintf("<h1>%s</h1>", portal.renderSlackTextBlock(*b.Text)), false
	case *slack.DividerBlock:
		return "<hr>", false
	case *slack.SectionBlock:
		var htmlParts []string
		if b.Text != nil {
			htmlParts = append(htmlParts, portal.renderSlackTextBlock(*b.Text))
		}
		if len(b.Fields) > 0 {
			var fieldTable strings.Builder
			fieldTable.WriteString("<table>")
			for i, field := range b.Fields {
				if i%2 == 0 {
					fieldTable.WriteString("<tr>")
				}
				fieldTable.WriteString(fmt.Sprintf("<td>%s</td>", portal.mrkdwnToMatrixHtml(field.Text)))
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
			htmlText.WriteString(portal.renderSlackRichTextElement(len(b.Elements), element, userTeam))
		}
		return format.UnwrapSingleParagraph(htmlText.String()), false
	case *slack.ContextBlock:
		var htmlText strings.Builder
		var unsupported bool = false
		for _, element := range b.ContextElements.Elements {
			if mrkdwnElem, ok := element.(*slack.TextBlockObject); ok {
				htmlText.WriteString(fmt.Sprintf("<sup>%s</sup>", portal.mrkdwnToMatrixHtml(mrkdwnElem.Text)))
			} else {
				portal.log.Debugfln("Unsupported Slack block element: %s", element.MixedElementType())
				htmlText.WriteString("<i>Slack message contains unsupported elements.</i>")
				unsupported = true
			}
		}
		return htmlText.String(), unsupported
	default:
		portal.log.Debugfln("Unsupported Slack block: %s", b.BlockType())
		return "<i>Slack message contains unsupported elements.</i>", true
	}
}

func (portal *Portal) renderSlackRichTextElement(numElements int, element slack.RichTextElement, userTeam *database.UserTeam) string {
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
		return fmt.Sprintf("%s%s%s", htmlTag, portal.renderRichTextSectionElements(e.Elements, userTeam), htmlCloseTag)
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
			htmlText.WriteString(fmt.Sprintf("<li>%s</li>", portal.renderSlackRichTextElement(1, &e, userTeam)))
		}
		htmlText.WriteString(htmlCloseTag)
		return htmlText.String()
	default:
		portal.log.Debugfln("Unsupported Slack section: %T", e)
		return fmt.Sprintf("<i>Unsupported section %s in Slack text.</i>", e.RichTextElementType())
	}
}

func (portal *Portal) blocksToHtml(blocks slack.Blocks, alwaysWrap bool, userTeam *database.UserTeam) string {
	var htmlText strings.Builder

	if len(blocks.BlockSet) == 1 && !alwaysWrap {
		// don't wrap in <p> tag if there's only one block
		text, _ := portal.renderSlackBlock(blocks.BlockSet[0], userTeam)
		htmlText.WriteString(text)
	} else {
		var lastBlockWasUnsupported bool = false
		for _, block := range blocks.BlockSet {
			text, unsupported := portal.renderSlackBlock(block, userTeam)
			if !(unsupported && lastBlockWasUnsupported) {
				htmlText.WriteString(fmt.Sprintf("<p>%s</p>", text))
			}
			lastBlockWasUnsupported = unsupported
		}
	}

	return htmlText.String()
}

func (portal *Portal) SlackBlocksToMatrix(blocks slack.Blocks, attachments []slack.Attachment, userTeam *database.UserTeam) (*event.MessageEventContent, error) {

	// Special case for bots like the Giphy bot which send images in a specific format
	if len(blocks.BlockSet) == 2 &&
		blocks.BlockSet[0].BlockType() == slack.MBTImage &&
		blocks.BlockSet[1].BlockType() == slack.MBTContext {
		imageBlock := blocks.BlockSet[0].(*slack.ImageBlock)
		return portal.renderImageBlock(*imageBlock)
	}

	var htmlText strings.Builder

	htmlText.WriteString(portal.blocksToHtml(blocks, false, userTeam))

	if len(attachments) > 0 && htmlText.String() != "" {
		htmlText.WriteString("<br>")
	}

	for _, attachment := range attachments {
		if attachment.IsMsgUnfurl {
			for _, message_block := range attachment.MessageBlocks {
				renderedAttachment := portal.blocksToHtml(message_block.Message.Blocks, true, userTeam)
				htmlText.WriteString(fmt.Sprintf("<blockquote><b>%s</b><br>%s<a href=\"%s\"><i>%s</i></a><br></blockquote>",
					attachment.AuthorName, renderedAttachment, attachment.FromURL, attachment.Footer))
			}
		} else if len(attachment.Blocks.BlockSet) > 0 {
			for _, message_block := range attachment.Blocks.BlockSet {
				renderedAttachment, _ := portal.renderSlackBlock(message_block, userTeam)
				htmlText.WriteString(fmt.Sprintf("<blockquote>%s</blockquote>", renderedAttachment))
			}
		} else {
			if len(attachment.Pretext) > 0 {
				htmlText.WriteString(fmt.Sprintf("<p>%s</p>", portal.mrkdwnToMatrixHtml(attachment.Pretext)))
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
						attachment.TitleLink, portal.mrkdwnToMatrixHtml(attachment.Title)))
				} else {
					attachParts = append(attachParts, fmt.Sprintf("<b>%s</b>", portal.mrkdwnToMatrixHtml(attachment.Title)))
				}
			}
			if len(attachment.Text) > 0 {
				attachParts = append(attachParts, portal.mrkdwnToMatrixHtml(attachment.Text))
			} else if len(attachment.Fallback) > 0 {
				attachParts = append(attachParts, portal.mrkdwnToMatrixHtml(attachment.Fallback))
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
						field.Title, portal.mrkdwnToMatrixHtml(field.Value))
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
				footerParts = append(footerParts, portal.mrkdwnToMatrixHtml(attachment.Footer))
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
	return &content, nil
}
