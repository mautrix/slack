package main

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"strings"

	"go.mau.fi/mautrix-slack/database"
	"maunium.net/go/mautrix/id"
)

//go:embed resources/emoji.json
var emojiFileData []byte
var emojis map[string]string

var re regexp.Regexp = *regexp.MustCompile(`:[^:\s]*:`)

func replaceShortcodesWithEmojis(text string) string {
	return re.ReplaceAllStringFunc(text, shortcodeToEmoji)
}

func convertSlackReaction(text string) string {
	var converted string
	emoji := strings.Split(text, "::")
	for _, e := range emoji {
		converted += shortcodeToEmoji(e)
	}
	return converted
}

func shortcodeToEmoji(code string) string {
	strippedCode := strings.Trim(code, ":")
	emoji, found := emojis[strippedCode]
	if found {
		return emoji
	} else {
		return code
	}
}

func emojiToShortcode(emoji string) string {
	var partCodes []string
	for _, r := range withoutVariationSelector(emoji) {
		for code, e := range emojis {
			if string(r) == withoutVariationSelector(e) {
				partCodes = append(partCodes, code)
				continue
			}
		}
	}
	return strings.Join(partCodes, "::")
}

func withoutVariationSelector(str string) string {
	return strings.Map(func(r rune) rune {
		if r == '\ufe0f' {
			return -1
		}
		return r
	}, str)
}

func init() {
	json.Unmarshal(emojiFileData, &emojis)
}

func (br *SlackBridge) ImportEmojis(userTeam *database.UserTeam, list *map[string]string, overwrite bool) error {
	if list == nil {
		resp, err := userTeam.Client.GetEmoji()
		if err != nil {
			br.ZLog.Err(err).Msg("failed to fetch emoji list from Slack")
			return err
		}
		list = &resp
	}

	deferredAliases := map[string]string{}
	uploaded := map[string]id.ContentURI{}

	for key, url := range *list {
		existing := br.DB.Emoji.GetBySlackID(key, userTeam.Key.TeamID)
		if existing == nil || overwrite {
			if strings.HasPrefix(url, "alias:") {
				deferredAliases[key] = strings.TrimPrefix(url, "alias:")
				continue
			}

			uri, err := uploadPlainFile(br.AS.BotIntent(), url)
			if err != nil {
				br.ZLog.Err(err).Str("url", url).Msg("failed to upload emoji to matrix")
				continue
			}

			uploaded[key] = uri

			dbEmoji := br.DB.Emoji.New()
			dbEmoji.SlackID = key
			dbEmoji.SlackTeam = userTeam.Key.TeamID
			dbEmoji.ImageURL = uri
			dbEmoji.Upsert(nil)
		}
	}

	for key, alias := range deferredAliases {
		if uri, ok := uploaded[alias]; ok {
			dbEmoji := br.DB.Emoji.New()
			dbEmoji.SlackID = key
			dbEmoji.SlackTeam = userTeam.Key.TeamID
			dbEmoji.Alias = alias
			dbEmoji.ImageURL = uri
			dbEmoji.Upsert(nil)
		} else if unicode := shortcodeToEmoji(alias); unicode != alias {
			dbEmoji := br.DB.Emoji.New()
			dbEmoji.SlackID = key
			dbEmoji.SlackTeam = userTeam.Key.TeamID
			dbEmoji.Alias = unicode
			dbEmoji.Upsert(nil)
		}
	}

	return nil
}

func (br *SlackBridge) GetEmoji(shortcode string, userTeam *database.UserTeam) string {
	converted := convertSlackReaction(shortcode)
	if converted != shortcode {
		return converted
	}

	dbEmoji := br.DB.Emoji.GetBySlackID(shortcode, userTeam.Key.TeamID)
	if dbEmoji == nil {
		br.ImportEmojis(userTeam, nil, false)
		dbEmoji = br.DB.Emoji.GetBySlackID(shortcode, userTeam.Key.TeamID)
	}

	if dbEmoji != nil && !dbEmoji.ImageURL.IsEmpty() {
		return dbEmoji.ImageURL.String()
	} else if dbEmoji != nil {
		return dbEmoji.Alias
	} else {
		return shortcode
	}
}
