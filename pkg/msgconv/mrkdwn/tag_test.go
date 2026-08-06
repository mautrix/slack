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

package mrkdwn

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestUserGroupMention(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		resolvedName string
		expectedID   string
		expected     string
	}{
		{
			name:     "embedded label",
			input:    "Hi <!subteam^S123|@platform-team>",
			expected: "Hi @platform-team",
		},
		{
			name:         "resolved handle",
			input:        "Hi <!subteam^S123>",
			resolvedName: "platform-team",
			expectedID:   "S123",
			expected:     "Hi @platform-team",
		},
		{
			name:       "unresolved fallback",
			input:      "Hi <!subteam^S404>",
			expectedID: "S404",
			expected:   "Hi &lt;!subteam^S404&gt;",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestedID := ""
			parser := New(&Params{GetUserGroupInfo: func(_ context.Context, userGroupID string) string {
				requestedID = userGroupID
				return test.resolvedName
			}})
			output, err := parser.Parse(context.Background(), test.input, &event.Mentions{})
			if err != nil {
				t.Fatal(err)
			}
			if output != test.expected {
				t.Fatalf("unexpected output: %q", output)
			}
			if requestedID != test.expectedID {
				t.Fatalf("unexpected requested ID: got %q, want %q", requestedID, test.expectedID)
			}
		})
	}
}
