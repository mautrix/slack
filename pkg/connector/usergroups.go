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
	"time"

	"github.com/slack-go/slack"
)

const UserGroupInfoCacheExpiry = 1 * time.Hour
const UserGroupInfoRetryInterval = 1 * time.Minute
const UserGroupInfoRequestTimeout = 10 * time.Second

func (s *SlackClient) GetUserGroupInfoForMention(ctx context.Context, userGroupID string) (string, error) {
	s.userGroupInfoCacheLock.Lock()
	defer s.userGroupInfoCacheLock.Unlock()

	now := time.Now()
	handle, found := s.userGroupInfoCache.data[userGroupID]
	cacheExpired := now.Sub(s.userGroupInfoCache.updatedAt) >= UserGroupInfoCacheExpiry
	canRetry := now.Sub(s.userGroupInfoCache.lastAttempt) >= UserGroupInfoRetryInterval
	if (!found || cacheExpired) && canRetry {
		if err := ctx.Err(); err != nil {
			return handle, err
		}
		s.userGroupInfoCache.lastAttempt = now
		fetchCtx, cancel := context.WithTimeout(ctx, UserGroupInfoRequestTimeout)
		defer cancel()
		groups, err := s.Client.GetUserGroupsContext(
			fetchCtx,
			slack.GetUserGroupsOptionTeamID(s.TeamID),
			slack.GetUserGroupsOptionIncludeDisabled(true),
		)
		if err != nil {
			return handle, err
		}
		cache := make(map[string]string, len(groups))
		for _, group := range groups {
			cache[group.ID] = group.Handle
		}
		s.userGroupInfoCache.updatedAt = time.Now()
		s.userGroupInfoCache.data = cache
		handle = cache[userGroupID]
	}
	return handle, nil
}
