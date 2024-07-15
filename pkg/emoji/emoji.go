// mautrix-slack - A Matrix-Slack puppeting bridge.
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

package emoji

import (
	"embed"
	"encoding/json"
	"regexp"
	"strings"
	"sync"

	"go.mau.fi/util/exerrors"
)

//go:generate go run ./emoji-generate.go
//go:embed emoji.json
var emojiFileData embed.FS

var shortcodeToUnicodeMap map[string]string
var unicodeToShortcodeMap map[string]string
var shortcodeRegex *regexp.Regexp
var initOnce sync.Once

func doInit() {
	file := exerrors.Must(emojiFileData.Open("emoji.json"))
	exerrors.PanicIfNotNil(json.NewDecoder(file).Decode(&shortcodeToUnicodeMap))
	exerrors.PanicIfNotNil(file.Close())
	unicodeToShortcodeMap = make(map[string]string, len(shortcodeToUnicodeMap))
	for shortcode, emoji := range shortcodeToUnicodeMap {
		unicodeToShortcodeMap[emoji] = shortcode
	}
	shortcodeRegex = regexp.MustCompile(`:[^:\s]*:`)
}

func GetShortcode(unicode string) string {
	initOnce.Do(doInit)
	return unicodeToShortcodeMap[unicode]
}

func GetUnicode(shortcode string) string {
	return shortcodeToUnicodeMap[strings.Trim(shortcode, ":")]
}

func replaceShortcode(code string) string {
	emoji := GetUnicode(code)
	if emoji == "" {
		return code
	}
	return emoji
}

func ReplaceShortcodesWithUnicode(text string) string {
	initOnce.Do(doInit)
	return shortcodeRegex.ReplaceAllStringFunc(text, replaceShortcode)
}
