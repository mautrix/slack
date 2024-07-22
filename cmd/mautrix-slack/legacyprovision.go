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
	"net/http"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-slack/pkg/connector"
	"go.mau.fi/mautrix-slack/pkg/slackid"
)

func jsonResponse(w http.ResponseWriter, status int, response any) {
	w.Header().Add("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

type Error struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	ErrCode string `json:"errcode"`
}

type Response struct {
	Success bool   `json:"success"`
	Status  string `json:"status"`
}

func legacyProvPing(w http.ResponseWriter, r *http.Request) {
	user := m.Matrix.Provisioning.GetUser(r)
	puppets := []any{}
	for _, login := range user.GetCachedUserLogins() {
		teamID, userID := slackid.ParseUserLoginID(login.ID)
		client, _ := login.Client.(*connector.SlackClient)
		var teamName string
		if client != nil && client.BootResp != nil {
			teamName = client.BootResp.Team.Name
		}

		puppets = append(puppets, map[string]any{
			"puppetId":   string(login.ID),
			"puppetMxid": user.MXID,
			"userId":     userID,
			"data": map[string]any{
				"team": map[string]any{
					"id":   teamID,
					"name": teamName,
				},
				"self": map[string]any{
					"id":   string(login.ID),
					"name": login.Metadata.(*connector.UserLoginMetadata).Email,
				},
			},
		})
	}

	resp := map[string]any{
		"puppets":         puppets,
		"management_room": user.ManagementRoom,
		"mxid":            user.MXID,
	}
	jsonResponse(w, http.StatusOK, resp)
}

func legacyProvLogin(w http.ResponseWriter, r *http.Request) {
	user := m.Matrix.Provisioning.GetUser(r)
	var data struct {
		Token       string
		Cookietoken string
	}

	err := json.NewDecoder(r.Body).Decode(&data)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "Invalid JSON",
			ErrCode: "Invalid JSON",
		})
		return
	}

	if data.Token == "" {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "Missing field token",
			ErrCode: "Missing field token",
		})
		return
	}

	if data.Cookietoken == "" {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "Missing field cookietoken",
			ErrCode: "Missing field cookietoken",
		})
		return
	}

	login, err := m.Bridge.Network.CreateLogin(r.Context(), user, connector.LoginFlowIDAuthToken)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   "Failed to create login",
			ErrCode: "M_UNKNOWN",
		})
		return
	}
	nextStep, err := login.Start(r.Context())
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   "Failed to start login",
			ErrCode: "M_UNKNOWN",
		})
		return
	} else if nextStep.StepID != connector.LoginStepIDAuthToken {
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   "Unexpected login step",
			ErrCode: "M_UNKNOWN",
		})
		return
	}
	nextStep, err = login.(bridgev2.LoginProcessCookies).SubmitCookies(r.Context(), map[string]string{
		"auth_token":   data.Token,
		"cookie_token": data.Cookietoken,
	})
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   "Failed to submit cookies",
			ErrCode: "M_UNKNOWN",
		})
		return
	} else if nextStep.StepID != connector.LoginStepIDComplete {
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   "Unexpected login step",
			ErrCode: "M_UNKNOWN",
		})
		return
	}

	teamID, userID := slackid.ParseUserLoginID(nextStep.CompleteParams.UserLogin.ID)
	jsonResponse(w, http.StatusCreated,
		map[string]any{
			"success": true,
			"teamid":  teamID,
			"userid":  userID,
		})
}

func legacyProvLogout(w http.ResponseWriter, r *http.Request) {
	user := m.Matrix.Provisioning.GetUser(r)
	loginID := r.URL.Query().Get("slack_team_id")
	if !strings.ContainsRune(loginID, '-') {
		loginIDPrefix := loginID + "-"
		loginID = ""
		for _, login := range user.GetUserLoginIDs() {
			if strings.HasPrefix(string(login), loginIDPrefix) {
				loginID = string(login)
				break
			}
		}
	}
	if loginID == "" {
		jsonResponse(w, http.StatusNotFound, Error{
			Error:   "Not logged in",
			ErrCode: "Not logged in",
		})
		return
	}
	login, err := m.Bridge.GetExistingUserLoginByID(r.Context(), networkid.UserLoginID(loginID))
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   "Failed to get login",
			ErrCode: "M_UNKNOWN",
		})
		return
	} else if login == nil {
		jsonResponse(w, http.StatusNotFound, Error{
			Error:   "Not logged in",
			ErrCode: "Not logged in",
		})
		return
	}
	login.Logout(r.Context())
	jsonResponse(w, http.StatusOK, Response{true, "Logged out successfully."})
}
