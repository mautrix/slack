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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/slack-go/slack"
)

func newUserGroupTestClient(t *testing.T, handler http.HandlerFunc) *SlackClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &SlackClient{
		Client: slack.New("testing-token", slack.OptionAPIURL(server.URL+"/")),
		TeamID: "T123",
	}
}

func TestGetUserGroupInfoForMentionCachesAndRefreshes(t *testing.T) {
	requests := 0
	handle := "platform-team"
	client := newUserGroupTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/usergroups.list" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.FormValue("team_id") != "T123" || r.FormValue("include_disabled") != "true" {
			t.Errorf("unexpected request parameters: %s", r.Form.Encode())
		}
		_, _ = fmt.Fprintf(w, `{"ok":true,"usergroups":[{"id":"S123","handle":%q}]}`, handle)
	})

	for range 2 {
		name, err := client.GetUserGroupInfoForMention(context.Background(), "S123")
		if err != nil || name != "platform-team" {
			t.Fatalf("unexpected cached result: name=%q, err=%v", name, err)
		}
	}
	if requests != 1 {
		t.Fatalf("expected 1 request, got %d", requests)
	}

	client.userGroupInfoCache.updatedAt = time.Now().Add(-UserGroupInfoCacheExpiry)
	client.userGroupInfoCache.lastAttempt = time.Now().Add(-UserGroupInfoRetryInterval)
	handle = "renamed-team"
	name, err := client.GetUserGroupInfoForMention(context.Background(), "S123")
	if err != nil || name != "renamed-team" {
		t.Fatalf("unexpected refreshed result: name=%q, err=%v", name, err)
	}
	if requests != 2 {
		t.Fatalf("expected 2 requests, got %d", requests)
	}
}

func TestGetUserGroupInfoForMentionRetainsStaleDataAndBacksOff(t *testing.T) {
	requests := 0
	fail := false
	client := newUserGroupTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if fail {
			_, _ = fmt.Fprint(w, `{"ok":false,"error":"request_timeout"}`)
		} else {
			_, _ = fmt.Fprint(w, `{"ok":true,"usergroups":[{"id":"S123","handle":"platform-team"}]}`)
		}
	})

	name, err := client.GetUserGroupInfoForMention(context.Background(), "S123")
	if err != nil || name != "platform-team" {
		t.Fatalf("unexpected initial result: name=%q, err=%v", name, err)
	}

	client.userGroupInfoCache.updatedAt = time.Now().Add(-UserGroupInfoCacheExpiry)
	client.userGroupInfoCache.lastAttempt = time.Now().Add(-UserGroupInfoRetryInterval)
	fail = true
	name, err = client.GetUserGroupInfoForMention(context.Background(), "S123")
	if err == nil || name != "platform-team" {
		t.Fatalf("expected stale handle with refresh error, got name=%q, err=%v", name, err)
	}
	name, err = client.GetUserGroupInfoForMention(context.Background(), "S123")
	if err != nil || name != "platform-team" {
		t.Fatalf("expected stale handle during backoff, got name=%q, err=%v", name, err)
	}
	if requests != 2 {
		t.Fatalf("expected 2 requests, got %d", requests)
	}
}

func TestGetUserGroupInfoForMentionDoesNotBackOffCanceledContext(t *testing.T) {
	client := newUserGroupTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":true,"usergroups":[{"id":"S123","handle":"platform-team"}]}`)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.GetUserGroupInfoForMention(ctx, "S123")
	if err == nil {
		t.Fatal("expected canceled request to fail")
	}
	if !client.userGroupInfoCache.lastAttempt.IsZero() {
		t.Fatal("canceled context unexpectedly started backoff")
	}

	name, err := client.GetUserGroupInfoForMention(context.Background(), "S123")
	if err != nil || name != "platform-team" {
		t.Fatalf("unexpected retry result: name=%q, err=%v", name, err)
	}
}
