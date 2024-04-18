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

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/rs/zerolog/hlog"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/id"
)

type legacyResponse struct {
	Success bool   `json:"success"`
	Status  string `json:"status"`
}

type legacyError struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	ErrCode string `json:"errcode"`
}

func (p *ProvisioningAPI) legacyPing(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)

	puppets := []any{}
	p.bridge.userAndTeamLock.Lock()
	for _, ut := range user.teams {
		puppets = append(puppets, map[string]interface{}{
			"puppetId":   fmt.Sprintf("%s-%s", ut.UserTeamKey.TeamID, ut.UserTeamKey.UserID),
			"puppetMxid": user.MXID,
			"userId":     ut.UserID,
			"data": map[string]interface{}{
				"team": map[string]string{
					"id":   ut.TeamID,
					"name": ut.Team.Name,
				},
				"self": map[string]string{
					"id":   ut.UserID,
					"name": ut.Email,
				},
			},
		})
	}
	p.bridge.userAndTeamLock.Unlock()

	resp := map[string]any{
		"puppets":         puppets,
		"management_room": user.ManagementRoom,
		"mxid":            user.MXID,
	}
	jsonResponse(w, http.StatusOK, resp)
}

func (p *ProvisioningAPI) legacyLogout(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	user := p.bridge.GetUserByMXID(id.UserID(userID))

	slackTeamID := strings.Split(r.URL.Query().Get("slack_team_id"), "-")[0] // in case some client sends userTeam instead of team ID

	userTeam := user.GetTeam(slackTeamID)
	if userTeam == nil || userTeam.Token == "" {
		jsonResponse(w, http.StatusNotFound, legacyError{
			Error:   "Not logged in",
			ErrCode: "Not logged in",
		})

		return
	}

	userTeam.Logout(r.Context(), status.BridgeState{StateEvent: status.StateLoggedOut})
	jsonResponse(w, http.StatusOK, legacyResponse{true, "Logged out successfully."})
}

func (p *ProvisioningAPI) legacyLogin(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	user := p.bridge.GetUserByMXID(id.UserID(userID))

	var data struct {
		Token       string
		Cookietoken string
	}

	err := json.NewDecoder(r.Body).Decode(&data)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, legacyError{
			Error:   "Invalid JSON",
			ErrCode: "Invalid JSON",
		})
		return
	}

	if data.Token == "" {
		jsonResponse(w, http.StatusBadRequest, legacyError{
			Error:   "Missing field token",
			ErrCode: "Missing field token",
		})
		return
	}

	if data.Cookietoken == "" {
		jsonResponse(w, http.StatusBadRequest, legacyError{
			Error:   "Missing field cookietoken",
			ErrCode: "Missing field cookietoken",
		})
		return
	}

	cookieToken, _ := url.PathUnescape(data.Cookietoken)
	info, err := user.TokenLogin(r.Context(), data.Token, cookieToken)
	if err != nil {
		hlog.FromRequest(r).Err(err).Msg("Failed to do token login")
		jsonResponse(w, http.StatusNotAcceptable, legacyError{
			Error:   fmt.Sprintf("Slack login error: %s", err),
			ErrCode: err.Error(),
		})

		return
	}

	jsonResponse(w, http.StatusCreated,
		map[string]interface{}{
			"success": true,
			"teamid":  info.TeamID,
			"userid":  info.UserID,
		})
}
