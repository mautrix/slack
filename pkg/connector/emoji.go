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

package connector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-slack/pkg/connector/slackdb"
	"go.mau.fi/mautrix-slack/pkg/emoji"
)

func (s *SlackClient) handleEmojiChange(ctx context.Context, evt *slack.EmojiChangedEvent) {
	defer s.Main.DB.Emoji.WithLock(s.TeamID)()
	log := zerolog.Ctx(ctx)
	log.UpdateContext(func(c zerolog.Context) zerolog.Context {
		return c.Str("subtype", evt.SubType)
	})
	switch evt.SubType {
	case "add":
		s.addEmoji(ctx, evt.Name, evt.Value)
		log.Debug().Str("emoji_id", evt.Name).Msg("Handled emoji addition")
	case "remove":
		err := s.Main.DB.Emoji.DeleteMany(ctx, s.TeamID, evt.Names...)
		if err != nil {
			log.Err(err).Strs("emoji_ids", evt.Names).Msg("Failed to delete emojis from database")
		} else {
			log.Debug().Strs("emoji_ids", evt.Names).Msg("Handled emoji deletion")
		}
	case "rename":
		dbEmoji, err := s.Main.DB.Emoji.GetBySlackID(ctx, s.TeamID, evt.OldName)
		if err != nil {
			log.Err(err).Msg("Failed to get emoji from database for renaming")
		} else if dbEmoji == nil || dbEmoji.Value != evt.Value {
			log.Warn().Msg("Old emoji not found for renaming, adding new one")
			s.addEmoji(ctx, evt.NewName, evt.Value)
		} else if err = s.Main.DB.Emoji.Rename(ctx, dbEmoji, evt.NewName); err != nil {
			log.Err(err).Msg("Failed to rename emoji in database")
		} else {
			log.Debug().Str("old_id", evt.OldName).Str("new_name", evt.NewName).Msg("Handled emoji rename")
		}
	default:
		log.Warn().Msg("Unknown emoji change subtype, resyncing emojis")
		err := s.syncEmojis(ctx, false)
		if err != nil {
			log.Err(err).Msg("Failed to resync emojis")
		}
	}
}

func (s *SlackClient) addEmoji(ctx context.Context, emojiName, emojiValue string) *slackdb.Emoji {
	log := zerolog.Ctx(ctx)
	dbEmoji, err := s.Main.DB.Emoji.GetBySlackID(ctx, s.TeamID, emojiName)
	if err != nil {
		log.Err(err).
			Str("emoji_name", emojiName).
			Str("emoji_value", emojiValue).
			Msg("Failed to check if emoji already exists")
		return nil
	}
	var newAlias string
	var newImageMXC id.ContentURIString
	if strings.HasPrefix(emojiValue, "alias:") {
		newAlias = strings.TrimPrefix(emojiValue, "alias:")
		aliasTarget, isImage, _ := s.tryGetEmoji(ctx, newAlias, false, false)
		if isImage {
			newImageMXC = id.ContentURIString(aliasTarget)
		}
		if dbEmoji != nil && dbEmoji.Value == emojiValue && dbEmoji.Alias == newAlias && dbEmoji.ImageMXC == newImageMXC {
			return dbEmoji
		}
	} else if dbEmoji != nil && dbEmoji.Value == emojiValue {
		return dbEmoji
	}
	if dbEmoji == nil {
		dbEmoji = &slackdb.Emoji{
			TeamID:  s.TeamID,
			EmojiID: emojiName,
		}
	}
	dbEmoji.Value = emojiValue
	dbEmoji.Alias = newAlias
	dbEmoji.ImageMXC = newImageMXC
	err = s.Main.DB.Emoji.Put(ctx, dbEmoji)
	if err != nil {
		log.Err(err).
			Str("emoji_name", emojiName).
			Str("emoji_value", emojiValue).
			Msg("Failed to save custom emoji to database")
	}
	return dbEmoji
}

func downloadPlainFile(ctx context.Context, url, thing string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare request: %w", err)
	}

	getResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download %s: %w", thing, err)
	}

	data, err := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to read %s data: %w", thing, err)
	}
	return data, nil
}

func reuploadEmoji(ctx context.Context, intent bridgev2.MatrixAPI, url string) (id.ContentURIString, error) {
	data, err := downloadPlainFile(ctx, url, "emoji")
	if err != nil {
		return "", err
	}

	mime := http.DetectContentType(data)
	resp, _, err := intent.UploadMedia(ctx, "", data, "", mime)
	if err != nil {
		return "", fmt.Errorf("failed to upload avatar to Matrix: %w", err)
	}

	return resp, nil
}

func (s *SlackClient) ResyncEmojisDueToNotFound(ctx context.Context) bool {
	lock := s.Main.DB.Emoji.GetLock(s.TeamID)
	if !lock.TryLock() {
		return false
	}
	defer lock.Unlock()
	err := s.syncEmojis(ctx, false)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to sync emojis after emoji wasn't found")
		return false
	}
	return true
}

func (s *SlackClient) SyncEmojis(ctx context.Context) {
	defer s.Main.DB.Emoji.WithLock(s.TeamID)()
	err := s.syncEmojis(ctx, true)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to sync emojis")
	}
}

func (s *SlackClient) syncEmojis(ctx context.Context, onlyIfCountMismatch bool) error {
	log := zerolog.Ctx(ctx).With().Str("action", "sync emojis").Logger()
	resp, err := s.Client.GetEmojiContext(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to fetch emoji list")
		return err
	}
	if onlyIfCountMismatch {
		emojiCount, err := s.Main.DB.Emoji.GetEmojiCount(ctx, s.TeamID)
		if err != nil {
			log.Err(err).Msg("Failed to get emoji count from database")
			return nil
		} else if emojiCount == len(resp) {
			log.Debug().Int("emoji_count", len(resp)).Msg("Not syncing emojis: count is already correct")
			return nil
		}
		log.Debug().
			Int("emoji_count", len(resp)).
			Int("cached_emoji_count", emojiCount).
			Msg("Syncing team emojis as server has different number than cache")
	} else {
		log.Debug().Int("emoji_count", len(resp)).Msg("Syncing team emojis (didn't check cache)")
	}

	deferredAliases := make(map[string]string)
	created := make(map[string]*slackdb.Emoji, len(resp))
	existingIDs := make([]string, 0, len(resp))

	for key, url := range resp {
		existingIDs = append(existingIDs, key)
		if strings.HasPrefix(url, "alias:") {
			deferredAliases[key] = strings.TrimPrefix(url, "alias:")
		} else {
			addedEmoji := s.addEmoji(ctx, key, url)
			if addedEmoji != nil {
				created[key] = addedEmoji
			}
		}
	}

	for key, alias := range deferredAliases {
		dbEmoji := &slackdb.Emoji{
			TeamID:  s.TeamID,
			EmojiID: key,
			Value:   fmt.Sprintf("alias:%s", alias),
			Alias:   alias,
		}
		if otherEmoji, ok := created[alias]; ok {
			dbEmoji.ImageMXC = otherEmoji.ImageMXC
		}
		err = s.Main.DB.Emoji.Put(ctx, dbEmoji)
		if err != nil {
			log.Err(err).
				Str("emoji_id", dbEmoji.EmojiID).
				Str("alias", dbEmoji.Alias).
				Str("image_mxc", string(dbEmoji.ImageMXC)).
				Msg("Failed to save deferred emoji alias to database")
		}
	}

	emojiCount, err := s.Main.DB.Emoji.GetEmojiCount(ctx, s.TeamID)
	if err != nil {
		log.Err(err).Msg("Failed to get emoji count from database to check if emojis need to be pruned")
	} else if emojiCount > len(resp) {
		err = s.Main.DB.Emoji.Prune(ctx, s.TeamID, existingIDs...)
		if err != nil {
			log.Err(err).Msg("Failed to prune removed emojis from database")
		}
	}

	return nil
}

func (s *SlackClient) tryGetEmoji(ctx context.Context, shortcode string, ensureUploaded, allowRecurse bool) (val string, isImage, found bool) {
	if unicode := emoji.GetUnicode(shortcode); unicode != "" {
		return unicode, false, true
	}

	dbEmoji, err := s.Main.DB.Emoji.GetBySlackID(ctx, s.TeamID, shortcode)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Str("shortcode", shortcode).Msg("Failed to get emoji from database")
		return
	} else if dbEmoji == nil {
		return
	}
	found = true
	if dbEmoji.ImageMXC != "" {
		val = string(dbEmoji.ImageMXC)
		isImage = true
	} else if strings.HasPrefix(dbEmoji.Value, "alias:") {
		if !allowRecurse {
			zerolog.Ctx(ctx).Warn().Str("shortcode", shortcode).Msg("Not recursing into emoji alias")
			return "", false, true
		}
		val, isImage, _ = s.tryGetEmoji(ctx, strings.TrimPrefix(dbEmoji.Value, "alias:"), ensureUploaded, false)
	} else if ensureUploaded {
		defer s.Main.DB.Emoji.WithLock(s.TeamID)()
		dbEmoji.ImageMXC, err = reuploadEmoji(ctx, s.Main.br.Bot, dbEmoji.Value)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).
				Str("shortcode", shortcode).
				Str("url", dbEmoji.Value).
				Msg("Failed to reupload emoji")
			return
		}
		err = s.Main.DB.Emoji.SaveMXC(ctx, dbEmoji)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).
				Str("shortcode", shortcode).
				Str("url", dbEmoji.Value).
				Str("mxc", string(dbEmoji.ImageMXC)).
				Msg("Failed to save reuploaded emoji")
		}
		val = string(dbEmoji.ImageMXC)
		isImage = true
	}
	return
}

func (s *SlackClient) GetEmoji(ctx context.Context, shortcode string) (string, bool) {
	emojiVal, isImage, found := s.tryGetEmoji(ctx, shortcode, true, true)
	if !found && s.ResyncEmojisDueToNotFound(ctx) {
		emojiVal, isImage, _ = s.tryGetEmoji(ctx, shortcode, true, true)
	}
	if emojiVal == "" {
		emojiVal = fmt.Sprintf(":%s:", shortcode)
		isImage = false
	}
	return emojiVal, isImage
}
