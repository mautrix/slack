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
	"maunium.net/go/mautrix/bridge/commands"
)

type WrappedCommandEvent struct {
	*commands.Event
	Bridge *SlackBridge
	User   *User
	Portal *Portal
}

func (br *SlackBridge) RegisterCommands() {
	proc := br.CommandProcessor.(*commands.Processor)
	proc.AddHandlers(
		cmdLogin,
		cmdLogout,
		cmdReconnect,
		cmdDisconnect,
	)
}

func wrapCommand(handler func(*WrappedCommandEvent)) func(*commands.Event) {
	return func(ce *commands.Event) {
		user := ce.User.(*User)
		var portal *Portal
		if ce.Portal != nil {
			portal = ce.Portal.(*Portal)
		}
		br := ce.Bridge.Child.(*SlackBridge)
		handler(&WrappedCommandEvent{ce, br, user, portal})
	}
}

var cmdLogin = &commands.FullHandler{
	Func: wrapCommand(fnLogin),
	Name: "login",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Link the bridge to your Slack account",
	},
}

func fnLogin(ce *WrappedCommandEvent) {
	if ce.User.IsLoggedIn() {
		ce.Reply("You're already logged in")
		return
	}
}

var cmdLogout = &commands.FullHandler{
	Func: wrapCommand(fnLogout),
	Name: "logout",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Unlink the bridge from your Slack account.",
	},
	RequiresLogin: true,
}

func fnLogout(ce *WrappedCommandEvent) {
	err := ce.User.Logout()
	if err != nil {
		ce.Reply("Error logging out: %v", err)
	} else {
		ce.Reply("Logged out successfully.")
	}
}

var cmdDisconnect = &commands.FullHandler{
	Func: wrapCommand(fnDisconnect),
	Name: "disconnect",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Disconnect from Slack (without logging out)",
	},
	RequiresLogin: true,
}

func fnDisconnect(ce *WrappedCommandEvent) {
	if !ce.User.Connected() {
		ce.Reply("You're already not connected")
	} else if err := ce.User.Disconnect(); err != nil {
		ce.Reply("Error while disconnecting: %v", err)
	} else {
		ce.Reply("Successfully disconnected")
	}
}

var cmdReconnect = &commands.FullHandler{
	Func:    wrapCommand(fnReconnect),
	Name:    "reconnect",
	Aliases: []string{"connect"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Reconnect to Slack after disconnecting",
	},
	RequiresLogin: true,
}

func fnReconnect(ce *WrappedCommandEvent) {
	if ce.User.Connected() {
		ce.Reply("You're already connected")
	} else if err := ce.User.Connect(); err != nil {
		ce.Reply("Error while reconnecting: %v", err)
	} else {
		ce.Reply("Successfully reconnected")
	}
}
