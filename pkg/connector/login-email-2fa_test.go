package connector

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/bridgev2"
)

func TestSlackEmailLoginTwoFactorCodeIsNative(t *testing.T) {
	workspace := slackLoginWorkspace{
		ID:                "T1",
		Name:              "Protected",
		Domain:            "protected",
		MagicLoginCode:    "magic",
		TwoFactorRequired: true,
	}
	api := &fakeSlackEmailLoginAPI{
		workspaces:  []slackLoginWorkspace{workspace},
		twoFactor:   &slackLoginTwoFactor{},
		token:       "xoxs-test",
		cookieToken: "",
	}
	completed := false
	login := &SlackEmailLogin{
		API:   api,
		email: "person@example.com",
		complete: func(_ context.Context, _ *bridgev2.User, token, cookieToken string) (*bridgev2.LoginStep, error) {
			assert.Equal(t, "xoxs-test", token)
			assert.Empty(t, cookieToken)
			completed = true
			return &bridgev2.LoginStep{Type: bridgev2.LoginStepTypeComplete}, nil
		},
	}

	step, err := login.SubmitUserInput(context.Background(), map[string]string{
		loginFieldEmailCode: "ABC123",
	})
	require.NoError(t, err)
	require.NotNil(t, step.UserInputParams)
	assert.Equal(t, bridgev2.LoginStepTypeUserInput, step.Type)
	assert.Equal(t, bridgev2.LoginInputFieldType2FACode, step.UserInputParams.Fields[0].Type)
	assert.Equal(t, loginFieldTwoFactorCode, step.UserInputParams.Fields[0].ID)
	require.NotNil(t, login.twoFactor)

	step, err = login.SubmitUserInput(context.Background(), map[string]string{
		loginFieldTwoFactorCode: "123456",
	})
	require.NoError(t, err)
	assert.Equal(t, bridgev2.LoginStepTypeComplete, step.Type)
	assert.Nil(t, step.CookiesParams)
	assert.True(t, completed)
	assert.Equal(t, "123456", api.submittedTwoFactor)
	assert.Equal(t, 1, api.submitTwoFactorCalls)
	assert.Nil(t, login.twoFactor)
}

func TestSlackEmailLoginTwoFactorRetryReturnsNativeCode(t *testing.T) {
	workspace := slackLoginWorkspace{
		ID:                "T1",
		Name:              "Protected",
		MagicLoginCode:    "magic",
		TwoFactorRequired: true,
	}
	api := &fakeSlackEmailLoginAPI{
		submitTwoFactorErr: &slackLoginAPIError{
			Method: "auth.loginMagic",
			Code:   "invalid_pin",
		},
	}
	login := &SlackEmailLogin{
		API:        api,
		workspaces: map[string]slackLoginWorkspace{"Protected": workspace},
		twoFactor:  &slackLoginTwoFactor{Workspace: workspace},
	}

	step, err := login.SubmitUserInput(context.Background(), map[string]string{
		loginFieldTwoFactorCode: "123456",
	})
	require.NoError(t, err)
	assert.Equal(t, bridgev2.LoginStepTypeUserInput, step.Type)
	assert.Nil(t, step.CookiesParams)
	assert.Contains(t, step.Instructions, "rejected")
	assert.Equal(t, 0, api.startTwoFactorCalls)
	assert.Equal(t, 1, api.submitTwoFactorCalls)
	require.NotNil(t, login.twoFactor)
}

func TestSlackEmailLoginTwoFactorRejectsMalformedCodeLocally(t *testing.T) {
	workspace := slackLoginWorkspace{MagicLoginCode: "magic"}
	api := &fakeSlackEmailLoginAPI{}
	login := &SlackEmailLogin{
		API:       api,
		twoFactor: &slackLoginTwoFactor{Workspace: workspace},
	}

	step, err := login.SubmitUserInput(context.Background(), map[string]string{
		loginFieldTwoFactorCode: "12345",
	})
	require.NoError(t, err)
	assert.Equal(t, bridgev2.LoginStepTypeUserInput, step.Type)
	assert.Contains(t, step.Instructions, "six-digit")
	assert.Equal(t, 0, api.submitTwoFactorCalls)
}

func TestSlackLoginAPIStartsNativeTwoFactor(t *testing.T) {
	requests := 0
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/auth.loginMagic", r.URL.Path)
		fields := readMultipartFields(t, r)
		assert.Equal(t, map[string]string{
			"magic_token":                 "magic",
			"two_factor_is_backup":        "0",
			"two_factor_native_supported": "1",
		}, fields)
		return jsonResponse(r, `{"ok":false,"error":"missing_pin_app_sms"}`), nil
	})
	api := newSlackLoginAPIWithClient(
		&http.Client{Transport: transport},
		"https://slack.com",
		"https://app.slack.com",
	)

	challenge, err := api.StartTwoFactor(context.Background(), slackLoginWorkspace{
		ID:             "T1",
		MagicLoginCode: "magic",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, requests)
	require.NotNil(t, challenge)
	assert.Equal(t, "T1", challenge.Workspace.ID)
}

func TestSlackLoginAPIStartTwoFactorRejectsInvalidMagicToken(t *testing.T) {
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(r, `{"ok":false,"error":"invalid_token"}`), nil
	})
	api := newSlackLoginAPIWithClient(
		&http.Client{Transport: transport},
		"https://slack.com",
		"https://app.slack.com",
	)

	_, err := api.StartTwoFactor(context.Background(), slackLoginWorkspace{MagicLoginCode: "expired"})
	var apiErr *slackLoginAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "auth.loginMagic", apiErr.Method)
	assert.Equal(t, "invalid_token", apiErr.Code)
}

func TestSlackLoginAPISubmitsNativeTwoFactor(t *testing.T) {
	requests := 0
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		fields := readMultipartFields(t, r)
		assert.Equal(t, map[string]string{
			"magic_token":                 "magic",
			"two_factor_is_backup":        "0",
			"two_factor_native_supported": "1",
			"two_factor_pin":              "123456",
		}, fields)
		return jsonResponse(r, `{
			"ok":true,
			"token":"xoxs-native-token",
			"team":"T1",
			"user":"U1"
		}`), nil
	})
	api := newSlackLoginAPIWithClient(
		&http.Client{Transport: transport},
		"https://slack.com",
		"https://app.slack.com",
	)
	challenge := &slackLoginTwoFactor{
		Workspace: slackLoginWorkspace{
			ID:             "T1",
			MagicLoginCode: "magic",
		},
	}

	token, cookieToken, err := api.SubmitTwoFactor(context.Background(), challenge, "123456")
	require.NoError(t, err)
	assert.Equal(t, 1, requests)
	assert.Equal(t, "xoxs-native-token", token)
	assert.Empty(t, cookieToken)
}

func TestSlackLoginAPISubmitTwoFactorReturnsNativeErrors(t *testing.T) {
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(r, `{"ok":false,"error":"invalid_pin"}`), nil
	})
	api := newSlackLoginAPIWithClient(
		&http.Client{Transport: transport},
		"https://slack.com",
		"https://app.slack.com",
	)
	challenge := &slackLoginTwoFactor{
		Workspace: slackLoginWorkspace{MagicLoginCode: "magic"},
	}

	_, _, err := api.SubmitTwoFactor(context.Background(), challenge, "123456")
	var apiErr *slackLoginAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "auth.loginMagic", apiErr.Method)
	assert.Equal(t, "invalid_pin", apiErr.Code)
}

func TestSlackLoginAPISubmitTwoFactorRequiresNativeState(t *testing.T) {
	api := newSlackLoginAPIWithClient(&http.Client{}, "https://slack.com", "https://app.slack.com")

	_, _, err := api.SubmitTwoFactor(context.Background(), nil, "123456")
	assert.ErrorContains(t, err, "not initialized")

	challenge := &slackLoginTwoFactor{
		Workspace: slackLoginWorkspace{MagicLoginCode: "magic"},
	}
	_, _, err = api.SubmitTwoFactor(context.Background(), challenge, "")
	assert.ErrorContains(t, err, "blank")

	challenge.Workspace.MagicLoginCode = ""
	_, _, err = api.SubmitTwoFactor(context.Background(), challenge, "123456")
	var apiErr *slackLoginAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "two_factor_state_expired", apiErr.Code)
}

func readMultipartFields(t *testing.T, r *http.Request) map[string]string {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	require.NoError(t, err)
	assert.Equal(t, "multipart/form-data", mediaType)
	reader := multipart.NewReader(r.Body, params["boundary"])
	fields := make(map[string]string)
	for {
		part, partErr := reader.NextPart()
		if partErr == io.EOF {
			break
		}
		require.NoError(t, partErr)
		value, readErr := io.ReadAll(part)
		require.NoError(t, readErr)
		fields[part.FormName()] = string(value)
	}
	return fields
}

func jsonResponse(r *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
