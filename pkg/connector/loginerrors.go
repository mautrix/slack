// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
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
	"errors"
	"fmt"
	"net/http"

	"github.com/slack-go/slack"
	"maunium.net/go/mautrix/bridgev2"
)

// Login failures that a user can act on. Anything not listed here falls through to
// ErrLoginUnknown, which keeps the underlying error in the chain for the logs but gives
// the client a generic message rather than raw Go error text.
var (
	ErrLoginInvalidAuth = bridgev2.RespError{
		ErrCode:    "FI.MAU.SLACK.INVALID_AUTH",
		Err:        "Slack rejected the token. Please log in again to get a fresh one.",
		StatusCode: http.StatusUnauthorized,
	}
	ErrLoginTokenRevoked = bridgev2.RespError{
		ErrCode:    "FI.MAU.SLACK.TOKEN_REVOKED",
		Err:        "This token has been revoked. Please log in again.",
		StatusCode: http.StatusUnauthorized,
	}
	ErrLoginAccountInactive = bridgev2.RespError{
		ErrCode:    "FI.MAU.SLACK.ACCOUNT_INACTIVE",
		Err:        "This Slack account has been deactivated. Ask a workspace admin to reactivate it.",
		StatusCode: http.StatusForbidden,
	}
	ErrLoginMissingScope = bridgev2.RespError{
		ErrCode:    "FI.MAU.SLACK.MISSING_SCOPE",
		Err:        "This token is missing permissions the bridge needs. Please reinstall the app and try again.",
		StatusCode: http.StatusForbidden,
	}
	ErrLoginTeamDisabled = bridgev2.RespError{
		ErrCode:    "FI.MAU.SLACK.TEAM_UNAVAILABLE",
		Err:        "This Slack workspace is no longer available.",
		StatusCode: http.StatusForbidden,
	}
	ErrLoginRateLimited = bridgev2.RespError{
		ErrCode:    "FI.MAU.SLACK.RATE_LIMITED",
		Err:        "Slack is rate limiting the login. Please wait a moment and try again.",
		StatusCode: http.StatusTooManyRequests,
	}
	ErrLoginBadEmailCode = bridgev2.RespError{
		ErrCode:    "FI.MAU.SLACK.BAD_EMAIL_CODE",
		Err:        "That confirmation code wasn't accepted. Please check the code and try again.",
		StatusCode: http.StatusBadRequest,
	}
	ErrLoginBadTwoFactorCode = bridgev2.RespError{
		ErrCode:    "FI.MAU.SLACK.BAD_2FA_CODE",
		Err:        "That two-factor code wasn't accepted. Please try again.",
		StatusCode: http.StatusBadRequest,
	}
	ErrLoginCaptchaRejected = bridgev2.RespError{
		ErrCode:    "FI.MAU.SLACK.CAPTCHA_REJECTED",
		Err:        "Slack rejected the CAPTCHA. Please try again.",
		StatusCode: http.StatusBadRequest,
	}
	ErrLoginBrowserRequired = bridgev2.RespError{
		ErrCode:    "FI.MAU.SLACK.BROWSER_REQUIRED",
		Err:        "This workspace requires signing in through a browser. Use the auth token login flow instead.",
		StatusCode: http.StatusBadRequest,
	}
	ErrLoginUnknown = bridgev2.RespError{
		ErrCode:    "M_UNKNOWN",
		Err:        "Internal error logging in to Slack",
		StatusCode: http.StatusInternalServerError,
	}
)

// slackAPIErrorCode digs a Slack API error string (e.g. invalid_auth) out of the error
// chain, whichever client produced it. Returns "" if this isn't a Slack API error.
func slackAPIErrorCode(err error) string {
	var apiErr *slackLoginAPIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	var respErr slack.SlackErrorResponse
	if errors.As(err, &respErr) {
		return respErr.Err
	}
	var rateErr *slack.RateLimitedError
	if errors.As(err, &rateErr) {
		return "ratelimited"
	}
	return ""
}

// wrapSlackLoginError translates a Slack error into one the client can act on. The
// original error is kept in the chain either way, so logs are unaffected.
func wrapSlackLoginError(err error) error {
	if err == nil {
		return nil
	}
	var mapped bridgev2.RespError
	switch slackAPIErrorCode(err) {
	case "":
		return fmt.Errorf("%w: %w", ErrLoginUnknown, err)
	case "invalid_auth", "not_authed", "no_permission":
		mapped = ErrLoginInvalidAuth
	case "token_revoked", "token_expired":
		mapped = ErrLoginTokenRevoked
	case "account_inactive", "user_removed_from_team":
		mapped = ErrLoginAccountInactive
	case "missing_scope", "not_allowed_token_type", "invalid_token_type":
		mapped = ErrLoginMissingScope
	case "team_disabled", "team_not_found", "enterprise_is_restricted":
		mapped = ErrLoginTeamDisabled
	case "ratelimited", "rate_limited":
		mapped = ErrLoginRateLimited
	case "invalid_code", "expired_code", "code_already_used":
		mapped = ErrLoginBadEmailCode
	case "invalid_2fa_code", "two_factor_required", "bad_2fa_code":
		mapped = ErrLoginBadTwoFactorCode
	case "captcha_required", "invalid_captcha", "challenge_failed":
		mapped = ErrLoginCaptchaRejected
	case "browser_auth_required", "sso_required", "saml_required":
		mapped = ErrLoginBrowserRequired
	default:
		return fmt.Errorf("%w: %w", ErrLoginUnknown, err)
	}
	return fmt.Errorf("%w: %w", mapped, err)
}
