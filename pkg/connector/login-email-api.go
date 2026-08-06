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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/slack-go/slack"
)

const (
	slackLoginAPIBaseURL         = "https://slack.com/"
	slackLoginAppBaseURL         = "https://app.slack.com/"
	slackLoginMaxResponseSize    = 16 * 1024 * 1024
	slackLoginDefaultRequestTime = 30 * time.Second
)

var slackAuthTokenPattern = regexp.MustCompile(`xoxc-[a-zA-Z0-9-]+`)

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

type slackLoginCaptcha struct {
	SiteKey string `json:"sitekey"`
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
