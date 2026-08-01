package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/bridgev2"
)

type fakeSlackEmailLoginAPI struct {
	captcha                *slackLoginCaptcha
	requestCodeErr         error
	submitCaptchaErr       error
	workspaces             []slackLoginWorkspace
	confirmCodeErr         error
	token                  string
	cookieToken            string
	loginErr               error
	twoFactor              *slackLoginTwoFactor
	startTwoFactorErr      error
	submitTwoFactorErr     error
	requestedEmail         string
	captchaEmail           string
	submittedCaptcha       string
	submitCaptchaCallCount int
	confirmedEmail         string
	confirmedCode          string
	loggedWorkspace        slackLoginWorkspace
	twoFactorWorkspace     slackLoginWorkspace
	startTwoFactorCalls    int
	submittedTwoFactor     string
	submitTwoFactorCalls   int
}

func (f *fakeSlackEmailLoginAPI) RequestCode(_ context.Context, email string) (*slackLoginCaptcha, error) {
	f.requestedEmail = email
	return f.captcha, f.requestCodeErr
}

func (f *fakeSlackEmailLoginAPI) SubmitCaptcha(_ context.Context, email, captchaResponse string) error {
	f.captchaEmail = email
	f.submittedCaptcha = captchaResponse
	f.submitCaptchaCallCount++
	return f.submitCaptchaErr
}

func (f *fakeSlackEmailLoginAPI) ConfirmCode(_ context.Context, email, code string) ([]slackLoginWorkspace, error) {
	f.confirmedEmail = email
	f.confirmedCode = code
	return f.workspaces, f.confirmCodeErr
}

func (f *fakeSlackEmailLoginAPI) LoginWorkspace(_ context.Context, workspace slackLoginWorkspace) (string, string, error) {
	f.loggedWorkspace = workspace
	return f.token, f.cookieToken, f.loginErr
}

func (f *fakeSlackEmailLoginAPI) StartTwoFactor(_ context.Context, workspace slackLoginWorkspace) (*slackLoginTwoFactor, error) {
	f.twoFactorWorkspace = workspace
	f.startTwoFactorCalls++
	return f.twoFactor, f.startTwoFactorErr
}

func (f *fakeSlackEmailLoginAPI) SubmitTwoFactor(_ context.Context, _ *slackLoginTwoFactor, code string) (string, string, error) {
	f.submittedTwoFactor = code
	f.submitTwoFactorCalls++
	return f.token, f.cookieToken, f.submitTwoFactorErr
}

func TestSlackLoginFlowsPreferNativeEmail(t *testing.T) {
	flows := (&SlackConnector{}).GetLoginFlows()
	require.Len(t, flows, 3)
	assert.Equal(t, LoginFlowIDEmail, flows[0].ID)
	assert.Equal(t, LoginFlowIDAuthToken, flows[1].ID)
	assert.Equal(t, LoginFlowIDApp, flows[2].ID)
}

func TestSlackLoginFlowsUseNativeUserInput(t *testing.T) {
	connector := &SlackConnector{}
	for _, flowID := range []string{LoginFlowIDEmail, LoginFlowIDAuthToken} {
		process, err := connector.CreateLogin(context.Background(), nil, flowID)
		require.NoError(t, err)
		step, err := process.Start(context.Background())
		require.NoError(t, err)
		assert.Equal(t, bridgev2.LoginStepTypeUserInput, step.Type)
		assert.Nil(t, step.CookiesParams)
	}
}

func TestSlackEmailLoginStateMachine(t *testing.T) {
	api := &fakeSlackEmailLoginAPI{
		workspaces: []slackLoginWorkspace{{
			ID:             "T2",
			Name:           "Second",
			Domain:         "second",
			MagicLoginCode: "magic-2",
		}, {
			ID:             "T1",
			Name:           "First",
			Domain:         "first",
			MagicLoginCode: "magic-1",
		}},
		token:       "xoxc-test",
		cookieToken: "xoxd-test",
	}
	var completedToken, completedCookie string
	login := &SlackEmailLogin{
		API: api,
		complete: func(_ context.Context, _ *bridgev2.User, token, cookieToken string) (*bridgev2.LoginStep, error) {
			completedToken = token
			completedCookie = cookieToken
			return &bridgev2.LoginStep{Type: bridgev2.LoginStepTypeComplete, StepID: LoginStepIDComplete}, nil
		},
	}

	step, err := login.Start(context.Background())
	require.NoError(t, err)
	require.Equal(t, LoginStepIDEmail, step.StepID)

	step, err = login.SubmitUserInput(context.Background(), map[string]string{loginFieldEmail: "person@example.com"})
	require.NoError(t, err)
	require.Equal(t, LoginStepIDEmailCode, step.StepID)
	assert.Equal(t, "person@example.com", api.requestedEmail)

	step, err = login.SubmitUserInput(context.Background(), map[string]string{loginFieldEmailCode: "abc-123"})
	require.NoError(t, err)
	require.Equal(t, LoginStepIDWorkspace, step.StepID)
	require.Len(t, step.UserInputParams.Fields, 1)
	assert.Equal(t, []string{"First (first.slack.com)", "Second (second.slack.com)"}, step.UserInputParams.Fields[0].Options)
	assert.Equal(t, "ABC123", api.confirmedCode)

	step, err = login.SubmitUserInput(context.Background(), map[string]string{loginFieldWorkspace: "First (first.slack.com)"})
	require.NoError(t, err)
	assert.Equal(t, bridgev2.LoginStepTypeComplete, step.Type)
	assert.Equal(t, "T1", api.loggedWorkspace.ID)
	assert.Equal(t, "xoxc-test", completedToken)
	assert.Equal(t, "xoxd-test", completedCookie)
}

func TestSlackEmailLoginCaptchaUsesEmbeddedWebviewAndKeepsNativeCodeStep(t *testing.T) {
	api := &fakeSlackEmailLoginAPI{
		captcha: &slackLoginCaptcha{SiteKey: "site-key"},
		workspaces: []slackLoginWorkspace{{
			ID:             "T1",
			Name:           "First",
			MagicLoginCode: "magic-1",
		}, {
			ID:             "T2",
			Name:           "Second",
			MagicLoginCode: "magic-2",
		}},
	}
	login := &SlackEmailLogin{API: api}
	step, err := login.SubmitUserInput(context.Background(), map[string]string{loginFieldEmail: "person@example.com"})
	require.NoError(t, err)
	require.NotNil(t, step)
	assert.Equal(t, bridgev2.LoginStepTypeCookies, step.Type)
	assert.Equal(t, LoginStepIDEmailCaptcha, step.StepID)
	assert.Contains(t, step.Instructions, "CAPTCHA")
	require.NotNil(t, step.CookiesParams)
	assert.Equal(t, slackLoginCaptchaPageURL, step.CookiesParams.URL)
	assert.Contains(t, step.CookiesParams.ExtractJS, `"siteKey":"site-key"`)
	assert.Contains(t, step.CookiesParams.ExtractJS, "grecaptcha.render")
	assert.Contains(t, step.CookiesParams.ExtractJS, "z-index:1999999999")
	assert.NotContains(t, step.CookiesParams.ExtractJS, "z-index:2147483646")
	assert.NotContains(t, step.CookiesParams.ExtractJS, "person@example.com")
	require.Len(t, step.CookiesParams.Fields, 1)
	assert.Equal(t, loginFieldCaptchaToken, step.CookiesParams.Fields[0].ID)
	assert.True(t, step.CookiesParams.Fields[0].Required)
	assert.Equal(t, bridgev2.LoginCookieTypeSpecial, step.CookiesParams.Fields[0].Sources[0].Type)
	assert.Equal(t, "person@example.com", login.email)
	require.NotNil(t, login.captcha)

	step, err = login.SubmitCookies(context.Background(), map[string]string{loginFieldCaptchaToken: "captcha-solution"})
	require.NoError(t, err)
	require.NotNil(t, step)
	assert.Equal(t, LoginStepIDEmailCode, step.StepID)
	assert.Equal(t, "person@example.com", api.captchaEmail)
	assert.Equal(t, "captcha-solution", api.submittedCaptcha)
	assert.Nil(t, login.captcha)

	step, err = login.SubmitUserInput(context.Background(), map[string]string{loginFieldEmailCode: "ABC-123"})
	require.NoError(t, err)
	require.NotNil(t, step)
	assert.Equal(t, LoginStepIDWorkspace, step.StepID)
	assert.Equal(t, "person@example.com", api.confirmedEmail)
	assert.Equal(t, "ABC123", api.confirmedCode)
}

func TestSlackEmailLoginCaptchaBlankAndRejectedSolutionsRetryWebview(t *testing.T) {
	api := &fakeSlackEmailLoginAPI{
		captcha:          &slackLoginCaptcha{SiteKey: "site-key"},
		submitCaptchaErr: &slackLoginAPIError{Method: "signup.confirmEmail", Code: "captcha_failed"},
	}
	login := &SlackEmailLogin{API: api}

	_, err := login.SubmitUserInput(context.Background(), map[string]string{loginFieldEmail: "person@example.com"})
	require.NoError(t, err)

	step, err := login.SubmitCookies(context.Background(), map[string]string{})
	require.NoError(t, err)
	require.Equal(t, LoginStepIDEmailCaptcha, step.StepID)
	assert.Contains(t, step.Instructions, "did not return")
	assert.Equal(t, 0, api.submitCaptchaCallCount)

	step, err = login.SubmitCookies(context.Background(), map[string]string{loginFieldCaptchaToken: "expired-solution"})
	require.NoError(t, err)
	require.Equal(t, LoginStepIDEmailCaptcha, step.StepID)
	assert.Contains(t, step.Instructions, "rejected or expired")
	assert.Equal(t, 1, api.submitCaptchaCallCount)
	assert.NotNil(t, login.captcha)
}

func TestSlackCaptchaExtractionJSEscapesSiteKey(t *testing.T) {
	extractJS, err := slackCaptchaExtractionJS(&slackLoginCaptcha{SiteKey: `"</script><script>alert(1)</script>`})
	require.NoError(t, err)
	assert.NotContains(t, extractJS, "</script>")
	assert.Contains(t, extractJS, `\u003c/script\u003e`)
	assert.Contains(t, extractJS, "captcha_token")
	assert.NotContains(t, extractJS, "%__CONFIG_REPLACEME__%")
	assert.NotContains(t, extractJS, "new Promise((res0, rej0)")

	_, err = slackCaptchaExtractionJS(&slackLoginCaptcha{})
	assert.Error(t, err)
}

func TestSlackEmailLoginSingleWorkspaceFailureCanRetry(t *testing.T) {
	api := &fakeSlackEmailLoginAPI{
		workspaces: []slackLoginWorkspace{{
			ID:             "T1",
			Name:           "Only",
			Domain:         "only",
			MagicLoginCode: "magic-1",
		}},
		loginErr: errors.New("magic login unavailable"),
	}
	login := &SlackEmailLogin{API: api}

	step, err := login.SubmitUserInput(context.Background(), map[string]string{loginFieldEmail: "person@example.com"})
	require.NoError(t, err)
	require.Equal(t, LoginStepIDEmailCode, step.StepID)

	step, err = login.SubmitUserInput(context.Background(), map[string]string{loginFieldEmailCode: "ABC123"})
	require.NoError(t, err)
	require.Equal(t, LoginStepIDWorkspace, step.StepID)
	assert.Equal(t, []string{"Only (only.slack.com)"}, step.UserInputParams.Fields[0].Options)

	step, err = login.SubmitUserInput(context.Background(), map[string]string{loginFieldWorkspace: "Only (only.slack.com)"})
	require.NoError(t, err)
	require.Equal(t, LoginStepIDWorkspace, step.StepID)
}

func TestSlackEmailLoginCompletionFailureRestartsEmailFlow(t *testing.T) {
	api := &fakeSlackEmailLoginAPI{
		workspaces: []slackLoginWorkspace{{
			ID:             "T1",
			Name:           "Only",
			MagicLoginCode: "magic-1",
		}},
		token:       "xoxc-test",
		cookieToken: "xoxd-test",
	}
	login := &SlackEmailLogin{
		API: api,
		complete: func(context.Context, *bridgev2.User, string, string) (*bridgev2.LoginStep, error) {
			return nil, errors.New("client.boot failed")
		},
	}

	_, err := login.SubmitUserInput(context.Background(), map[string]string{loginFieldEmail: "person@example.com"})
	require.NoError(t, err)
	step, err := login.SubmitUserInput(context.Background(), map[string]string{loginFieldEmailCode: "ABC123"})
	require.NoError(t, err)
	require.Equal(t, LoginStepIDEmail, step.StepID)
	assert.Empty(t, login.email)
	assert.Nil(t, login.workspaces)
	assert.Contains(t, step.Instructions, "could not validate")
}

func TestSlackLoginAPIRequestCodeContract(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseMultipartForm(1024*1024))
		assert.Regexp(t, `^noversion-\d{10}\.\d{3}$`, r.URL.Query().Get("_x_id"))
		assert.Empty(t, r.Header.Get("Origin"))
		methods = append(methods, r.URL.Path)
		switch r.URL.Path {
		case "/api/signup.checkEmail":
			assert.Equal(t, "person@example.com", r.FormValue("email"))
			assert.Equal(t, "undefined", r.FormValue("get_info"))
			_, _ = fmt.Fprint(w, `{"ok":true,"challenge_response":false}`)
		case "/api/signup.confirmEmail":
			assert.Equal(t, "person@example.com", r.FormValue("email"))
			assert.Equal(t, "en-US", r.FormValue("locale"))
			assert.Equal(t, "signin", r.FormValue("entry_point"))
			_, _ = fmt.Fprint(w, `{"ok":true,"is_alphanumeric_code":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	api := newSlackLoginAPIWithClient(&http.Client{Jar: jar}, server.URL+"/", server.URL+"/")
	captcha, err := api.RequestCode(context.Background(), "person@example.com")
	require.NoError(t, err)
	assert.Nil(t, captcha)
	assert.Equal(t, []string{"/api/signup.checkEmail", "/api/signup.confirmEmail"}, methods)
}

func TestSlackLoginAPIFetchesCaptchaConfiguration(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseMultipartForm(1024*1024))
		methods = append(methods, r.URL.Path)
		switch r.URL.Path {
		case "/api/signup.checkEmail":
			assert.Equal(t, "person@example.com", r.FormValue("email"))
			_, _ = fmt.Fprint(w, `{"ok":true,"challenge_response":true}`)
		case "/api/auth.captcha":
			assert.Equal(t, "fetch-auth-captcha-signin", r.FormValue("_x_reason"))
			_, _ = fmt.Fprint(w, `{"ok":true,"sitekey":"site-key"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	api := newSlackLoginAPIWithClient(server.Client(), server.URL+"/", server.URL+"/")
	captcha, err := api.RequestCode(context.Background(), "person@example.com")
	require.NoError(t, err)
	require.NotNil(t, captcha)
	assert.Equal(t, "site-key", captcha.SiteKey)
	assert.Equal(t, []string{"/api/signup.checkEmail", "/api/auth.captcha"}, methods)
}

func TestSlackLoginAPISubmitsCaptchaWithConfirmEmail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/signup.confirmEmail", r.URL.Path)
		require.NoError(t, r.ParseMultipartForm(1024*1024))
		assert.Equal(t, "person@example.com", r.FormValue("email"))
		assert.Equal(t, "en-US", r.FormValue("locale"))
		assert.Equal(t, "signin", r.FormValue("entry_point"))
		assert.Equal(t, "captcha-solution", r.FormValue("captcha_response"))
		_, _ = fmt.Fprint(w, `{"ok":true,"is_alphanumeric_code":true}`)
	}))
	defer server.Close()

	api := newSlackLoginAPIWithClient(server.Client(), server.URL+"/", server.URL+"/")
	require.NoError(t, api.SubmitCaptcha(context.Background(), "person@example.com", "captcha-solution"))
}

func TestSlackLoginAPIConfirmCodeFindsWorkspaces(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseMultipartForm(1024*1024))
		methods = append(methods, r.URL.Path)
		switch r.URL.Path {
		case "/api/signin.confirmCode":
			assert.Equal(t, "person@example.com", r.FormValue("email"))
			assert.Equal(t, "ABC123", r.FormValue("code"))
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "confirmed", Path: "/"})
			_, _ = fmt.Fprint(w, `{"ok":true,"has_workspace":true,"magic_login_url":"https://example.invalid/magic"}`)
		case "/api/signin.findWorkspaces":
			assert.Equal(t, "get_started_workspaces", r.FormValue("_x_reason"))
			_, _ = fmt.Fprint(w, `{
				"ok": true,
				"confirmed_email": "person@example.com",
				"current_teams": [{
					"email": "person@example.com",
					"teams": [
						{"id":"T1","name":"One","domain":"one","magic_login_code":"magic-1"},
						{"id":"T2","name":"Two","domain":"two","magic_login_url":"https://two.slack.com/magic"}
					]
				}],
				"current_orgs": []
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	api := newSlackLoginAPIWithClient(&http.Client{Jar: jar}, server.URL+"/", server.URL+"/")
	workspaces, err := api.ConfirmCode(context.Background(), "person@example.com", "ABC123")
	require.NoError(t, err)
	require.Len(t, workspaces, 2)
	assert.Equal(t, []string{"T1", "T2"}, []string{workspaces[0].ID, workspaces[1].ID})
	assert.Equal(t, []string{"/api/signin.confirmCode", "/api/signin.findWorkspaces"}, methods)
}

func TestSlackLoginAPIMagicLoginExtractsSession(t *testing.T) {
	var paths []string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/auth.loginMagicBulk":
			assert.Equal(t, "magic-1", r.URL.Query().Get("magic_tokens"))
			assert.Equal(t, "1", r.URL.Query().Get("ssb"))
			http.SetCookie(w, &http.Cookie{Name: "d", Value: "xoxd-test-cookie", Path: "/"})
			_, _ = fmt.Fprintf(w, `{
				"ok": true,
				"token_results": {
					"magic-1": {
						"ok": true,
						"team": {"id":"T1","url":%q}
					}
				}
			}`, server.URL+"/")
		case "/auth":
			assert.Equal(t, "client", r.URL.Query().Get("app"))
			assert.Equal(t, "json", r.URL.Query().Get("response_type"))
			assert.Equal(t, "/client/T1", r.URL.Query().Get("return_to"))
			assert.Equal(t, "T1", r.URL.Query().Get("teams"))
			cookie, err := r.Cookie("d")
			require.NoError(t, err)
			assert.Equal(t, "xoxd-test-cookie", cookie.Value)
			_, _ = fmt.Fprint(w, `{"teams":{"T1":{"token":"xoxc-test-auth-token"}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	api := newSlackLoginAPIWithClient(&http.Client{Jar: jar}, server.URL+"/", server.URL+"/")
	token, cookieToken, err := api.LoginWorkspace(context.Background(), slackLoginWorkspace{
		ID:             "T1",
		MagicLoginCode: "magic-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "xoxc-test-auth-token", token)
	assert.Equal(t, "xoxd-test-cookie", cookieToken)
	assert.Equal(t, []string{"/api/auth.loginMagicBulk", "/auth"}, paths)
}

func TestSlackLoginAPIWorkspaceClientURLUsesAppHostAndTeamID(t *testing.T) {
	api := newSlackLoginAPIWithClient(
		http.DefaultClient,
		"https://slack.com/",
		"https://app.slack.com/",
	)

	assert.Equal(t,
		"https://app.slack.com/client/T123",
		api.workspaceClientURL(slackLoginWorkspace{
			ID:  "T123",
			URL: "https://workspace.slack.com/",
		}),
	)
}

func TestSlackLoginAPIFetchAuthTokenDoesNotUseAnotherWorkspace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "T1", r.URL.Query().Get("teams"))
		_, _ = fmt.Fprint(w, `{"teams":{"T2":{"token":"xoxc-wrong-workspace"}}}`)
	}))
	defer server.Close()

	api := newSlackLoginAPIWithClient(server.Client(), server.URL+"/", server.URL+"/")
	_, _, err := api.fetchAuthToken(context.Background(), slackLoginWorkspace{ID: "T1"}, server.URL+"/client/T1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "selected workspace")
}

func TestSlackLoginAPIMagicLoginCanUseTokenResult(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.SetCookie(w, &http.Cookie{Name: "d", Value: "xoxd-test-cookie", Path: "/"})
		_, _ = fmt.Fprint(w, `{
			"ok": true,
			"token_results": {
				"magic-1": {
					"ok": true,
					"token": "xoxc-direct-token"
				}
			}
		}`)
	}))
	defer server.Close()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	api := newSlackLoginAPIWithClient(&http.Client{Jar: jar}, server.URL+"/", server.URL+"/")
	token, cookieToken, err := api.LoginWorkspace(context.Background(), slackLoginWorkspace{
		ID:             "T1",
		MagicLoginCode: "magic-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "xoxc-direct-token", token)
	assert.Equal(t, "xoxd-test-cookie", cookieToken)
	assert.Equal(t, 1, requests)
}

func TestSlackLoginAPIMagicLoginURLFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "d", Value: "xoxd-fallback-cookie", Path: "/"})
		_, _ = fmt.Fprint(w, `<html><script>window.boot = {"api_token":"xoxc-fallback-token"}</script></html>`)
	}))
	defer server.Close()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	api := newSlackLoginAPIWithClient(&http.Client{Jar: jar}, server.URL+"/", server.URL+"/")
	token, cookieToken, err := api.LoginWorkspace(context.Background(), slackLoginWorkspace{
		ID:            "T1",
		MagicLoginURL: server.URL + "/magic",
	})
	require.NoError(t, err)
	assert.Equal(t, "xoxc-fallback-token", token)
	assert.Equal(t, "xoxd-fallback-cookie", cookieToken)
}

func TestSlackLoginAPIMagicLoginReturnsStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":false,"error":"invalid_magic"}`)
	}))
	defer server.Close()

	api := newSlackLoginAPIWithClient(server.Client(), server.URL+"/", server.URL+"/")
	_, _, err := api.LoginWorkspace(context.Background(), slackLoginWorkspace{
		ID:             "T1",
		MagicLoginCode: "magic-1",
	})
	var apiErr *slackLoginAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "auth.loginMagicBulk", apiErr.Method)
	assert.Equal(t, "invalid_magic", apiErr.Code)
}

func TestSlackFindWorkspacesFlattenDeduplicates(t *testing.T) {
	response := slackFindWorkspacesResponse{
		CurrentTeams: []slackWorkspaceGroup{{Teams: []slackLoginWorkspace{
			{ID: "T1", MagicLoginCode: "one"},
			{ID: "T1", MagicLoginCode: "duplicate"},
			{ID: "T2"},
			{ID: "T3", MagicLoginCode: "sso", SSORequired: true},
			{ID: "T4", MagicLoginCode: "two-factor", TwoFactorRequired: true},
		}}},
		CurrentOrgs: []slackWorkspaceOrg{{Org: &slackLoginWorkspace{ID: "E1", MagicLoginURL: "https://example.invalid"}}},
	}
	workspaces := response.flatten()
	ids := []string{workspaces[0].ID, workspaces[1].ID, workspaces[2].ID}
	slices.Sort(ids)
	assert.Equal(t, []string{"E1", "T1", "T4"}, ids)
}

func TestSlackLoginAPIRateLimitWithoutJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer server.Close()

	api := newSlackLoginAPIWithClient(server.Client(), server.URL+"/", server.URL+"/")
	_, err := api.RequestCode(context.Background(), "person@example.com")
	var apiErr *slackLoginAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "ratelimited", apiErr.Code)
	assert.Equal(t, http.StatusTooManyRequests, apiErr.HTTPStatus)
}

func TestSlackLoginAPIReturnsStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":false,"error":"invalid_code"}`)
	}))
	defer server.Close()

	api := newSlackLoginAPIWithClient(server.Client(), server.URL+"/", server.URL+"/")
	_, err := api.ConfirmCode(context.Background(), "person@example.com", "ABC123")
	var apiErr *slackLoginAPIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, "invalid_code", apiErr.Code)
	assert.Equal(t, "signin.confirmCode", apiErr.Method)
}
