// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
)

const (
	SecWebSocketProtocol = "com.gitlab.beeper.discord"
)

type ProvisioningAPI struct {
	bridge *SlackBridge
	log    log.Logger
}

func newProvisioningAPI(br *SlackBridge) *ProvisioningAPI {
	p := &ProvisioningAPI{
		bridge: br,
		log:    br.Log.Sub("Provisioning"),
	}

	prefix := br.Config.Bridge.Provisioning.Prefix

	p.log.Debugln("Enabling provisioning API at", prefix)

	r := br.AS.Router.PathPrefix(prefix).Subrouter()

	r.Use(p.authMiddleware)

	r.HandleFunc("/ping", p.ping).Methods(http.MethodGet)
	r.HandleFunc("/login", p.login).Methods(http.MethodPost)
	r.HandleFunc("/logout", p.logout).Methods(http.MethodPost)

	return p
}

func jsonResponse(w http.ResponseWriter, status int, response interface{}) {
	w.Header().Add("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(response)
}

// Response structs
type Response struct {
	Success bool   `json:"success"`
	Status  string `json:"status"`
}

type Error struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	ErrCode string `json:"errcode"`
}

// Wrapped http.ResponseWriter to capture the status code
type responseWrap struct {
	http.ResponseWriter
	statusCode int
}

var _ http.Hijacker = (*responseWrap)(nil)

func (rw *responseWrap) WriteHeader(statusCode int) {
	rw.ResponseWriter.WriteHeader(statusCode)
	rw.statusCode = statusCode
}

func (rw *responseWrap) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response does not implement http.Hijacker")
	}
	return hijacker.Hijack()
}

// Middleware
func (p *ProvisioningAPI) authMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")

		// Special case the login endpoint
		if strings.HasPrefix(auth, "Bearer ") {
			auth = auth[len("Bearer "):]
		}

		if auth != p.bridge.Config.Bridge.Provisioning.SharedSecret {
			jsonResponse(w, http.StatusForbidden, map[string]interface{}{
				"error":   "Invalid auth token",
				"errcode": "M_FORBIDDEN",
			})

			return
		}

		userID := r.URL.Query().Get("user_id")
		user := p.bridge.GetUserByMXID(id.UserID(userID))

		start := time.Now()
		wWrap := &responseWrap{w, 200}
		h.ServeHTTP(wWrap, r.WithContext(context.WithValue(r.Context(), "user", user)))
		duration := time.Now().Sub(start).Seconds()

		p.log.Infofln("%s %s from %s took %.2f seconds and returned status %d", r.Method, r.URL.Path, user.MXID, duration, wWrap.statusCode)
	})
}

// websocket upgrader
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
	Subprotocols: []string{SecWebSocketProtocol},
}

// Handlers
func (p *ProvisioningAPI) ping(w http.ResponseWriter, r *http.Request) {
	panic("not implemented")

	/*user := r.Context().Value("user").(*User)

	slackData := map[string]interface{}{
		"logged_in": user.IsLoggedIn(),
		"conn":      nil,
	}

	user.Lock()
	// if user.ID != "" {
	// 	slackData["id"] = user.ID
	// }

	// if user.Client != nil {
	// 	user.Lock()
	// 	discord["conn"] = map[string]interface{}{
	// 		"last_heartbeat_ack":  user.Session.LastHeartbeatAck,
	// 		"last_heartbeat_sent": user.Session.LastHeartbeatSent,
	// 	}
	// 	user.Unlock()
	// }

	resp := map[string]interface{}{
		"slack":           slackData,
		"management_room": user.ManagementRoom,
		"mxid":            user.MXID,
	}

	user.Unlock()

	jsonResponse(w, http.StatusOK, resp)*/
}

func (p *ProvisioningAPI) logout(w http.ResponseWriter, r *http.Request) {
	panic("not implemented")

	/*user := r.Context().Value("user").(*User)
	force := strings.ToLower(r.URL.Query().Get("force")) != "false"

	if !user.IsLoggedIn() {
		jsonResponse(w, http.StatusNotFound, Error{
			Error:   "You're not logged in",
			ErrCode: "not logged in",
		})

		return
	}

	// if user.Client == nil {
	// 	if force {
	// 		jsonResponse(w, http.StatusOK, Response{true, "Logged out successfully."})
	// 	} else {
	// 		jsonResponse(w, http.StatusNotFound, Error{
	// 			Error:   "You're not logged in",
	// 			ErrCode: "not logged in",
	// 		})
	// 	}

	// 	return
	// }

	// err := user.Logout()
	var err error
	if err != nil {
		user.log.Warnln("Error while logging out:", err)

		if !force {
			jsonResponse(w, http.StatusInternalServerError, Error{
				Error:   fmt.Sprintf("Unknown error while logging out: %v", err),
				ErrCode: err.Error(),
			})

			return
		}
	}

	jsonResponse(w, http.StatusOK, Response{true, "Logged out successfully."})*/
}

func (p *ProvisioningAPI) login(w http.ResponseWriter, r *http.Request) {
	panic("not implemented")
	/* 	userID := r.URL.Query().Get("user_id")
	   	user := p.bridge.GetUserByMXID(id.UserID(userID))

	   	r.ParseForm()

	   	token := r.Form.Get("token")
	   	if token == "" {
	   		jsonResponse(w, http.StatusBadRequest, Error{
	   			Error:   "No token specified",
	   			ErrCode: "No token specified",
	   		})

	   		return
	   	}

	   	info, err := user.TokenLogin(token)
	   	if err != nil {
	   		jsonResponse(w, http.StatusNotAcceptable, Error{
	   			Error:   fmt.Sprintf("Failed to login: %s", err),
	   			ErrCode: err.Error(),
	   		})

	   		return
	   	}

	   	jsonResponse(w, http.StatusCreated,
	   		map[string]interface{}{
	   			"success": true,
	   			"teamid":  info.TeamID,
	   			"userid":  info.UserID,
	   		}) */
}
