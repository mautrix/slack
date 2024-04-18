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

package main

import (
	"context"
	_ "embed"
	"strings"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/id"

	"github.com/slack-go/slack"
	"go.mau.fi/mautrix-slack/database"
	"go.mau.fi/mautrix-slack/msgconv/emoji"
)

func (ut *UserTeam) handleEmojiChange(ctx context.Context, evt *slack.EmojiChangedEvent) {
	ut.Team.emojiLock.Lock()
	defer ut.Team.emojiLock.Unlock()
	log := zerolog.Ctx(ctx)
	log.UpdateContext(func(c zerolog.Context) zerolog.Context {
		return c.Str("subtype", evt.SubType)
	})
	switch evt.SubType {
	case "add":
		ut.addEmoji(ctx, evt.Name, evt.Value)
	case "remove":
		err := ut.bridge.DB.Emoji.DeleteMany(ctx, ut.TeamID, evt.Names...)
		if err != nil {
			log.Err(err).Strs("emoji_ids", evt.Names).Msg("Failed to delete emojis from database")
		}
	case "rename":
		dbEmoji, err := ut.bridge.DB.Emoji.GetBySlackID(ctx, ut.TeamID, evt.OldName)
		if err != nil {
			log.Err(err).Msg("Failed to get emoji from database for renaming")
		} else if dbEmoji == nil || dbEmoji.Value != evt.Value {
			log.Warn().Msg("Old emoji not found for renaming, adding new one")
			ut.addEmoji(ctx, evt.NewName, evt.Value)
		} else if err = dbEmoji.Rename(ctx, evt.NewName); err != nil {
			log.Err(err).Msg("Failed to rename emoji in database")
		}
	default:
		log.Warn().Msg("Unknown emoji change subtype, resyncing emojis")
		err := ut.syncEmojis(ctx, false)
		if err != nil {
			log.Err(err).Msg("Failed to resync emojis")
		}
	}
}

func (ut *UserTeam) addEmoji(ctx context.Context, emojiName, emojiValue string) *database.Emoji {
	log := zerolog.Ctx(ctx)
	dbEmoji, err := ut.bridge.DB.Emoji.GetBySlackID(ctx, ut.TeamID, emojiName)
	if err != nil {
		log.Err(err).
			Str("emoji_name", emojiName).
			Str("emoji_value", emojiValue).
			Msg("Failed to check if emoji already exists")
		return nil
	}
	var newAlias string
	var newImageMXC id.ContentURI
	if strings.HasPrefix(emojiValue, "alias:") {
		newAlias = ut.TryGetEmoji(ctx, strings.TrimPrefix(emojiValue, "alias:"))
		if strings.HasPrefix(newAlias, "mxc://") {
			newImageMXC, _ = id.ParseContentURI(newAlias)
		}
		if dbEmoji != nil && dbEmoji.Value == emojiValue && dbEmoji.Alias == newAlias && dbEmoji.ImageMXC == newImageMXC {
			return dbEmoji
		}
	} else {
		if dbEmoji != nil && dbEmoji.Value == emojiValue {
			return dbEmoji
		}
		// Don't reupload emojis that are only missing the value column (but do set the value column so it's there in the future)
		if dbEmoji == nil || dbEmoji.Value != "" || dbEmoji.ImageMXC.IsEmpty() {
			newImageMXC, err = uploadPlainFile(ctx, ut.bridge.Bot, emojiValue)
			if err != nil {
				log.Err(err).
					Str("emoji_name", emojiName).
					Str("emoji_value", emojiValue).
					Msg("Failed to reupload emoji")
				return nil
			}
		}
	}
	if dbEmoji == nil {
		dbEmoji = ut.bridge.DB.Emoji.New()
		dbEmoji.TeamID = ut.TeamID
		dbEmoji.EmojiID = emojiName
	}
	dbEmoji.Value = emojiValue
	dbEmoji.Alias = newAlias
	dbEmoji.ImageMXC = newImageMXC
	err = dbEmoji.Upsert(ctx)
	if err != nil {
		log.Err(err).
			Str("emoji_name", emojiName).
			Str("emoji_value", emojiValue).
			Msg("Failed to save custom emoji to database")
	}
	return dbEmoji
}

func (ut *UserTeam) ResyncEmojisDueToNotFound(ctx context.Context) bool {
	if !ut.Team.emojiLock.TryLock() {
		return false
	}
	defer ut.Team.emojiLock.Unlock()
	err := ut.syncEmojis(ctx, false)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to sync emojis after emoji wasn't found")
		return false
	}
	return true
}

func (ut *UserTeam) SyncEmojis(ctx context.Context) {
	ut.Team.emojiLock.Lock()
	defer ut.Team.emojiLock.Lock()
	err := ut.syncEmojis(ctx, true)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to sync emojis")
	}
}

func (ut *UserTeam) syncEmojis(ctx context.Context, onlyIfCountMismatch bool) error {
	log := zerolog.Ctx(ctx).With().Str("action", "sync emojis").Logger()
	resp, err := ut.Client.GetEmojiContext(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to fetch emoji list")
		return err
	}
	if onlyIfCountMismatch {
		emojiCount, err := ut.bridge.DB.Emoji.GetEmojiCount(ctx, ut.TeamID)
		if err != nil {
			log.Err(err).Msg("Failed to get emoji count from database")
			return nil
		} else if emojiCount == len(resp) {
			return nil
		}
	}

	deferredAliases := make(map[string]string)
	uploaded := make(map[string]id.ContentURI, len(resp))
	existingIDs := make([]string, 0, len(resp))

	for key, url := range resp {
		existingIDs = append(existingIDs, key)
		if strings.HasPrefix(url, "alias:") {
			deferredAliases[key] = strings.TrimPrefix(url, "alias:")
		} else {
			addedEmoji := ut.addEmoji(ctx, key, url)
			if addedEmoji != nil && !addedEmoji.ImageMXC.IsEmpty() {
				uploaded[key] = addedEmoji.ImageMXC
			}
		}
	}

	for key, alias := range deferredAliases {
		dbEmoji := ut.bridge.DB.Emoji.New()
		dbEmoji.EmojiID = key
		dbEmoji.TeamID = ut.TeamID
		if uri, ok := uploaded[alias]; ok {
			dbEmoji.Alias = alias
			dbEmoji.ImageMXC = uri
		} else if unicode, ok := emoji.ShortcodeToUnicodeMap[alias]; ok {
			dbEmoji.Alias = unicode
		}
		err = dbEmoji.Upsert(ctx)
		if err != nil {
			log.Err(err).
				Str("emoji_id", key).
				Str("alias", alias).
				Msg("Failed to save deferred emoji alias to database")
		}
	}

	emojiCount, err := ut.bridge.DB.Emoji.GetEmojiCount(ctx, ut.TeamID)
	if err != nil {
		log.Err(err).Msg("Failed to get emoji count from database to check if emojis need to be pruned")
	} else if emojiCount > len(resp) {
		err = ut.bridge.DB.Emoji.Prune(ctx, ut.TeamID, existingIDs...)
		if err != nil {
			log.Err(err).Msg("Failed to prune removed emojis from database")
		}
	}

	return nil
}

func (ut *UserTeam) TryGetEmoji(ctx context.Context, shortcode string) string {
	unicode, ok := emoji.ShortcodeToUnicodeMap[shortcode]
	if ok {
		return unicode
	}

	dbEmoji, err := ut.bridge.DB.Emoji.GetBySlackID(ctx, ut.TeamID, shortcode)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Str("shortcode", shortcode).Msg("Failed to get emoji from database")
		return ""
	} else if dbEmoji != nil && !dbEmoji.ImageMXC.IsEmpty() {
		return dbEmoji.ImageMXC.String()
	} else if dbEmoji != nil {
		return dbEmoji.Alias
	} else {
		return ""
	}
}

func (ut *UserTeam) GetEmoji(ctx context.Context, shortcode string) string {
	emojiVal := ut.TryGetEmoji(ctx, shortcode)
	if emojiVal == "" && ut.ResyncEmojisDueToNotFound(ctx) {
		emojiVal = ut.TryGetEmoji(ctx, shortcode)
	}
	if emojiVal == "" {
		emojiVal = shortcode
	}
	return emojiVal
}
