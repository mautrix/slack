// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2026 Killian Lelong
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
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"slices"
	"strings"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
)

const (
	LoginFlowIDEmail = "email"

	LoginStepIDEmail        = "fi.mau.slack.login.enter_email"
	LoginStepIDEmailCaptcha = "fi.mau.slack.login.email_captcha"
	LoginStepIDEmailCode    = "fi.mau.slack.login.enter_email_code"
	LoginStepIDWorkspace    = "fi.mau.slack.login.select_workspace"
	LoginStepIDTwoFactor    = "fi.mau.slack.login.two_factor"

	loginFieldEmail         = "email"
	loginFieldCaptchaToken  = "captcha_token"
	loginFieldEmailCode     = "code"
	loginFieldWorkspace     = "workspace"
	loginFieldTwoFactorCode = "two_factor_code"

	slackLoginCaptchaPageURL     = "https://slack.com/signin"
	slackLoginTwoFactorCodeRegex = `^\d{6}$`
)

var (
	slackEmailCodePattern = regexp.MustCompile(`^[a-zA-Z0-9]{3}-?[a-zA-Z0-9]{3}$`)
	slackTwoFactorPattern = regexp.MustCompile(slackLoginTwoFactorCodeRegex)
)

type slackEmailLoginAPI interface {
	RequestCode(ctx context.Context, email string) (*slackLoginCaptcha, error)
	SubmitCaptcha(ctx context.Context, email, captchaResponse string) error
	ConfirmCode(ctx context.Context, email, code string) ([]slackLoginWorkspace, error)
	LoginWorkspace(ctx context.Context, workspace slackLoginWorkspace) (token, cookieToken string, err error)
	StartTwoFactor(ctx context.Context, workspace slackLoginWorkspace) (*slackLoginTwoFactor, error)
	SubmitTwoFactor(ctx context.Context, challenge *slackLoginTwoFactor, code string) (token, cookieToken string, err error)
}

type slackLoginCompleter func(ctx context.Context, user *bridgev2.User, token, cookieToken string) (*bridgev2.LoginStep, error)

type SlackEmailLogin struct {
	User       *bridgev2.User
	API        slackEmailLoginAPI
	complete   slackLoginCompleter
	email      string
	captcha    *slackLoginCaptcha
	twoFactor  *slackLoginTwoFactor
	workspaces map[string]slackLoginWorkspace
}

var _ bridgev2.LoginProcessUserInput = (*SlackEmailLogin)(nil)
var _ bridgev2.LoginProcessCookies = (*SlackEmailLogin)(nil)

func (s *SlackEmailLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return slackEmailStep("Enter the email address associated with your Slack account."), nil
}

func (s *SlackEmailLogin) Cancel() {}

func (s *SlackEmailLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	switch {
	case s.twoFactor != nil:
		return s.submitTwoFactor(ctx, input)
	case s.workspaces != nil:
		return s.submitWorkspace(ctx, input)
	case s.captcha != nil:
		return slackCaptchaStep(s.captcha, "Complete the embedded CAPTCHA before continuing.")
	case s.email != "":
		return s.submitCode(ctx, input)
	default:
		return s.submitEmail(ctx, input)
	}
}

func (s *SlackEmailLogin) submitEmail(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	email := strings.TrimSpace(input[loginFieldEmail])
	if !validSlackLoginEmail(email) {
		return slackEmailStep("Enter a valid Slack account email address."), nil
	}
	if s.API == nil {
		return nil, errors.New("slack email login client is not initialized")
	}
	captcha, err := s.API.RequestCode(ctx, email)
	if err != nil {
		var apiErr *slackLoginAPIError
		if errors.As(err, &apiErr) {
			return slackEmailStep(slackLoginErrorInstructions(apiErr)), nil
		}
		return nil, fmt.Errorf("failed to request Slack email code: %w", err)
	}
	s.email = email
	if captcha != nil {
		s.captcha = captcha
		return slackCaptchaStep(captcha, "Slack requires a CAPTCHA before it can email the confirmation code. Complete the embedded challenge to continue.")
	}
	return slackEmailCodeStep("Slack emailed you a confirmation code. Enter the six characters from that email."), nil
}

func (s *SlackEmailLogin) SubmitCookies(ctx context.Context, cookies map[string]string) (*bridgev2.LoginStep, error) {
	if s.twoFactor != nil {
		return nil, errors.New("slack two-factor login does not accept browser cookies")
	}
	if s.captcha == nil || s.email == "" {
		return nil, errors.New("slack email CAPTCHA is not pending")
	}
	captchaResponse := strings.TrimSpace(cookies[loginFieldCaptchaToken])
	if captchaResponse == "" {
		return slackCaptchaStep(s.captcha, "The CAPTCHA did not return a solution. Complete a new embedded challenge to continue.")
	}
	if s.API == nil {
		return nil, errors.New("slack email login client is not initialized")
	}
	if err := s.API.SubmitCaptcha(ctx, s.email, captchaResponse); err != nil {
		var apiErr *slackLoginAPIError
		if errors.As(err, &apiErr) {
			switch apiErr.Code {
			case "captcha_required", "captcha_failed", "invalid_captcha":
				return slackCaptchaStep(s.captcha, "Slack rejected or expired that CAPTCHA solution. Complete a new embedded challenge to continue.")
			case "ratelimited", "rate_limited", "code_request_limit_reached":
				return slackCaptchaStep(s.captcha, "Slack is rate limiting email sign-in. Wait a few minutes, then complete a new embedded challenge.")
			case "invalid_email", "email_invalid":
				s.email = ""
				s.captcha = nil
				return slackEmailStep("Slack rejected that email address. Check it and try again."), nil
			}
		}
		return nil, fmt.Errorf("failed to submit Slack email CAPTCHA: %w", err)
	}
	s.captcha = nil
	return slackEmailCodeStep("Slack emailed you a confirmation code. Enter the six characters from that email."), nil
}

func (s *SlackEmailLogin) submitCode(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	code := normalizeSlackEmailCode(input[loginFieldEmailCode])
	if !slackEmailCodePattern.MatchString(code) {
		return slackEmailCodeStep("Enter the six-character code Slack emailed you."), nil
	}
	workspaces, err := s.API.ConfirmCode(ctx, s.email, strings.ReplaceAll(code, "-", ""))
	if err != nil {
		var apiErr *slackLoginAPIError
		if errors.As(err, &apiErr) {
			switch apiErr.Code {
			case "invalid_code", "invalid_email_code", "failed":
				return slackEmailCodeStep("Slack rejected that confirmation code. Check the email and try again."), nil
			case "expired_code", "code_expired":
				s.email = ""
				s.captcha = nil
				return slackEmailStep("That Slack confirmation code expired. Enter your email to request a new one."), nil
			case "ratelimited", "rate_limited":
				return slackEmailCodeStep("Slack is rate limiting code checks. Wait before trying again."), nil
			}
		}
		return nil, fmt.Errorf("failed to confirm Slack email code: %w", err)
	}
	if len(workspaces) == 0 {
		s.email = ""
		return slackEmailStep("Slack did not return any workspaces that support email sign-in for this account."), nil
	}
	s.workspaces = make(map[string]slackLoginWorkspace, len(workspaces))
	options := make([]string, 0, len(workspaces))
	for _, workspace := range workspaces {
		label := workspace.loginLabel()
		for suffix := 2; s.workspaces[label].ID != ""; suffix++ {
			label = fmt.Sprintf("%s (%d)", workspace.loginLabel(), suffix)
		}
		s.workspaces[label] = workspace
		options = append(options, label)
	}
	if len(workspaces) == 1 {
		return s.finishWorkspace(ctx, workspaces[0])
	}
	slices.Sort(options)
	return slackWorkspaceStep(options, "Choose the Slack workspace to connect."), nil
}

func (s *SlackEmailLogin) submitWorkspace(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	selection := strings.TrimSpace(input[loginFieldWorkspace])
	workspace, ok := s.workspaces[selection]
	if !ok {
		return slackWorkspaceStep(slackWorkspaceOptions(s.workspaces), "Choose one of the Slack workspaces in the list."), nil
	}
	return s.finishWorkspace(ctx, workspace)
}

func (s *SlackEmailLogin) finishWorkspace(ctx context.Context, workspace slackLoginWorkspace) (*bridgev2.LoginStep, error) {
	if workspace.TwoFactorRequired {
		return s.startTwoFactor(ctx, workspace, "Enter the authentication code required by this Slack workspace.")
	}
	token, cookieToken, err := s.API.LoginWorkspace(ctx, workspace)
	if err != nil {
		return slackWorkspaceStep(slackWorkspaceOptions(s.workspaces), fmt.Sprintf("Could not sign in to %s: %s", workspace.displayName(), safeSlackLoginError(err))), nil
	}
	return s.completeWorkspace(ctx, workspace, token, cookieToken)
}

func (s *SlackEmailLogin) startTwoFactor(
	ctx context.Context,
	workspace slackLoginWorkspace,
	instructions string,
) (*bridgev2.LoginStep, error) {
	challenge, err := s.API.StartTwoFactor(ctx, workspace)
	if err != nil {
		logEvent := zerolog.Ctx(ctx).Warn().
			Str("workspace_id", workspace.ID).
			Bool("has_magic_login_url", workspace.MagicLoginURL != "").
			Bool("has_magic_login_code", workspace.MagicLoginCode != "")
		var apiErr *slackLoginAPIError
		if errors.As(err, &apiErr) {
			logEvent = logEvent.
				Str("api_method", apiErr.Method).
				Str("api_error", apiErr.Code).
				Int("http_status", apiErr.HTTPStatus)
		}
		logEvent.Msg("Failed to start Slack two-factor authentication")
		return slackWorkspaceStep(
			slackWorkspaceOptions(s.workspaces),
			fmt.Sprintf("Could not start two-factor authentication for %s: %s", workspace.displayName(), safeSlackLoginError(err)),
		), nil
	}
	if challenge == nil {
		return nil, errors.New("slack two-factor login did not return a challenge")
	}
	challenge.Workspace = workspace
	s.twoFactor = challenge
	return slackTwoFactorStep(instructions), nil
}

func (s *SlackEmailLogin) submitTwoFactor(
	ctx context.Context,
	input map[string]string,
) (*bridgev2.LoginStep, error) {
	code := strings.TrimSpace(input[loginFieldTwoFactorCode])
	if !slackTwoFactorPattern.MatchString(code) {
		return slackTwoFactorStep("Enter the six-digit code from your authenticator app or SMS."), nil
	}
	if s.API == nil {
		return nil, errors.New("slack email login client is not initialized")
	}
	challenge := s.twoFactor
	token, cookieToken, err := s.API.SubmitTwoFactor(ctx, challenge, code)
	if err != nil {
		var apiErr *slackLoginAPIError
		if errors.As(err, &apiErr) {
			switch apiErr.Code {
			case "ratelimited", "rate_limited":
				return slackTwoFactorStep("Slack is rate limiting authentication-code checks. Wait before trying again."), nil
			case "two_factor_expired", "two_factor_state_expired":
				s.twoFactor = nil
				return s.startTwoFactor(
					ctx,
					challenge.Workspace,
					"That two-factor session expired. Enter a new authentication code.",
				)
			case "invalid_2fa_code", "invalid_two_factor_code", "invalid_pin":
				return slackTwoFactorStep("Slack rejected that authentication code. Check the code and try again."), nil
			}
		}
		return slackTwoFactorStep("Slack did not complete two-factor authentication. Check the code and try again."), nil
	}
	s.twoFactor = nil
	return s.completeWorkspace(ctx, challenge.Workspace, token, cookieToken)
}

func (s *SlackEmailLogin) completeWorkspace(
	ctx context.Context,
	workspace slackLoginWorkspace,
	token, cookieToken string,
) (*bridgev2.LoginStep, error) {
	complete := s.complete
	if complete == nil {
		complete = completeSlackEmailLogin
	}
	step, err := complete(ctx, s.User, token, cookieToken)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("workspace_id", workspace.ID).Msg("Failed to validate Slack email login session")
		s.email = ""
		s.captcha = nil
		s.twoFactor = nil
		s.workspaces = nil
		return slackEmailStep("Slack created a session, but the bridge could not validate it. Enter your email to try again."), nil
	}
	return step, nil
}

func completeSlackEmailLogin(
	ctx context.Context,
	user *bridgev2.User,
	token, cookieToken string,
) (*bridgev2.LoginStep, error) {
	return (&SlackTokenLogin{User: user}).SubmitCookies(ctx, map[string]string{
		"auth_token":   token,
		"cookie_token": cookieToken,
	})
}

func slackEmailStep(instructions string) *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       LoginStepIDEmail,
		Instructions: instructions,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type:    bridgev2.LoginInputFieldTypeEmail,
				ID:      loginFieldEmail,
				Name:    "Email",
				Pattern: `^[^@\s]+@[^@\s]+\.[^@\s]+$`,
			}},
		},
	}
}

type slackCaptchaExtractionConfig struct {
	SiteKey string `json:"siteKey"`
}

//go:embed login-email-captcha.js
var slackCaptchaExtractionFunction string

func slackCaptchaExtractionJS(captcha *slackLoginCaptcha) (string, error) {
	if captcha == nil || strings.TrimSpace(captcha.SiteKey) == "" {
		return "", errors.New("slack CAPTCHA response did not contain a site key")
	}
	configJSON, err := json.Marshal(slackCaptchaExtractionConfig{SiteKey: captcha.SiteKey})
	if err != nil {
		return "", fmt.Errorf("failed to marshal Slack CAPTCHA extraction config: %w", err)
	}
	return "(" + slackCaptchaExtractionFunction + ")(" + string(configJSON) + ")", nil
}

func slackCaptchaStep(captcha *slackLoginCaptcha, instructions string) (*bridgev2.LoginStep, error) {
	extractJS, err := slackCaptchaExtractionJS(captcha)
	if err != nil {
		return nil, err
	}
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeCookies,
		StepID:       LoginStepIDEmailCaptcha,
		Instructions: instructions,
		CookiesParams: &bridgev2.LoginCookiesParams{
			URL:       slackLoginCaptchaPageURL,
			ExtractJS: extractJS,
			Fields: []bridgev2.LoginCookieField{{
				ID:       loginFieldCaptchaToken,
				Required: true,
				Sources: []bridgev2.LoginCookieFieldSource{{
					Type: bridgev2.LoginCookieTypeSpecial,
					Name: loginFieldCaptchaToken,
				}},
			}},
		},
	}, nil
}

func slackEmailCodeStep(instructions string) *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       LoginStepIDEmailCode,
		Instructions: instructions,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type:        bridgev2.LoginInputFieldType2FACode,
				ID:          loginFieldEmailCode,
				Name:        "Confirmation code",
				Description: "Six-character code from Slack",
				Pattern:     `^[a-zA-Z0-9]{3}-?[a-zA-Z0-9]{3}$`,
			}},
		},
	}
}

func slackWorkspaceStep(options []string, instructions string) *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       LoginStepIDWorkspace,
		Instructions: instructions,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type:        bridgev2.LoginInputFieldTypeSelect,
				ID:          loginFieldWorkspace,
				Name:        "Workspace",
				Description: "Slack workspace to connect",
				Options:     options,
			}},
		},
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

func slackWorkspaceOptions(workspaces map[string]slackLoginWorkspace) []string {
	options := make([]string, 0, len(workspaces))
	for label := range workspaces {
		options = append(options, label)
	}
	slices.Sort(options)
	return options
}

func validSlackLoginEmail(input string) bool {
	address, err := mail.ParseAddress(input)
	return err == nil && address.Address == input && strings.Contains(input, "@")
}

func normalizeSlackEmailCode(input string) string {
	return strings.ToUpper(strings.TrimSpace(input))
}

func safeSlackLoginError(err error) string {
	var apiErr *slackLoginAPIError
	if errors.As(err, &apiErr) {
		return slackLoginErrorInstructions(apiErr)
	}
	return "Slack did not complete the workspace login"
}

func slackLoginErrorInstructions(err *slackLoginAPIError) string {
	switch err.Code {
	case "ratelimited", "rate_limited", "code_request_limit_reached":
		return "Slack is rate limiting email sign-in. Wait a few minutes before trying again."
	case "invalid_email", "email_invalid":
		return "Slack rejected that email address. Check it and try again."
	case "captcha_required", "captcha_failed":
		return "Slack requires a CAPTCHA for this sign-in. Complete the embedded challenge and try again."
	default:
		return "Slack could not start email sign-in. Try again later."
	}
}
