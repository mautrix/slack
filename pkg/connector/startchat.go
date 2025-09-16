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
	"strings"

	"github.com/slack-go/slack"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-slack/pkg/slackid"
)

var (
	_ bridgev2.IdentifierResolvingNetworkAPI = (*SlackClient)(nil)
	_ bridgev2.UserSearchingNetworkAPI       = (*SlackClient)(nil)
	_ bridgev2.GroupCreatingNetworkAPI       = (*SlackClient)(nil)
	_ bridgev2.IdentifierValidatingNetwork   = (*SlackConnector)(nil)
)

func (s *SlackConnector) ValidateUserID(id networkid.UserID) bool {
	teamID, userID := slackid.ParseUserID(id)
	return teamID != "" && userID != ""
}

func (s *SlackClient) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	if s.Client == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	var userInfo *slack.User
	var err error
	if strings.ContainsRune(identifier, '@') {
		userInfo, err = s.Client.GetUserByEmailContext(ctx, identifier)
		// TODO return err try next for not found users?
	} else {
		if strings.ContainsRune(identifier, '-') {
			var teamID string
			teamID, identifier = slackid.ParseUserID(networkid.UserID(identifier))
			if teamID != s.TeamID {
				return nil, fmt.Errorf("%w: identifier does not match team", bridgev2.ErrResolveIdentifierTryNext)
			}
		} else {
			identifier = strings.ToUpper(identifier)
		}
		userInfo, err = s.Client.GetUserInfoContext(ctx, identifier)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}
	userID := slackid.MakeUserID(s.TeamID, userInfo.ID)
	ghost, err := s.Main.br.GetGhostByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get ghost: %w", err)
	}
	var chatResp *bridgev2.CreateChatResponse
	if createChat {
		resp, _, _, err := s.Client.OpenConversationContext(ctx, &slack.OpenConversationParameters{
			ReturnIM: true,
			Users:    []string{userInfo.ID},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to open conversation: %w", err)
		}
		chatInfo, err := s.wrapChatInfo(ctx, resp, true)
		if err != nil {
			return nil, fmt.Errorf("failed to wrap chat info: %w", err)
		}
		chatResp = &bridgev2.CreateChatResponse{
			PortalKey:  s.makePortalKey(resp),
			PortalInfo: chatInfo,
		}
	}
	return &bridgev2.ResolveIdentifierResponse{
		Ghost:  ghost,
		UserID: userID,
		Chat:   chatResp,
	}, nil
}

func (s *SlackClient) CreateGroup(ctx context.Context, params *bridgev2.GroupCreateParams) (*bridgev2.CreateChatResponse, error) {
	if s.Client == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	plainUsers := make([]string, len(params.Participants))
	for i, user := range params.Participants {
		var teamID string
		teamID, plainUsers[i] = slackid.ParseUserID(user)
		if teamID != s.TeamID || plainUsers[i] == "" {
			return nil, fmt.Errorf("invalid user ID %q", user)
		}
	}
	var resp *slack.Channel
	var err error
	switch params.Type {
	case "public-channel", "private-channel":
		if params.Name == nil {
			return nil, fmt.Errorf("missing name for channel")
		}
		resp, err = s.Client.CreateConversationContext(ctx, slack.CreateConversationParams{
			ChannelName: params.Name.Name,
			IsPrivate:   params.Type == "private-channel",
			TeamID:      s.TeamID,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create channel: %w", err)
		}
		resp, err = s.Client.InviteUsersToConversationContext(ctx, resp.ID, plainUsers...)
		if err != nil {
			return nil, fmt.Errorf("failed to invite users: %w", err)
		}
	case "group":
		resp, _, _, err = s.Client.OpenConversationContext(ctx, &slack.OpenConversationParameters{
			ReturnIM: true,
			Users:    plainUsers,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to open conversation: %w", err)
		}
	default:
		return nil, fmt.Errorf("unrecognized group type %q", params.Type)
	}
	if params.Topic != nil {
		resp, err = s.Client.SetTopicOfConversationContext(ctx, resp.ID, params.Topic.Topic)
		if err != nil {
			return nil, fmt.Errorf("failed to set topic: %w", err)
		}
	}
	chatInfo, err := s.wrapChatInfo(ctx, resp, true)
	if err != nil {
		return nil, fmt.Errorf("failed to wrap chat info: %w", err)
	}
	return &bridgev2.CreateChatResponse{
		PortalKey:  s.makePortalKey(resp),
		PortalInfo: chatInfo,
	}, nil
}

func (s *SlackClient) SearchUsers(ctx context.Context, query string) ([]*bridgev2.ResolveIdentifierResponse, error) {
	if s.Client == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	resp, err := s.Client.SearchUsersCacheContext(ctx, s.TeamID, query)
	if err != nil {
		return nil, err
	}
	results := make([]*bridgev2.ResolveIdentifierResponse, len(resp.Results))
	for i, user := range resp.Results {
		userID := slackid.MakeUserID(s.TeamID, user.ID)
		ghost, err := s.Main.br.GetGhostByID(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("failed to get ghost: %w", err)
		}
		results[i] = &bridgev2.ResolveIdentifierResponse{
			Ghost:    ghost,
			UserID:   userID,
			UserInfo: s.wrapUserInfo(user.ID, user, nil, ghost),
		}
	}
	return results, nil
}
