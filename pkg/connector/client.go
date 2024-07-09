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

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"

	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-slack/pkg/msgconv"
	"go.mau.fi/mautrix-slack/pkg/slackid"
)

func init() {
	status.BridgeStateHumanErrors.Update(status.BridgeStateErrorMap{
		"slack-invalid-auth":           "Invalid credentials, please log in again",
		"slack-user-removed-from-team": "You were removed from the Slack workspace",
		"slack-id-mismatch":            "Unexpected internal error: got different user ID",
	})
}

func makeSlackClient(log *zerolog.Logger, token, cookieToken string) *slack.Client {
	options := []slack.Option{
		slack.OptionLog(slackgoZerolog{Logger: log.With().Str("component", "slackgo").Logger()}),
		slack.OptionDebug(log.GetLevel() == zerolog.TraceLevel),
	}
	if cookieToken != "" {
		options = append(options, slack.OptionCookie("d", cookieToken))
	}
	return slack.New(token, options...)
}

func (s *SlackConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	teamID, userID := slackid.ParseUserLoginID(login.ID)
	token, ok := login.Metadata.Extra["token"].(string)
	if !ok {
		login.Client = &SlackClient{Main: s, UserLogin: login, UserID: userID, TeamID: teamID}
		return nil
	}
	cookieToken, _ := login.Metadata.Extra["cookie_token"].(string)
	client := makeSlackClient(&login.Log, token, cookieToken)
	login.Client = &SlackClient{
		Main:      s,
		UserLogin: login,
		Client:    client,
		RTM:       client.NewRTM(),
		UserID:    userID,
		TeamID:    teamID,
	}
	return nil
}

type SlackClient struct {
	Main      *SlackConnector
	UserLogin *bridgev2.UserLogin
	Client    *slack.Client
	RTM       *slack.RTM
	UserID    string
	TeamID    string
	BootResp  *slack.ClientBootResponse
}

var _ bridgev2.NetworkAPI = (*SlackClient)(nil)

var _ msgconv.SlackClientProvider = (*SlackClient)(nil)

func (s *SlackClient) GetClient() *slack.Client {
	return s.Client
}

func (s *SlackClient) Connect(ctx context.Context) error {
	var err error
	s.BootResp, err = s.Client.ClientBootContext(ctx)
	if err != nil {
		if err.Error() == "user_removed_from_team" || err.Error() == "invalid_auth" {
			s.invalidateSession(ctx, status.BridgeState{
				StateEvent: status.StateBadCredentials,
				Error:      status.BridgeStateErrorCode(fmt.Sprintf("slack-%s", strings.ReplaceAll(err.Error(), "_", "-"))),
			})
		} else {
			s.UserLogin.BridgeState.Send(status.BridgeState{
				StateEvent: status.StateUnknownError,
				Error:      "slack-unknown-fetch-error",
				Message:    fmt.Sprintf("Unknown error from Slack: %s", err.Error()),
			})
		}
		return err
	}
	go s.RTM.ManageConnection()
	return nil
}

func (s *SlackClient) consumeEvents(ctx context.Context) {
	for evt := range s.RTM.IncomingEvents {
		s.HandleSlackEvent(evt.Data)
	}
}

func (s *SlackClient) Disconnect() {
	if rtm := s.RTM; rtm != nil {
		err := rtm.Disconnect()
		if err != nil {
			s.UserLogin.Log.Err(err).Msg("Failed to disconnect RTM")
		}
		s.RTM = nil
	}
	s.Client = nil
}

func (s *SlackClient) IsLoggedIn() bool {
	return s.Client != nil
}

func (s *SlackClient) LogoutRemote(ctx context.Context) {
	_, err := s.Client.SendAuthSignoutContext(ctx)
	if err != nil {
		s.UserLogin.Log.Err(err).Msg("Failed to send sign out request to Slack")
	}
}

func (s *SlackClient) invalidateSession(ctx context.Context, state status.BridgeState) {
	s.UserLogin.Metadata.Extra["token"] = ""
	s.UserLogin.Metadata.Extra["cookie_token"] = ""
	err := s.UserLogin.Save(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to save user login after invalidating session")
	}
	s.Disconnect()
	s.UserLogin.BridgeState.Send(state)
}

func (s *SlackClient) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	return slackid.UserIDToUserLoginID(userID) == s.UserLogin.ID
}
