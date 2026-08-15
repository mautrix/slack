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
	"net/url"
	"strings"

	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/mediaproxy"
)

const (
	slackEmojiMediaIDPrefix = "emoji:"
	slackEmojiMediaHost     = "emoji.slack-edge.com"
)

func makeSlackEmojiMediaID(emojiURL string) networkid.MediaID {
	return networkid.MediaID(slackEmojiMediaIDPrefix + emojiURL)
}

func parseSlackEmojiMediaID(mediaID networkid.MediaID) (*url.URL, error) {
	rawURL, ok := strings.CutPrefix(string(mediaID), slackEmojiMediaIDPrefix)
	if !ok {
		return nil, fmt.Errorf("unknown Slack media ID type")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Slack emoji URL: %w", err)
	}
	if parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), slackEmojiMediaHost) || parsed.User != nil {
		return nil, fmt.Errorf("invalid Slack emoji URL host")
	}
	return parsed, nil
}

func (s *SlackConnector) Download(_ context.Context, mediaID networkid.MediaID, _ map[string]string) (mediaproxy.GetMediaResponse, error) {
	emojiURL, err := parseSlackEmojiMediaID(mediaID)
	if err != nil {
		return nil, err
	}
	return &mediaproxy.GetMediaResponseURL{
		URL: emojiURL.String(),
	}, nil
}
