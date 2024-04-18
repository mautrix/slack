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
	_ "embed"
	"encoding/json"
	"regexp"
	"strings"

	"go.mau.fi/util/exerrors"
)

//go:generate go run ./emoji-generate.go
//go:embed emoji.json
var emojiFileData []byte

var ShortcodeToUnicodeMap map[string]string
var UnicodeToShortcodeMap map[string]string

func init() {
	exerrors.PanicIfNotNil(json.Unmarshal(emojiFileData, &ShortcodeToUnicodeMap))
	UnicodeToShortcodeMap = make(map[string]string, len(ShortcodeToUnicodeMap))
	for shortcode, emoji := range ShortcodeToUnicodeMap {
		UnicodeToShortcodeMap[emoji] = shortcode
	}
}

var ShortcodeRegex = regexp.MustCompile(`:[^:\s]*:`)

func ReplaceShortcodesWithUnicode(text string) string {
	return ShortcodeRegex.ReplaceAllStringFunc(text, func(code string) string {
		strippedCode := strings.Trim(code, ":")
		emoji, found := ShortcodeToUnicodeMap[strippedCode]
		if found {
			return emoji
		} else {
			return code
		}
	})
}
