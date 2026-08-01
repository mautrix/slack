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
	"errors"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
)

type slackLoginTwoFactor struct {
	Workspace slackLoginWorkspace
}

func (s *slackLoginAPIClient) StartTwoFactor(
	ctx context.Context,
	workspace slackLoginWorkspace,
) (*slackLoginTwoFactor, error) {
	if workspace.MagicLoginCode == "" {
		return nil, errors.New("slack workspace did not provide a two-factor magic login code")
	}

	_, err := s.nativeMagicLogin(ctx, workspace.MagicLoginCode, "")
	if err != nil && !slackMagicLoginNeedsTwoFactorCode(err) {
		return nil, fmt.Errorf("failed to initialize Slack two-factor authentication: %w", err)
	}
	return &slackLoginTwoFactor{Workspace: workspace}, nil
}

func (s *slackLoginAPIClient) SubmitTwoFactor(
	ctx context.Context,
	challenge *slackLoginTwoFactor,
	code string,
) (string, string, error) {
	if challenge == nil {
		return "", "", errors.New("slack two-factor challenge is not initialized")
	}
	if challenge.Workspace.MagicLoginCode == "" {
		return "", "", &slackLoginAPIError{
			Method: "auth.loginMagic",
			Code:   "two_factor_state_expired",
		}
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return "", "", errors.New("slack two-factor code is blank")
	}

	token, err := s.nativeMagicLogin(ctx, challenge.Workspace.MagicLoginCode, code)
	if err != nil {
		return "", "", err
	}
	if token == "" {
		return "", "", errors.New("slack auth.loginMagic response did not contain a token")
	}
	return token, s.findCookieToken(s.apiBaseURL), nil
}

func (s *slackLoginAPIClient) nativeMagicLogin(
	ctx context.Context,
	magicLoginCode, twoFactorCode string,
) (string, error) {
	values := map[string]string{
		"magic_token":                 magicLoginCode,
		"two_factor_native_supported": "1",
		"two_factor_is_backup":        "0",
	}
	if twoFactorCode != "" {
		values["two_factor_pin"] = twoFactorCode
	}
	var response struct {
		slackLoginAPIResponse
		Token string `json:"token"`
	}
	err := s.postAPI(ctx, "auth.loginMagic", "", values, &response)
	return response.Token, err
}

func slackMagicLoginNeedsTwoFactorCode(err error) bool {
	var apiErr *slackLoginAPIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case "two_factor_required",
		"missing_pin",
		"missing_pin_app",
		"missing_pin_app_sms",
		"missing_pin_sms",
		"missing_pin_sms_app",
		"missing_pin_sms_sms":
		return true
	default:
		return false
	}
}

func slackTwoFactorStep(instructions string) *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       LoginStepIDTwoFactor,
		Instructions: instructions,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type:        bridgev2.LoginInputFieldType2FACode,
				ID:          loginFieldTwoFactorCode,
				Name:        "Authentication code",
				Description: "Six-digit code from your Slack authenticator or SMS",
				Pattern:     slackLoginTwoFactorCodeRegex,
			}},
		},
	}
}
