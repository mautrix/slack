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

//go:build ignore

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"go.mau.fi/util/exerrors"
)

type SkinVariation struct {
	Unified        string  `json:"unified"`
	NonQualified   *string `json:"non_qualified"`
	Image          string  `json:"image"`
	SheetX         int     `json:"sheet_x"`
	SheetY         int     `json:"sheet_y"`
	AddedIn        string  `json:"added_in"`
	HasImgApple    bool    `json:"has_img_apple"`
	HasImgGoogle   bool    `json:"has_img_google"`
	HasImgTwitter  bool    `json:"has_img_twitter"`
	HasImgFacebook bool    `json:"has_img_facebook"`
	Obsoletes      string  `json:"obsoletes,omitempty"`
	ObsoletedBy    string  `json:"obsoleted_by,omitempty"`
}

type Emoji struct {
	Name           string                    `json:"name"`
	Unified        string                    `json:"unified"`
	NonQualified   *string                   `json:"non_qualified"`
	Docomo         *string                   `json:"docomo"`
	Au             *string                   `json:"au"`
	Softbank       *string                   `json:"softbank"`
	Google         *string                   `json:"google"`
	Image          string                    `json:"image"`
	SheetX         int                       `json:"sheet_x"`
	SheetY         int                       `json:"sheet_y"`
	ShortName      string                    `json:"short_name"`
	ShortNames     []string                  `json:"short_names"`
	Text           *string                   `json:"text"`
	Texts          []string                  `json:"texts"`
	Category       string                    `json:"category"`
	Subcategory    string                    `json:"subcategory"`
	SortOrder      int                       `json:"sort_order"`
	AddedIn        string                    `json:"added_in"`
	HasImgApple    bool                      `json:"has_img_apple"`
	HasImgGoogle   bool                      `json:"has_img_google"`
	HasImgTwitter  bool                      `json:"has_img_twitter"`
	HasImgFacebook bool                      `json:"has_img_facebook"`
	SkinVariations map[string]*SkinVariation `json:"skin_variations,omitempty"`
	Obsoletes      string                    `json:"obsoletes,omitempty"`
	ObsoletedBy    string                    `json:"obsoleted_by,omitempty"`
}

func unifiedToUnicode(input string) string {
	parts := strings.Split(input, "-")
	output := make([]rune, len(parts))
	for i, part := range parts {
		output[i] = rune(exerrors.Must(strconv.ParseInt(part, 16, 32)))
	}
	return string(output)
}

var skinToneIDs = map[string]string{
	"1F3FB": "2",
	"1F3FC": "3",
	"1F3FD": "4",
	"1F3FE": "5",
	"1F3FF": "6",
}

func unifiedToSkinToneID(input string) string {
	parts := strings.Split(input, "-")
	var ok bool
	for i, part := range parts {
		parts[i], ok = skinToneIDs[part]
		if !ok {
			panic("unknown skin tone " + input)
		}
	}
	return "skin-tone-" + strings.Join(parts, "-")
}

func getVariationSequences() (output map[string]struct{}) {
	variationSequences := exerrors.Must(http.Get("https://www.unicode.org/Public/15.1.0/ucd/emoji/emoji-variation-sequences.txt"))
	buf := bufio.NewReader(variationSequences.Body)
	output = make(map[string]struct{})
	for {
		line, err := buf.ReadString('\n')
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			panic(err)
		}
		parts := strings.Split(line, "; ")
		if len(parts) < 2 || parts[1] != "emoji style" {
			continue
		}
		unifiedParts := strings.Split(parts[0], " ")
		output[unifiedParts[0]] = struct{}{}
	}
	return
}

func main() {
	var emojis []Emoji
	resp := exerrors.Must(http.Get("https://raw.githubusercontent.com/iamcal/emoji-data/master/emoji.json"))
	exerrors.PanicIfNotNil(json.NewDecoder(resp.Body).Decode(&emojis))
	vs := getVariationSequences()

	shortcodeToEmoji := make(map[string]string)
	for _, emoji := range emojis {
		shortcodeToEmoji[emoji.ShortName] = unifiedToUnicode(emoji.Unified)
		if _, needsVariation := vs[emoji.Unified]; needsVariation {
			shortcodeToEmoji[emoji.ShortName] += "\ufe0f"
		}
		for skinToneKey, stEmoji := range emoji.SkinVariations {
			shortcodeToEmoji[fmt.Sprintf("%s::%s", emoji.ShortName, unifiedToSkinToneID(skinToneKey))] = unifiedToUnicode(stEmoji.Unified)
		}
	}
	file := exerrors.Must(os.OpenFile("emoji.json", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644))
	enc := json.NewEncoder(file)
	enc.SetIndent("", " ")
	exerrors.PanicIfNotNil(enc.Encode(shortcodeToEmoji))
	exerrors.PanicIfNotNil(file.Close())
}
