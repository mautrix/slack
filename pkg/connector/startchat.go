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
	"strings"

	"github.com/slack-go/slack"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-slack/pkg/slackid"
)

var (
	_ bridgev2.IdentifierResolvingNetworkAPI = (*SlackClient)(nil)
	_ bridgev2.UserSearchingNetworkAPI       = (*SlackClient)(nil)
	_ bridgev2.GroupCreatingNetworkAPI       = (*SlackClient)(nil)
	_ bridgev2.IdentifierValidatingNetwork   = (*SlackConnector)(nil)
	_ bridgev2.GhostDMCreatingNetworkAPI     = (*SlackClient)(nil)
)

func (s *SlackConnector) ValidateUserID(id networkid.UserID) bool {
	// Slack IDs are uppercase, but MXIDs are always lowercase, so require the input to be lowercase
	if strings.ToLower(string(id)) != string(id) {
		return false
	}
	teamID, userID := slackid.ParseUserID(id)
	return isValidSlackID(teamID) && isValidSlackID(userID)
}

func isValidSlackID(userID string) bool {
	if len(userID) == 0 {
		return false
	}
	for _, chr := range userID {
		if (chr < 'A' || chr > 'Z') && (chr < '0' || chr > '9') {
			return false
		}
	}
	return true
}

func (s *SlackClient) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	if s.Client == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	if strings.ContainsRune(identifier, '-') {
		var teamID string
		teamID, identifier = slackid.ParseUserID(networkid.UserID(identifier))
		if teamID != s.TeamID {
			return nil, fmt.Errorf("%w: identifier does not match team", bridgev2.ErrResolveIdentifierTryNext)
		}
	} else {
		identifier = strings.ToUpper(identifier)
	}
	if !isValidSlackID(identifier) {
		return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("Invalid Slack user ID"))
	}
	userInfo, err := s.Client.GetUserInfoContext(ctx, identifier)
	if err != nil {
		return nil, wrapSlackRespError(fmt.Errorf("failed to get user info: %w", err))
	}
	userID := slackid.MakeUserID(s.TeamID, userInfo.ID)
	ghost, err := s.Main.br.GetGhostByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get ghost: %w", err)
	}
	var chatResp *bridgev2.CreateChatResponse
	if createChat {
		chatResp, err = s.CreateChatWithGhost(ctx, ghost)
		if err != nil {
			return nil, err
		}
	}
	return &bridgev2.ResolveIdentifierResponse{
		Ghost:  ghost,
		UserID: userID,
		Chat:   chatResp,
	}, nil
}

func (s *SlackClient) CreateChatWithGhost(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.CreateChatResponse, error) {
	teamID, identifier := slackid.ParseUserID(ghost.ID)
	if teamID != s.TeamID {
		return nil, fmt.Errorf("%w: identifier does not match team", bridgev2.ErrResolveIdentifierTryNext)
	}
	resp, _, _, err := s.Client.OpenConversationContext(ctx, &slack.OpenConversationParameters{
		ReturnIM: true,
		Users:    []string{identifier},
	})
	if err != nil {
		return nil, wrapSlackRespError(fmt.Errorf("failed to open conversation: %w", err))
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

func (s *SlackClient) CreateGroup(ctx context.Context, params *bridgev2.GroupCreateParams) (*bridgev2.CreateChatResponse, error) {
	if s.Main.Config.DMOnly && (params.Type == "public-channel" || params.Type == "private-channel") {
		return nil, bridgev2.RespError(mautrix.MForbidden.WithMessage("Creating Slack channels is disabled in DM-only mode"))
	}
	if s.Client == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	plainUsers := make([]string, len(params.Participants))
	for i, user := range params.Participants {
		var teamID string
		teamID, plainUsers[i] = slackid.ParseUserID(user)
		if teamID != s.TeamID || !isValidSlackID(plainUsers[i]) {
			return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("Invalid user ID %q", user))
		}
	}
	var resp *slack.Channel
	var err error
	switch params.Type {
	case "public-channel", "private-channel":
		if params.Name == nil {
			return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("Channel name is required"))
		}
		resp, err = s.Client.CreateConversationContext(ctx, slack.CreateConversationParams{
			ChannelName: params.Name.Name,
			IsPrivate:   params.Type == "private-channel",
			TeamID:      s.TeamID,
		})
		if err != nil {
			return nil, wrapSlackRespError(fmt.Errorf("failed to create channel: %w", err))
		}
		if len(plainUsers) > 0 {
			resp, err = s.Client.InviteUsersToConversationContext(ctx, resp.ID, plainUsers...)
			if err != nil {
				return nil, wrapSlackRespError(fmt.Errorf("failed to invite users: %w", err))
			}
		}
	case "group":
		resp, _, _, err = s.Client.OpenConversationContext(ctx, &slack.OpenConversationParameters{
			ReturnIM: true,
			Users:    plainUsers,
		})
		if err != nil {
			return nil, wrapSlackRespError(fmt.Errorf("failed to open conversation: %w", err))
		}
		s.addDMChannel(resp)
	default:
		return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("Unrecognized group type %q", params.Type))
	}
	if params.Topic != nil {
		resp, err = s.Client.SetTopicOfConversationContext(ctx, resp.ID, params.Topic.Topic)
		if err != nil {
			return nil, wrapSlackRespError(fmt.Errorf("failed to set topic: %w", err))
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
		return nil, wrapSlackRespError(fmt.Errorf("failed to search users: %w", err))
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

// wrapSlackRespError translates a Slack API error into an HTTP error for the provisioning API,
// keeping the original error in the chain. Clients retry 429 and 502-504 automatically, which isn't
// wanted for provisioning requests, so those statuses aren't used.
func wrapSlackRespError(err error) error {
	var mapped bridgev2.RespError
	switch slackAPIErrorCode(err) {
	case "ratelimited", "rate_limited":
		mapped = bridgev2.RespError(mautrix.MLimitExceeded.WithMessage("Slack is rate limiting requests. Please wait a moment and try again.").WithStatus(http.StatusBadRequest))
	case "invalid_auth", "not_authed", "token_revoked", "token_expired", "account_inactive",
		"no_permission", "not_authorized", "access_denied", "missing_scope", "not_allowed_token_type",
		"restricted_action", "team_access_not_granted", "user_is_restricted", "ekm_access_denied",
		"cant_invite", "cant_invite_self":
		mapped = bridgev2.RespError(mautrix.MForbidden.WithMessage("Slack denied the request"))
	case "user_not_found", "users_not_found":
		mapped = bridgev2.RespError(mautrix.MNotFound.WithMessage("User not found on Slack"))
	case "channel_not_found":
		mapped = bridgev2.RespError(mautrix.MNotFound.WithMessage("Channel not found on Slack"))
	case "name_taken":
		mapped = bridgev2.RespError(mautrix.MInvalidParam.WithMessage("A channel with that name already exists"))
	case "invalid_name", "invalid_name_specials", "invalid_name_maxlength", "invalid_name_punctuation", "invalid_name_required":
		mapped = bridgev2.RespError(mautrix.MInvalidParam.WithMessage("Invalid channel name"))
	default:
		mapped = bridgev2.RespError(mautrix.MUnknown.WithMessage("Request to Slack failed"))
	}
	return fmt.Errorf("%w: %w", mapped, err)
}
