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
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	"go.mau.fi/mautrix-slack/pkg/slackid"
)

const LoginFlowIDAuthToken = "token"
const LoginStepIDAuthToken = "fi.mau.slack.login.enter_auth_token"
const LoginStepIDComplete = "fi.mau.slack.login.complete"

func (s *SlackConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{{
		Name:        "Email",
		Description: "Sign in with your Slack email address",
		ID:          LoginFlowIDEmail,
	}, {
		Name:        "Auth token & cookie",
		Description: "Advanced: sign in with an existing auth token and cookie token",
		ID:          LoginFlowIDAuthToken,
	}, {
		Name:        "Slack app",
		Description: "Log in with a Slack app",
		ID:          LoginFlowIDApp,
	}}
}

func (s *SlackConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	switch flowID {
	case LoginFlowIDEmail:
		return &SlackEmailLogin{
			User: user,
			API:  newSlackLoginAPI(),
		}, nil
	case LoginFlowIDAuthToken:
		return &SlackTokenLogin{
			User: user,
		}, nil
	case LoginFlowIDApp:
		return &SlackAppLogin{
			User: user,
		}, nil
	default:
		return nil, fmt.Errorf("unknown login flow %s", flowID)
	}
}

type SlackTokenLogin struct {
	User *bridgev2.User
}

var _ bridgev2.LoginProcessUserInput = (*SlackTokenLogin)(nil)

func (s *SlackTokenLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       LoginStepIDAuthToken,
		Instructions: "Enter an existing Slack auth token and cookie token. This advanced flow does not open a browser.",
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type:        bridgev2.LoginInputFieldTypeToken,
				ID:          "auth_token",
				Name:        "Auth token",
				Description: "Slack auth token (starts with xoxc-)",
				Pattern:     `^xoxc-.+$`,
			}, {
				Type:        bridgev2.LoginInputFieldTypeToken,
				ID:          "cookie_token",
				Name:        "Cookie token",
				Description: "Slack cookie token (starts with xoxd-)",
				Pattern:     `^xoxd-[a-zA-Z0-9/+=]+$`,
			}},
		},
	}, nil
}

func (s *SlackTokenLogin) Cancel() {}

func (s *SlackTokenLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	token, cookieToken := input["auth_token"], input["cookie_token"]
	if token == "" || cookieToken == "" {
		return s.Start(ctx)
	}
	return completeSlackTokenLogin(ctx, s.User, token, cookieToken)
}

func completeSlackTokenLogin(ctx context.Context, user *bridgev2.User, token, cookieToken string) (*bridgev2.LoginStep, error) {
	client := makeSlackClient(&user.Log, token, cookieToken, "")
	err := client.FetchVersionData(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to fetch version data")
		return nil, err
	}
	info, err := client.ClientUserBootContext(ctx, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("client.boot failed: %w", err)
	}
	ul, err := user.NewLogin(ctx, &database.UserLogin{
		ID:         slackid.MakeUserLoginID(info.Team.ID, info.Self.ID),
		RemoteName: fmt.Sprintf("%s - %s", info.Team.Name, info.Self.Profile.Email),
		Metadata: &slackid.UserLoginMetadata{
			Email:       info.Self.Profile.Email,
			Token:       token,
			CookieToken: cookieToken,
		},
	}, &bridgev2.NewLoginParams{
		DeleteOnConflict:  true,
		DontReuseExisting: false,
	})
	if err != nil {
		return nil, err
	}
	sc := ul.Client.(*SlackClient)
	err = sc.connect(ul.Log.WithContext(context.Background()), info)
	if err != nil {
		return nil, fmt.Errorf("failed to connect after login: %w", err)
	}
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       LoginStepIDComplete,
		Instructions: fmt.Sprintf("Successfully logged into %s as %s", info.Team.Name, info.Self.Profile.Email),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}
