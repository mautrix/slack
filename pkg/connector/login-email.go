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
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"
	"maunium.net/go/mautrix/bridgev2"
)

const (
	LoginFlowIDEmail             = "email"
	LoginStepIDEmail             = "fi.mau.slack.login.enter_email"
	LoginStepIDEmailCaptcha      = "fi.mau.slack.login.email_captcha"
	LoginStepIDEmailCode         = "fi.mau.slack.login.enter_email_code"
	LoginStepIDWorkspace         = "fi.mau.slack.login.select_workspace"
	loginFieldEmail              = "email"
	loginFieldCaptchaToken       = "captcha_token"
	loginFieldEmailCode          = "code"
	loginFieldWorkspace          = "workspace"
	slackLoginAPIBaseURL         = "https://slack.com/"
	slackLoginAppBaseURL         = "https://app.slack.com/"
	slackLoginCaptchaPageURL     = "https://slack.com/signin"
	slackLoginMaxResponseSize    = 16 * 1024 * 1024
	slackLoginDefaultRequestTime = 30 * time.Second
)

var (
	slackAuthTokenPattern = regexp.MustCompile(`xoxc-[a-zA-Z0-9-]+`)
	slackEmailCodePattern = regexp.MustCompile(`^[a-zA-Z0-9]{3}-?[a-zA-Z0-9]{3}$`)
)

type slackLoginCaptcha struct {
	SiteKey string `json:"sitekey"`
}

type slackEmailLoginAPI interface {
	RequestCode(ctx context.Context, email string) (*slackLoginCaptcha, error)
	SubmitCaptcha(ctx context.Context, email, captchaResponse string) error
	ConfirmCode(ctx context.Context, email, code string) ([]slackLoginWorkspace, error)
	LoginWorkspace(ctx context.Context, workspace slackLoginWorkspace) (token, cookieToken string, err error)
}

type slackLoginCompleter func(ctx context.Context, user *bridgev2.User, token, cookieToken string) (*bridgev2.LoginStep, error)

type SlackEmailLogin struct {
	User       *bridgev2.User
	API        slackEmailLoginAPI
	complete   slackLoginCompleter
	email      string
	captcha    *slackLoginCaptcha
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
	token, cookieToken, err := s.API.LoginWorkspace(ctx, workspace)
	if err != nil {
		return slackWorkspaceStep(slackWorkspaceOptions(s.workspaces), fmt.Sprintf("Could not sign in to %s: %s", workspace.displayName(), safeSlackLoginError(err))), nil
	}
	complete := s.complete
	if complete == nil {
		complete = completeSlackTokenLogin
	}
	step, err := complete(ctx, s.User, token, cookieToken)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("workspace_id", workspace.ID).Msg("Failed to validate Slack email login session")
		s.email = ""
		s.captcha = nil
		s.workspaces = nil
		return slackEmailStep("Slack created a session, but the bridge could not validate it. Enter your email to try again."), nil
	}
	return step, nil
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

type slackLoginAPIClient struct {
	client     *http.Client
	apiBaseURL *url.URL
	appBaseURL *url.URL
}

func newSlackLoginAPI() *slackLoginAPIClient {
	jar, _ := cookiejar.New(nil)
	return newSlackLoginAPIWithClient(
		&http.Client{Jar: jar, Timeout: slackLoginDefaultRequestTime},
		slackLoginAPIBaseURL,
		slackLoginAppBaseURL,
	)
}

func newSlackLoginAPIWithClient(client *http.Client, apiBaseURL, appBaseURL string) *slackLoginAPIClient {
	apiURL, err := url.Parse(apiBaseURL)
	if err != nil {
		panic(err)
	}
	appURL, err := url.Parse(appBaseURL)
	if err != nil {
		panic(err)
	}
	return &slackLoginAPIClient{
		client:     client,
		apiBaseURL: apiURL,
		appBaseURL: appURL,
	}
}

type slackLoginAPIResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

type slackLoginAPIError struct {
	Method     string
	Code       string
	HTTPStatus int
}

func (s *slackLoginAPIError) Error() string {
	if s.Code != "" {
		return fmt.Sprintf("Slack %s failed: %s", s.Method, s.Code)
	}
	return fmt.Sprintf("Slack %s failed with HTTP %d", s.Method, s.HTTPStatus)
}

func (s *slackLoginAPIClient) RequestCode(ctx context.Context, email string) (*slackLoginCaptcha, error) {
	var checkResponse struct {
		slackLoginAPIResponse
		ChallengeResponse bool `json:"challenge_response"`
	}
	err := s.postAPI(ctx, "signup.checkEmail", "", map[string]string{
		"email":    email,
		"get_info": "undefined",
	}, &checkResponse)
	if err != nil {
		return nil, err
	}
	if checkResponse.ChallengeResponse {
		var captchaResponse struct {
			slackLoginAPIResponse
			slackLoginCaptcha
		}
		err = s.postAPI(ctx, "auth.captcha", "fetch-auth-captcha-signin", nil, &captchaResponse)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(captchaResponse.SiteKey) == "" {
			return nil, errors.New("slack auth.captcha response did not contain a site key")
		}
		return &captchaResponse.slackLoginCaptcha, nil
	}

	return nil, s.confirmEmail(ctx, email, "")
}

func (s *slackLoginAPIClient) SubmitCaptcha(ctx context.Context, email, captchaResponse string) error {
	if strings.TrimSpace(captchaResponse) == "" {
		return errors.New("slack CAPTCHA response is blank")
	}
	return s.confirmEmail(ctx, email, captchaResponse)
}

func (s *slackLoginAPIClient) confirmEmail(ctx context.Context, email, captchaResponse string) error {
	var confirmResponse struct {
		slackLoginAPIResponse
	}
	values := map[string]string{
		"email":       email,
		"locale":      "en-US",
		"entry_point": "signin",
	}
	if captchaResponse != "" {
		values["captcha_response"] = captchaResponse
	}
	return s.postAPI(ctx, "signup.confirmEmail", "", values, &confirmResponse)
}

func (s *slackLoginAPIClient) ConfirmCode(ctx context.Context, email, code string) ([]slackLoginWorkspace, error) {
	var confirmResponse struct {
		slackLoginAPIResponse
		MagicLoginURL string `json:"magic_login_url"`
		HasWorkspace  bool   `json:"has_workspace"`
	}
	err := s.postAPI(ctx, "signin.confirmCode", "", map[string]string{
		"email": email,
		"code":  code,
	}, &confirmResponse)
	if err != nil {
		return nil, err
	}

	var workspaceResponse slackFindWorkspacesResponse
	err = s.postAPI(ctx, "signin.findWorkspaces", "get_started_workspaces", nil, &workspaceResponse)
	if err != nil {
		return nil, err
	}
	return workspaceResponse.flatten(), nil
}

type slackFindWorkspacesResponse struct {
	slackLoginAPIResponse
	ConfirmedEmail string                `json:"confirmed_email"`
	CurrentTeams   []slackWorkspaceGroup `json:"current_teams"`
	CurrentOrgs    []slackWorkspaceOrg   `json:"current_orgs"`
}

type slackWorkspaceGroup struct {
	Email string                `json:"email"`
	Teams []slackLoginWorkspace `json:"teams"`
}

type slackWorkspaceOrg struct {
	Email string                `json:"email"`
	Org   *slackLoginWorkspace  `json:"org"`
	Teams []slackLoginWorkspace `json:"teams"`
}

type slackLoginWorkspace struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	URL               string `json:"url"`
	Domain            string `json:"domain"`
	MagicLoginURL     string `json:"magic_login_url"`
	MagicLoginCode    string `json:"magic_login_code"`
	SSORequired       bool   `json:"sso_required"`
	TwoFactorRequired bool   `json:"two_factor_required"`
}

type slackMagicLoginResponse struct {
	slackLoginAPIResponse
	TokenResults map[string]slackMagicLoginResult `json:"token_results"`
}

type slackMagicLoginResult struct {
	OK        bool                 `json:"ok"`
	Error     string               `json:"error"`
	Token     string               `json:"token"`
	Redir     string               `json:"redir"`
	AuthRedir string               `json:"auth_redir"`
	Team      *slackLoginWorkspace `json:"team"`
}

func (s slackLoginWorkspace) displayName() string {
	if s.Name != "" {
		return s.Name
	}
	if s.Domain != "" {
		return s.Domain
	}
	return "Slack workspace"
}

func (s slackLoginWorkspace) loginLabel() string {
	name := s.displayName()
	if s.Domain != "" && s.Domain != name {
		return fmt.Sprintf("%s (%s.slack.com)", name, strings.TrimSuffix(s.Domain, ".slack.com"))
	}
	return name
}

func (s *slackFindWorkspacesResponse) flatten() []slackLoginWorkspace {
	workspaces := make([]slackLoginWorkspace, 0)
	seen := make(map[string]struct{})
	add := func(workspace slackLoginWorkspace) {
		if workspace.ID == "" ||
			workspace.SSORequired ||
			workspace.TwoFactorRequired ||
			(workspace.MagicLoginURL == "" && workspace.MagicLoginCode == "") {
			return
		}
		if _, ok := seen[workspace.ID]; ok {
			return
		}
		seen[workspace.ID] = struct{}{}
		workspaces = append(workspaces, workspace)
	}
	for _, group := range s.CurrentTeams {
		for _, workspace := range group.Teams {
			add(workspace)
		}
	}
	for _, org := range s.CurrentOrgs {
		if org.Org != nil {
			add(*org.Org)
		}
		for _, workspace := range org.Teams {
			add(workspace)
		}
	}
	return workspaces
}

func (s *slackLoginAPIClient) LoginWorkspace(ctx context.Context, workspace slackLoginWorkspace) (string, string, error) {
	var (
		token         string
		cookieToken   string
		clientBootURL string
	)
	if workspace.MagicLoginCode != "" {
		magicResponse, responseBody, responseURL, err := s.exchangeMagicLoginCode(ctx, workspace.MagicLoginCode)
		if err != nil {
			return "", "", err
		}
		token = slackAuthTokenPattern.FindString(string(responseBody))
		cookieToken = s.findCookieToken(responseURL)
		clientBootURL, err = s.magicLoginClientURL(magicResponse, workspace)
		if err != nil {
			return "", "", err
		}
	} else {
		responseBody, responseURL, err := s.getSlackLoginURL(ctx, workspace.MagicLoginURL)
		if err != nil {
			return "", "", err
		}
		token = slackAuthTokenPattern.FindString(string(responseBody))
		cookieToken = s.findCookieToken(responseURL)
		clientBootURL = s.workspaceClientURL(workspace)
	}

	if token == "" {
		if clientBootURL == "" {
			clientBootURL = s.workspaceClientURL(workspace)
		}
		authToken, responseURL, err := s.fetchAuthToken(ctx, workspace, clientBootURL)
		if err != nil {
			return "", "", err
		}
		token = authToken
		if cookieToken == "" {
			cookieToken = s.findCookieToken(responseURL)
		}
	}
	if token == "" {
		return "", "", errors.New("slack client boot response did not contain an auth token")
	}
	if cookieToken == "" {
		return "", "", errors.New("slack magic login did not set a cookie token")
	}
	return token, cookieToken, nil
}

func (s *slackLoginAPIClient) fetchAuthToken(
	ctx context.Context,
	workspace slackLoginWorkspace,
	returnTo string,
) (string, *url.URL, error) {
	returnToURL, err := url.Parse(returnTo)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse Slack client return URL: %w", err)
	}
	query := url.Values{
		"app":           {"client"},
		"response_type": {"json"},
		"return_to":     {returnToURL.RequestURI()},
		"teams":         {workspace.ID},
	}
	endpoint := s.appBaseURL.ResolveReference(&url.URL{
		Path:     "auth",
		RawQuery: query.Encode(),
	})
	responseBody, responseURL, err := s.getSlackLoginURL(ctx, endpoint.String())
	if err != nil {
		return "", responseURL, fmt.Errorf("failed to fetch Slack client credentials: %w", err)
	}
	var response struct {
		Teams map[string]struct {
			Token string `json:"token"`
		} `json:"teams"`
	}
	if err = json.Unmarshal(responseBody, &response); err != nil {
		return "", responseURL, fmt.Errorf("failed to decode Slack client credentials: %w", err)
	}
	if team, ok := response.Teams[workspace.ID]; ok && team.Token != "" {
		return team.Token, responseURL, nil
	}
	return "", responseURL, errors.New("slack client credentials did not contain an auth token for the selected workspace")
}

func (s *slackLoginAPIClient) exchangeMagicLoginCode(
	ctx context.Context,
	magicLoginCode string,
) (*slackMagicLoginResponse, []byte, *url.URL, error) {
	query := url.Values{
		"magic_tokens": {magicLoginCode},
		"ssb":          {"1"},
	}
	endpoint := s.appBaseURL.ResolveReference(&url.URL{
		Path:     "api/auth.loginMagicBulk",
		RawQuery: query.Encode(),
	})
	responseBody, responseURL, err := s.getSlackLoginURL(ctx, endpoint.String())
	if err != nil {
		return nil, nil, responseURL, err
	}
	var response slackMagicLoginResponse
	if err = json.Unmarshal(responseBody, &response); err != nil {
		return nil, nil, responseURL, fmt.Errorf("failed to decode Slack magic login response: %w", err)
	}
	if !response.OK {
		return nil, nil, responseURL, &slackLoginAPIError{
			Method: "auth.loginMagicBulk",
			Code:   response.Error,
		}
	}
	if len(response.TokenResults) == 0 {
		return nil, nil, responseURL, errors.New("slack magic login returned no token results")
	}
	return &response, responseBody, responseURL, nil
}

func (s *slackLoginAPIClient) magicLoginClientURL(
	response *slackMagicLoginResponse,
	workspace slackLoginWorkspace,
) (string, error) {
	var result *slackMagicLoginResult
	if exactResult, ok := response.TokenResults[workspace.MagicLoginCode]; ok {
		result = &exactResult
	} else {
		for _, candidate := range response.TokenResults {
			candidateCopy := candidate
			result = &candidateCopy
			break
		}
	}
	if result == nil {
		return "", errors.New("slack magic login result was empty")
	}
	if result.Error != "" {
		return "", &slackLoginAPIError{Method: "auth.loginMagicBulk", Code: result.Error}
	}
	if result.AuthRedir != "" {
		return "", errors.New("slack workspace requires an additional browser authentication step")
	}
	if result.Team != nil {
		if clientURL := s.workspaceClientURL(*result.Team); clientURL != "" {
			return clientURL, nil
		}
	}
	if clientURL := s.workspaceClientURL(workspace); clientURL != "" {
		return clientURL, nil
	}
	if result.Redir != "" {
		if parsedRedir, err := url.Parse(result.Redir); err == nil &&
			parsedRedir.IsAbs() &&
			(parsedRedir.Scheme == "https" || parsedRedir.Scheme == "http") {
			return parsedRedir.String(), nil
		}
	}
	return "", nil
}

func (s *slackLoginAPIClient) workspaceClientURL(workspace slackLoginWorkspace) string {
	if workspace.ID == "" {
		return ""
	}
	return s.appBaseURL.ResolveReference(&url.URL{
		Path: "client/" + url.PathEscape(workspace.ID),
	}).String()
}

func (s *slackLoginAPIClient) getSlackLoginURL(ctx context.Context, loginURL string) ([]byte, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build Slack login request: %w", err)
	}
	req.Header.Set("User-Agent", slack.DefaultUserAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("slack login request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, resp.Request.URL, &slackLoginAPIError{Method: "magic login", HTTPStatus: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, slackLoginMaxResponseSize+1))
	if err != nil {
		return nil, resp.Request.URL, fmt.Errorf("failed to read Slack login response: %w", err)
	}
	if len(body) > slackLoginMaxResponseSize {
		return nil, resp.Request.URL, errors.New("slack login response was too large")
	}
	return body, resp.Request.URL, nil
}

func (s *slackLoginAPIClient) findCookieToken(requestURL *url.URL) string {
	if s.client.Jar == nil {
		return ""
	}
	urls := []*url.URL{requestURL, s.apiBaseURL, s.appBaseURL}
	for _, candidate := range urls {
		for _, cookie := range s.client.Jar.Cookies(candidate) {
			if cookie.Name != "d" {
				continue
			}
			value, err := url.QueryUnescape(cookie.Value)
			if err == nil {
				return value
			}
			return cookie.Value
		}
	}
	return ""
}

func (s *slackLoginAPIClient) postAPI(ctx context.Context, method, reason string, values map[string]string, output any) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if err := writer.WriteField(key, values[key]); err != nil {
			return fmt.Errorf("failed to encode Slack %s request: %w", method, err)
		}
	}
	if reason != "" {
		if err := writer.WriteField("_x_reason", reason); err != nil {
			return fmt.Errorf("failed to encode Slack %s request reason: %w", method, err)
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to finalize Slack %s request: %w", method, err)
	}

	endpoint := s.apiBaseURL.ResolveReference(&url.URL{
		Path:     "api/" + method,
		RawQuery: "_x_id=" + url.QueryEscape(newSlackLoginRequestID()),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), &body)
	if err != nil {
		return fmt.Errorf("failed to build Slack %s request: %w", method, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Referer", s.apiBaseURL.ResolveReference(&url.URL{Path: "signin"}).String())
	req.Header.Set("User-Agent", slack.DefaultUserAgent)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("slack %s request failed: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusTooManyRequests {
		return &slackLoginAPIError{Method: method, HTTPStatus: resp.StatusCode}
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, slackLoginMaxResponseSize+1))
	if err != nil {
		return fmt.Errorf("failed to read Slack %s response: %w", method, err)
	}
	if len(responseBody) > slackLoginMaxResponseSize {
		return fmt.Errorf("slack %s response was too large", method)
	}
	var response slackLoginAPIResponse
	if err = json.Unmarshal(responseBody, &response); err != nil {
		if resp.StatusCode == http.StatusTooManyRequests {
			return &slackLoginAPIError{Method: method, Code: "ratelimited", HTTPStatus: resp.StatusCode}
		}
		return fmt.Errorf("failed to inspect Slack %s response status: %w", method, err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		if response.Error == "" {
			response.Error = "ratelimited"
		}
		return &slackLoginAPIError{Method: method, Code: response.Error, HTTPStatus: resp.StatusCode}
	}
	if err = json.Unmarshal(responseBody, output); err != nil {
		return fmt.Errorf("failed to decode Slack %s response: %w", method, err)
	}
	if !response.OK {
		return &slackLoginAPIError{Method: method, Code: response.Error, HTTPStatus: resp.StatusCode}
	}
	return nil
}

func newSlackLoginRequestID() string {
	nowMillis := time.Now().UnixMilli()
	return fmt.Sprintf("noversion-%d.%03d", nowMillis/1000, nowMillis%1000)
}
