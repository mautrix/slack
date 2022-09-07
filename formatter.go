package main

// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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

import (
	"regexp"
	"strings"

	"github.com/slack-go/slack"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/util"
)

var escapeFixer = regexp.MustCompile(`\\(__[^_]|\*\*[^*])`)

func (portal *Portal) renderSlackMarkdown(text string) event.MessageEventContent {
	text = replaceShortcodesWithEmojis(text)

	text = escapeFixer.ReplaceAllStringFunc(text, func(s string) string {
		return s[:2] + `\` + s[2:]
	})

	return format.RenderMarkdown(text, true, false)
}

func (portal *Portal) renderSlackFile(file slack.File) event.MessageEventContent {
	content := event.MessageEventContent{
		Info: &event.FileInfo{
			MimeType: file.Mimetype,
			Size:     int(file.Size),
		},
	}
	if file.OriginalW != 0 {
		content.Info.Width = file.OriginalW
	}
	if file.OriginalH != 0 {
		content.Info.Height = file.OriginalH
	}
	if file.Name != "" {
		content.Body = file.Name
	} else {
		mimeClass := strings.Split(file.Mimetype, "/")[0]
		switch mimeClass {
		case "application":
			content.Body = "file"
		default:
			content.Body = mimeClass
		}

		content.Body += util.ExtensionFromMimetype(file.Mimetype)
	}

	// Slack only gives us a "filetype" and they document a non-exhaustive list of values that may have.
	// I guess we're going with that. Also added a few other ones that weren't on their list.
	switch file.Filetype {
	case "bmp", "gif", "jpg", "png", "svg", "tiff", "webp":
		content.MsgType = event.MsgImage
	case "flv", "mkv", "mov", "mp4", "mpg", "ogv", "webm", "wmv":
		content.MsgType = event.MsgVideo
	case "m4a", "mp3", "ogg", "wav", "opus", "flac":
		content.MsgType = event.MsgAudio
	default:
		content.MsgType = event.MsgFile
	}

	return content
}
