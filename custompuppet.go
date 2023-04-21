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
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

var (
	ErrNoCustomMXID    = errors.New("no custom mxid set")
	ErrMismatchingMXID = errors.New("whoami result does not match custom mxid")
)

// /////////////////////////////////////////////////////////////////////////////
// additional bridge api
// /////////////////////////////////////////////////////////////////////////////
func (br *SlackBridge) newDoublePuppetClient(mxid id.UserID, accessToken string) (*mautrix.Client, error) {
	_, homeserver, err := mxid.Parse()
	if err != nil {
		return nil, err
	}

	homeserverURL, found := br.Config.Bridge.DoublePuppetServerMap[homeserver]
	if !found {
		if homeserver == br.AS.HomeserverDomain {
			homeserverURL = ""
		} else if br.Config.Bridge.DoublePuppetAllowDiscovery {
			resp, err := mautrix.DiscoverClientAPI(homeserver)
			if err != nil {
				return nil, fmt.Errorf("failed to find homeserver URL for %s: %v", homeserver, err)
			}

			homeserverURL = resp.Homeserver.BaseURL
			br.Log.Debugfln("Discovered URL %s for %s to enable double puppeting for %s", homeserverURL, homeserver, mxid)
		} else {
			return nil, fmt.Errorf("double puppeting from %s is not allowed", homeserver)
		}
	}

	return br.AS.NewExternalMautrixClient(mxid, accessToken, homeserverURL)
}

// /////////////////////////////////////////////////////////////////////////////
// mautrix.Syncer implementation
// /////////////////////////////////////////////////////////////////////////////
func (puppet *Puppet) GetFilterJSON(_ id.UserID) *mautrix.Filter {
	everything := []event.Type{{Type: "*"}}
	return &mautrix.Filter{
		Presence: mautrix.FilterPart{
			Senders: []id.UserID{puppet.CustomMXID},
			Types:   []event.Type{event.EphemeralEventPresence},
		},
		AccountData: mautrix.FilterPart{NotTypes: everything},
		Room: mautrix.RoomFilter{
			Ephemeral:    mautrix.FilterPart{Types: []event.Type{event.EphemeralEventTyping, event.EphemeralEventReceipt}},
			IncludeLeave: false,
			AccountData:  mautrix.FilterPart{NotTypes: everything},
			State:        mautrix.FilterPart{NotTypes: everything},
			Timeline:     mautrix.FilterPart{NotTypes: everything},
		},
	}
}

func (puppet *Puppet) OnFailedSync(_ *mautrix.RespSync, err error) (time.Duration, error) {
	puppet.log.Warnln("Sync error:", err)
	if errors.Is(err, mautrix.MUnknownToken) {
		if !puppet.tryRelogin(err, "syncing") {
			return 0, err
		}

		puppet.customIntent.AccessToken = puppet.AccessToken

		return 0, nil
	}

	return 10 * time.Second, nil
}

func (puppet *Puppet) ProcessResponse(resp *mautrix.RespSync, _ string) error {
	if !puppet.customUser.IsLoggedIn() {
		puppet.log.Debugln("Skipping sync processing: custom user not connected to slack")

		return nil
	}

	// for roomID, events := range resp.Rooms.Join {
	// 	for _, evt := range events.Ephemeral.Events {
	// 		evt.RoomID = roomID
	// 		err := evt.Content.ParseRaw(evt.Type)
	// 		if err != nil {
	// 			continue
	// 		}

	// 		switch evt.Type {
	// 		case event.EphemeralEventReceipt:
	// 			if puppet.EnableReceipts {
	// 				go puppet.bridge.MatrixHandler.HandleReceipt(evt)
	// 			}
	// 		case event.EphemeralEventTyping:
	// 			go puppet.bridge.MatrixHandler.HandleTyping(evt)
	// 		}
	// 	}
	// }

	// if puppet.EnablePresence {
	// 	for _, evt := range resp.Presence.Events {
	// 		if evt.Sender != puppet.CustomMXID {
	// 			continue
	// 		}

	// 		err := evt.Content.ParseRaw(evt.Type)
	// 		if err != nil {
	// 			continue
	// 		}

	// 		go puppet.bridge.MatrixHandler.HandlePresence(evt)
	// 	}
	// }

	return nil
}

// /////////////////////////////////////////////////////////////////////////////
// mautrix.Storer implementation
// /////////////////////////////////////////////////////////////////////////////
func (puppet *Puppet) SaveFilterID(_ id.UserID, _ string) {
}

func (puppet *Puppet) SaveNextBatch(_ id.UserID, nbt string) {
	puppet.NextBatch = nbt
	puppet.Update()
}

func (puppet *Puppet) SaveRoom(_ *mautrix.Room) {
}

func (puppet *Puppet) LoadFilterID(_ id.UserID) string {
	return ""
}

func (puppet *Puppet) LoadNextBatch(_ id.UserID) string {
	return puppet.NextBatch
}

func (puppet *Puppet) LoadRoom(_ id.RoomID) *mautrix.Room {
	return nil
}

// /////////////////////////////////////////////////////////////////////////////
// additional puppet api
// /////////////////////////////////////////////////////////////////////////////
func (puppet *Puppet) clearCustomMXID() {
	puppet.CustomMXID = ""
	puppet.AccessToken = ""
	puppet.customIntent = nil
	puppet.customUser = nil
}

func (puppet *Puppet) newCustomIntent() (*appservice.IntentAPI, error) {
	if puppet.CustomMXID == "" {
		return nil, ErrNoCustomMXID
	}

	client, err := puppet.bridge.newDoublePuppetClient(puppet.CustomMXID, puppet.AccessToken)
	if err != nil {
		return nil, err
	}

	client.Syncer = puppet
	client.Store = puppet

	ia := puppet.bridge.AS.NewIntentAPI("custom")
	ia.Client = client
	ia.Localpart, _, _ = puppet.CustomMXID.Parse()
	ia.UserID = puppet.CustomMXID
	ia.IsCustomPuppet = true

	return ia, nil
}

func (puppet *Puppet) StartCustomMXID(reloginOnFail bool) error {
	if puppet.CustomMXID == "" {
		puppet.clearCustomMXID()

		return nil
	}

	intent, err := puppet.newCustomIntent()
	if err != nil {
		puppet.clearCustomMXID()

		return err
	}

	resp, err := intent.Whoami()
	if err != nil {
		if !reloginOnFail || (errors.Is(err, mautrix.MUnknownToken) && !puppet.tryRelogin(err, "initializing double puppeting")) {
			puppet.clearCustomMXID()

			return err
		}

		intent.AccessToken = puppet.AccessToken
	} else if resp.UserID != puppet.CustomMXID {
		puppet.clearCustomMXID()

		return ErrMismatchingMXID
	}

	puppet.customIntent = intent
	puppet.customUser = puppet.bridge.GetUserByMXID(puppet.CustomMXID)
	puppet.startSyncing()

	return nil
}

func (puppet *Puppet) tryRelogin(cause error, action string) bool {
	if !puppet.bridge.Config.CanAutoDoublePuppet(puppet.CustomMXID) {
		return false
	}

	puppet.log.Debugfln("Trying to relogin after '%v' while %s", cause, action)

	accessToken, err := puppet.loginWithSharedSecret(puppet.CustomMXID, puppet.TeamID)
	if err != nil {
		puppet.log.Errorfln("Failed to relogin after '%v' while %s: %v", cause, action, err)

		return false
	}

	puppet.log.Infofln("Successfully relogined after '%v' while %s", cause, action)
	puppet.AccessToken = accessToken

	return true
}

func (puppet *Puppet) startSyncing() {
	if !puppet.bridge.Config.Bridge.SyncWithCustomPuppets {
		return
	}

	go func() {
		puppet.log.Debugln("Starting syncing...")
		puppet.customIntent.SyncPresence = "offline"

		err := puppet.customIntent.Sync()
		if err != nil {
			puppet.log.Errorln("Fatal error syncing:", err)
		}
	}()
}

func (puppet *Puppet) stopSyncing() {
	if !puppet.bridge.Config.Bridge.SyncWithCustomPuppets {
		return
	}

	puppet.customIntent.StopSync()
}

func (puppet *Puppet) loginWithSharedSecret(mxid id.UserID, teamID string) (string, error) {
	_, homeserver, _ := mxid.Parse()

	puppet.log.Debugfln("Logging into %s with shared secret", mxid)

	loginSecret := puppet.bridge.Config.Bridge.LoginSharedSecretMap[homeserver]

	client, err := puppet.bridge.newDoublePuppetClient(mxid, "")
	if err != nil {
		return "", fmt.Errorf("failed to create mautrix client to log in: %v", err)
	}

	req := mautrix.ReqLogin{
		Identifier:               mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: string(mxid)},
		DeviceID:                 id.DeviceID(fmt.Sprintf("Slack Bridge %s", teamID)),
		InitialDeviceDisplayName: "Slack Bridge",
	}
	if loginSecret == "appservice" {
		client.AccessToken = puppet.bridge.AS.Registration.AppToken
		req.Type = mautrix.AuthTypeAppservice
	} else {
		mac := hmac.New(sha512.New, []byte(loginSecret))
		mac.Write([]byte(mxid))
		req.Password = hex.EncodeToString(mac.Sum(nil))
		req.Type = mautrix.AuthTypePassword
	}
	resp, err := client.Login(&req)
	if err != nil {
		return "", err
	}

	return resp.AccessToken, nil
}

func (puppet *Puppet) SwitchCustomMXID(accessToken string, mxid id.UserID) error {
	prevCustomMXID := puppet.CustomMXID
	if puppet.customIntent != nil {
		puppet.stopSyncing()
	}

	puppet.CustomMXID = mxid
	puppet.AccessToken = accessToken

	err := puppet.StartCustomMXID(false)
	if err != nil {
		return err
	}

	if prevCustomMXID != "" {
		delete(puppet.bridge.puppetsByCustomMXID, prevCustomMXID)
	}

	if puppet.CustomMXID != "" {
		puppet.bridge.puppetsByCustomMXID[puppet.CustomMXID] = puppet
	}

	puppet.EnablePresence = puppet.bridge.Config.Bridge.DefaultBridgePresence
	puppet.EnableReceipts = puppet.bridge.Config.Bridge.DefaultBridgeReceipts

	puppet.bridge.AS.StateStore.MarkRegistered(puppet.CustomMXID)

	puppet.Update()

	// TODO leave rooms with default puppet

	return nil
}
