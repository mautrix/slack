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
		Name:        "Auth token & cookie",
		Description: "Log in with an auth token (and a cookie, if the token is from a browser)",
		ID:          LoginFlowIDAuthToken,
	}, {
		Name:        "Slack app",
		Description: "Log in with a Slack app",
		ID:          LoginFlowIDApp,
	}}
}

func (s *SlackConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	switch flowID {
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

var _ bridgev2.LoginProcessCookies = (*SlackTokenLogin)(nil)

// Picks the xoxc token from localStorage.localConfig_v2. Upstream used
// `Object.values(teams)[0].token`, which silently picks the wrong team when
// the webview has more than one workspace cached (e.g. after sign-in/-out
// cycles). That stale token then fails `client.boot` with invalid_auth.
//
// We only resolve when we have a definitive signal identifying the team the
// user just signed into:
//   1. A team id appeared in localConfig_v2 that wasn't there when the
//      webview first rendered this script (the "added since baseline" diff).
//   2. The webview has navigated to app.slack.com/client/TXXXXX — the team
//      id is in the URL.
//   3. The webview is on a concrete <team>.slack.com subdomain and one of
//      the cached teams' urls matches the current host.
//
// There is no timeout fallback. If none of those fire, the user hasn't
// actually finished signing in yet — waiting is correct.
const ExtractSlackTokenJS = `
new Promise(resolve => {
	let useSlackInBrowserClicked = false
	let mautrixSlackTokenBaseline = null // Set<teamId> present when script first saw localStorage

	function mautrixReadTeams() {
		try {
			const raw = localStorage.localConfig_v2
			if (!raw) return []
			const cfg = JSON.parse(raw)
			return cfg && cfg.teams ? Object.values(cfg.teams) : []
		} catch (e) {
			return []
		}
	}

	function mautrixPickTeam(teams) {
		if (!teams || teams.length === 0) return null
		const host = window.location.host || ""
		const clientMatch = window.location.pathname.match(/\/client\/(T[A-Z0-9]+)/)
		const teamIdFromUrl = clientMatch ? clientMatch[1] : null

		// Strongest signal: URL names a specific team id.
		if (teamIdFromUrl) {
			const byId = teams.find(t => t && t.id === teamIdFromUrl)
			if (byId) return byId
		}
		// Strong signal: workspace subdomain.
		if (host && host !== "slack.com" && host !== "app.slack.com") {
			const byHost = teams.find(t => t && t.url && String(t.url).indexOf(host) !== -1)
			if (byHost) return byHost
		}
		// Diff signal: a team id appeared that wasn't in baseline — that's
		// the workspace the user just signed into in this webview session.
		if (mautrixSlackTokenBaseline) {
			const added = teams.find(t => t && t.id && !mautrixSlackTokenBaseline.has(t.id))
			if (added) return added
		}
		return null
	}

	function mautrixFindSlackToken() {
		// Auto-click the "Use Slack in Browser" button when Slack shows it.
		if (/\.slack\.com$/.test(window.location.host)) {
			const link = document?.querySelector?.(".p-ssb_redirect__body")?.querySelector?.(".c-link")
			if (link && !useSlackInBrowserClicked) {
				location.href = link.getAttribute("href")
				useSlackInBrowserClicked = true
			}
		}

		// Snapshot baseline on first tick, regardless of whether any team is
		// cached yet. Any team id seen later is then definitionally "new".
		if (mautrixSlackTokenBaseline === null) {
			const baselineTeams = mautrixReadTeams()
			mautrixSlackTokenBaseline = new Set(
				baselineTeams.map(t => t && t.id).filter(Boolean),
			)
		}

		if (!localStorage.localConfig_v2?.includes("xoxc-")) {
			return
		}
		const teams = mautrixReadTeams()
		const picked = mautrixPickTeam(teams)
		if (!picked || !picked.token) return

		window.clearInterval(mautrixSlackTokenCheckInterval)
		resolve({ auth_token: picked.token })
	}
	const mautrixSlackTokenCheckInterval = window.setInterval(mautrixFindSlackToken, 1000)
	// Run once immediately so we snapshot baseline before any login activity.
	mautrixFindSlackToken()
})
`

func (s *SlackTokenLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeCookies,
		StepID:       LoginStepIDAuthToken,
		Instructions: "Enter a JSON object with your auth token and cookie token, or a cURL command copied from browser devtools.\n\nFor example: `{\"auth_token\":\"xoxc-...\",\"cookie_token\":\"xoxd-...\"}`",
		CookiesParams: &bridgev2.LoginCookiesParams{
			URL:       "https://slack.com/signin",
			UserAgent: "",
			Fields: []bridgev2.LoginCookieField{{
				ID:       "auth_token",
				Required: true,
				Sources: []bridgev2.LoginCookieFieldSource{{
					Type: bridgev2.LoginCookieTypeSpecial,
					Name: "fi.mau.slack.auth_token",
				}, {
					Type:            bridgev2.LoginCookieTypeRequestBody,
					Name:            "token",
					RequestURLRegex: `^https://.+?\.slack\.com/api/(client|experiments|api|users|teams|conversations)\..+$`,
				}},
				Pattern: `^xoxc-.+$`,
			}, {
				ID:       "cookie_token",
				Required: true,
				Sources: []bridgev2.LoginCookieFieldSource{{
					Type:         bridgev2.LoginCookieTypeCookie,
					Name:         "d",
					CookieDomain: "slack.com",
				}},
				Pattern: `^xoxd-[a-zA-Z0-9/+=]+$`,
			}},
			ExtractJS: ExtractSlackTokenJS,
		},
	}, nil
}

func (s *SlackTokenLogin) Cancel() {}

func (s *SlackTokenLogin) SubmitCookies(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	token, cookieToken := input["auth_token"], input["cookie_token"]
	client := makeSlackClient(&s.User.Log, token, cookieToken, "")
	err := client.FetchVersionData(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to fetch version data")
		return nil, err
	}
	info, err := client.ClientUserBootContext(ctx, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("client.boot failed: %w", err)
	}
	ul, err := s.User.NewLogin(ctx, &database.UserLogin{
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
