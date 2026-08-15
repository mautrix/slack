// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
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
	"slices"
	"strings"

	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/mediaproxy"
)

type MediaIDType byte

const (
	MediaIDTypeEmoji MediaIDType = 1
)

func (s *SlackConnector) Download(_ context.Context, mediaID networkid.MediaID, _ map[string]string) (mediaproxy.GetMediaResponse, error) {
	rawParsedID, err := ParseMediaID(mediaID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse media ID: %w", err)
	}
	switch parsedID := rawParsedID.(type) {
	case *DirectMediaEmoji:
		return &mediaproxy.GetMediaResponseURL{
			URL: parsedID.URL(),
		}, nil
	default:
		return nil, fmt.Errorf("unknown media ID type: %T", parsedID)
	}
}

func ParseMediaID(mediaID networkid.MediaID) (any, error) {
	if len(mediaID) == 0 {
		return nil, fmt.Errorf("empty media ID")
	}
	mediaIDType := MediaIDType(mediaID[0])
	mediaID = mediaID[1:]
	switch mediaIDType {
	case MediaIDTypeEmoji:
		teamID, mediaID, err := readShortString(mediaID)
		if err != nil {
			return nil, fmt.Errorf("failed to read team ID: %w", err)
		}
		emojiID, mediaID, err := readShortString(mediaID)
		if err != nil {
			return nil, fmt.Errorf("failed to read emoji ID: %w", err)
		}
		fileName, _, err := readShortString(mediaID)
		if err != nil {
			return nil, fmt.Errorf("failed to read file name: %w", err)
		}
		return &DirectMediaEmoji{
			TeamID:   teamID,
			EmojiID:  emojiID,
			FileName: fileName,
		}, nil
	default:
		return nil, fmt.Errorf("unknown media ID type: %d", mediaIDType)
	}
}

const slackEmojiURLPrefix = "https://emoji.slack-edge.com/"

type DirectMediaEmoji struct {
	TeamID   string
	EmojiID  string
	FileName string
}

func DirectMediaEmojiFromURL(url string) *DirectMediaEmoji {
	data, ok := strings.CutPrefix(url, slackEmojiURLPrefix)
	if !ok {
		return nil
	}
	parts := strings.Split(data, "/")
	if len(parts) != 3 {
		return nil
	}
	teamID := parts[0]
	emojiID := parts[1]
	fileName := parts[2]
	if len(teamID) > 16 || len(emojiID) > 100 || len(fileName) > 32 {
		return nil
	}
	return &DirectMediaEmoji{
		TeamID:   teamID,
		EmojiID:  emojiID,
		FileName: fileName,
	}
}

func (dme *DirectMediaEmoji) URL() string {
	if dme == nil {
		return ""
	}
	return fmt.Sprintf("%s%s/%s/%s", slackEmojiURLPrefix, dme.TeamID, dme.EmojiID, dme.FileName)
}

func (dme *DirectMediaEmoji) MediaID() networkid.MediaID {
	if dme == nil || len(dme.TeamID) > 16 || len(dme.EmojiID) > 100 || len(dme.FileName) > 32 {
		return nil
	}
	return slices.Concat(
		[]byte{byte(MediaIDTypeEmoji)},
		[]byte{byte(len(dme.TeamID))},
		[]byte(dme.TeamID),
		[]byte{byte(len(dme.EmojiID))},
		[]byte(dme.EmojiID),
		[]byte{byte(len(dme.FileName))},
		[]byte(dme.FileName),
	)
}

func readShortString(input []byte) (value string, remaining []byte, err error) {
	if len(input) == 0 {
		return "", nil, fmt.Errorf("input is empty")
	}
	length := int(input[0])
	if len(input) < 1+length {
		return "", nil, fmt.Errorf("input is too short for length %d", length)
	}
	value = string(input[1 : 1+length])
	remaining = input[1+length:]
	return
}
