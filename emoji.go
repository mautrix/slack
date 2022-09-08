package main

import (
	_ "embed"
	"encoding/json"
	"regexp"
)

//go:embed resources/emoji.json
var emojiFileData []byte
var emojis map[string]string

var re regexp.Regexp = *regexp.MustCompile(`:[^:\s]*:`)

func replaceShortcodesWithEmojis(text string) string {
	return re.ReplaceAllStringFunc(text, shortcodeToEmoji)
}

func shortcodeToEmoji(code string) string {
	emoji, found := emojis[code[1:len(code)-1]]
	if found {
		return emoji
	} else {
		return code
	}
}

func emojiToShortcode(emoji string) string {
	for code, e := range emojis {
		if emoji == e {
			return code
		}
	}
	return ""
}

func init() {
	json.Unmarshal(emojiFileData, &emojis)
}
