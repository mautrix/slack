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
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/beeper/libserv/pkg/requestlog"
	"github.com/gorilla/mux"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/id"
)

type ProvisioningAPI struct {
	bridge *SlackBridge
	log    zerolog.Logger
}

func newProvisioningAPI(br *SlackBridge) *ProvisioningAPI {
	prov := &ProvisioningAPI{
		bridge: br,
		log:    br.ZLog.With().Str("component", "provisioning").Logger(),
	}

	prov.log.Debug().Str("base_path", prov.bridge.Config.Bridge.Provisioning.Prefix).Msg("Enabling provisioning API")
	r := br.AS.Router.PathPrefix(br.Config.Bridge.Provisioning.Prefix).Subrouter()

	r.Use(hlog.NewHandler(prov.log))
	r.Use(requestlog.AccessLogger(true))
	r.Use(prov.authMiddleware)

	r.HandleFunc("/v1/ping", prov.legacyPing).Methods(http.MethodGet)
	r.HandleFunc("/v1/login", prov.legacyLogin).Methods(http.MethodPost)
	r.HandleFunc("/v1/logout", prov.legacyLogout).Methods(http.MethodPost)

	r.HandleFunc("/v2/ping", prov.ping).Methods(http.MethodGet)
	r.HandleFunc("/v2/login", prov.login).Methods(http.MethodPost)
	r.HandleFunc("/v2/logout/{teamID}", prov.logout).Methods(http.MethodPost)

	return prov
}

func jsonResponse(w http.ResponseWriter, status int, response any) {
	w.Header().Add("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(response)
}

func (p *ProvisioningAPI) authMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if auth != p.bridge.Config.Bridge.Provisioning.SharedSecret {
			jsonResponse(w, http.StatusForbidden, map[string]interface{}{
				"error":   "Invalid auth token",
				"errcode": "M_FORBIDDEN",
			})

			return
		}

		userID := r.URL.Query().Get("user_id")
		user := p.bridge.GetUserByMXID(id.UserID(userID))
		if user != nil {
			hlog.FromRequest(r).UpdateContext(func(c zerolog.Context) zerolog.Context {
				return c.Stringer("user_id", user.MXID)
			})
		}

		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), "user", user)))
	})
}

type RespPingSlackTeam struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Subdomain string        `json:"subdomain"`
	SpaceMXID id.RoomID     `json:"space_mxid"`
	AvatarURL id.ContentURI `json:"avatar_url"`
}

type RespPingSlackUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type RespPingSlackEntry struct {
	Team RespPingSlackTeam `json:"team"`
	User RespPingSlackUser `json:"user"`

	Connected bool `json:"connected"`
	LoggedIn  bool `json:"logged_in"`
}

type RespPing struct {
	MXID           id.UserID            `json:"mxid"`
	ManagementRoom id.RoomID            `json:"management_room"`
	SpaceRoom      id.RoomID            `json:"space_room"`
	Admin          bool                 `json:"admin"`
	Whitelisted    bool                 `json:"whitelisted"`
	SlackTeams     []RespPingSlackEntry `json:"slack_teams"`
}

func (p *ProvisioningAPI) ping(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)

	p.bridge.userAndTeamLock.Lock()
	teams := make([]RespPingSlackEntry, 0, len(user.teams))
	for _, ut := range user.teams {
		if ut.Token == "" {
			return
		}
		teams = append(teams, RespPingSlackEntry{
			Team: RespPingSlackTeam{
				ID:        ut.Team.ID,
				Name:      ut.Team.Name,
				Subdomain: ut.Team.Domain,
				SpaceMXID: ut.Team.MXID,
				AvatarURL: ut.Team.AvatarMXC,
			},
			User: RespPingSlackUser{
				ID:    ut.UserID,
				Email: ut.Email,
			},
			Connected: ut.RTM != nil,
			LoggedIn:  ut.Token != "",
		})
	}
	p.bridge.userAndTeamLock.Unlock()

	jsonResponse(w, http.StatusOK, &RespPing{
		MXID:           user.MXID,
		ManagementRoom: user.ManagementRoom,
		SpaceRoom:      user.SpaceRoom,
		Admin:          user.PermissionLevel >= bridgeconfig.PermissionLevelAdmin,
		Whitelisted:    user.PermissionLevel >= bridgeconfig.PermissionLevelUser,
		SlackTeams:     teams,
	})
}

type ReqLogin struct {
	Token       string `json:"token"`
	CookieToken string `json:"cookie_token"`
}

type RespLogin struct {
	TeamID    string `json:"team_id"`
	TeamName  string `json:"team_name"`
	UserID    string `json:"user_id"`
	UserEmail string `json:"user_email"`
}

func (p *ProvisioningAPI) login(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)

	var req ReqLogin
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, mautrix.RespError{
			Err:     "Invalid request body",
			ErrCode: "M_NOT_JSON",
		})
		return
	} else if req.Token == "" {
		jsonResponse(w, http.StatusBadRequest, mautrix.RespError{
			Err:     "Missing `token` field in request body",
			ErrCode: "M_BAD_JSON",
		})
		return
	}

	authInfo, err := user.TokenLogin(r.Context(), req.Token, req.CookieToken)
	if err != nil {
		hlog.FromRequest(r).Err(err).Msg("Failed to do token login")
		// TODO proper error messages for known types
		jsonResponse(w, http.StatusInternalServerError, &mautrix.RespError{
			Err:     "Failed to log in",
			ErrCode: "M_UNKNOWN",
		})
		return
	}
	resp := &RespLogin{
		TeamID:    authInfo.TeamID,
		TeamName:  authInfo.TeamName,
		UserID:    authInfo.UserID,
		UserEmail: authInfo.UserEmail,
	}
	hlog.FromRequest(r).Info().Any("auth_info", resp).Msg("Token login successful")
	jsonResponse(w, http.StatusOK, resp)
}

func (p *ProvisioningAPI) logout(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)

	userTeam := user.GetTeam(strings.ToUpper(mux.Vars(r)["team_id"]))
	if userTeam == nil || userTeam.Token == "" {
		jsonResponse(w, http.StatusNotFound, &mautrix.RespError{
			Err:     "Not logged into that team",
			ErrCode: "M_NOT_FOUND",
		})
		return
	}

	userTeam.Logout(r.Context(), status.BridgeState{StateEvent: status.StateLoggedOut})
	jsonResponse(w, http.StatusOK, struct{}{})
}
