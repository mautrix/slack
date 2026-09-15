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

package slackid

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDirectMediaFileRoundTrip(t *testing.T) {
	original := &DirectMediaFile{
		UserLoginID: MakeUserLoginID("T01234567", "U89ABCDEF"),
		FileID:      "F0123456789",
	}
	mediaID := original.MediaID()
	require.NotNil(t, mediaID)

	parsed, err := ParseMediaID(mediaID)
	require.NoError(t, err)
	assert.Equal(t, original, parsed)
}

func TestDirectMediaEmojiRoundTrip(t *testing.T) {
	original := DirectMediaEmojiFromURL("https://emoji.slack-edge.com/T01234567/party-parrot/0123456789abcdef.gif")
	require.NotNil(t, original)
	mediaID := original.MediaID()
	require.NotNil(t, mediaID)

	parsed, err := ParseMediaID(mediaID)
	require.NoError(t, err)
	assert.Equal(t, original, parsed)
}

func TestParseMediaIDRejectsInvalid(t *testing.T) {
	_, err := ParseMediaID(nil)
	assert.Error(t, err)

	_, err = ParseMediaID([]byte{0xFF})
	assert.Error(t, err)

	mediaID := (&DirectMediaFile{UserLoginID: "T-U", FileID: "F1"}).MediaID()
	_, err = ParseMediaID(mediaID[:len(mediaID)-1])
	assert.Error(t, err)
}
