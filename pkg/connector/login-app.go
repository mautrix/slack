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

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	"go.mau.fi/mautrix-slack/pkg/slackid"
)

const LoginFlowIDApp = "app"
const LoginStepIDAppToken = "fi.mau.slack.login.enter_app_tokens"

type SlackAppLogin struct {
	User *bridgev2.User
}

var _ bridgev2.LoginProcessUserInput = (*SlackAppLogin)(nil)

func (s *SlackAppLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return &bridgev2.LoginStep{
		Type:   bridgev2.LoginStepTypeUserInput,
		StepID: LoginStepIDAppToken,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type:        bridgev2.LoginInputFieldTypeToken,
				ID:          "bot_token",
				Name:        "Bot token",
				Description: "Slack bot token for the workspace (starts with `xoxb-`)",
				Pattern:     "^xoxb-.+$",
			}, {
				Type:        bridgev2.LoginInputFieldTypeToken,
				ID:          "app_token",
				Name:        "App token",
				Description: "Slack app-level token (starts with `xapp-`)",
				Pattern:     "^xapp-.+$",
			}},
		},
	}, nil
}

func (s *SlackAppLogin) Cancel() {}

func (s *SlackAppLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	token, appToken := input["bot_token"], input["app_token"]
	client := makeSlackClient(&s.User.Log, token, "", appToken)
	info, err := client.AuthTestContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth.test failed: %w", err)
	}
	ul, err := s.User.NewLogin(ctx, &database.UserLogin{
		ID:         slackid.MakeUserLoginID(info.TeamID, info.UserID),
		RemoteName: fmt.Sprintf("%s - %s", info.Team, info.User),
		Metadata: &slackid.UserLoginMetadata{
			Token:    token,
			AppToken: appToken,
		},
	}, &bridgev2.NewLoginParams{
		DeleteOnConflict:  true,
		DontReuseExisting: false,
	})
	if err != nil {
		return nil, err
	}
	sc := ul.Client.(*SlackClient)
	err = sc.Connect(ul.Log.WithContext(context.Background()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect after login: %w", err)
	}
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       LoginStepIDComplete,
		Instructions: fmt.Sprintf("Successfully logged into %s as %s", info.Team, info.User),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}
