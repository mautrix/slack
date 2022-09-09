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
	"net/url"

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
		cmdLoginPassword,
		cmdLoginToken,
		cmdLogout,
		cmdSyncTeams,
		cmdDeletePortal,
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

var cmdLoginPassword = &commands.FullHandler{
	Func: wrapCommand(fnLoginPassword),
	Name: "login-password",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Link the bridge to a Slack account (legacy password login)",
		Args:        "<email> <domain> <password>",
	},
}

func fnLoginPassword(ce *WrappedCommandEvent) {
	if len(ce.Args) != 3 {
		ce.Reply("**Usage**: $cmdprefix login-password <email> <domain> <password>")
		return
	}

	if ce.User.IsLoggedInTeam(ce.Args[0], ce.Args[1]) {
		ce.Reply("%s is already logged in to team %s", ce.Args[0], ce.Args[1])
		return
	}

	err := ce.User.LoginTeam(ce.Args[0], ce.Args[1], ce.Args[2])
	if err != nil {
		ce.Reply("Failed to log in as %s for team %s: %v", ce.Args[0], ce.Args[1], err)
		return
	}

	ce.Reply("Successfully logged into %s for team %s", ce.Args[0], ce.Args[1])
	ce.Reply("Note: with legacy password login, your conversations will only be bridged once messages arrive in them through Slack. Use the `login-token` command if you want your joined conversations to be immediately bridged (you don't need to logout first).")
}

var cmdLoginToken = &commands.FullHandler{
	Func: wrapCommand(fnLoginToken),
	Name: "login-token",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Link the bridge to a Slack account",
		Args:        "<token> <cookieToken>",
	},
}

func fnLoginToken(ce *WrappedCommandEvent) {
	if len(ce.Args) != 2 {
		ce.Reply("**Usage**: $cmdprefix login-token <token> <cookieToken>")
		return
	}

	cookieToken, _ := url.PathUnescape(ce.Args[1])
	info, err := ce.User.TokenLogin(ce.Args[0], cookieToken)
	if err != nil {
		ce.Reply("Failed to log in with token: %v", err)
	} else {
		ce.Reply("Successfully logged into %s for team %s", info.UserEmail, info.TeamName)
	}
}

var cmdLogout = &commands.FullHandler{
	Func: wrapCommand(fnLogout),
	Name: "logout",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Unlink the bridge from your Slack account.",
		Args:        "<email> <domain>",
	},
	RequiresLogin: true,
}

func fnLogout(ce *WrappedCommandEvent) {
	if len(ce.Args) != 2 {
		ce.Reply("**Usage**: $cmdprefix logout <email> <domain>")

		return
	}
	userTeam := ce.User.bridge.DB.UserTeam.GetBySlackTeam(ce.User.MXID, ce.Args[0], ce.Args[1])

	err := ce.User.LogoutUserTeam(userTeam)
	if err != nil {
		ce.Reply("Error logging out: %v", err)
	} else {
		ce.Reply("Logged out successfully.")
	}
}

var cmdSyncTeams = &commands.FullHandler{
	Func: wrapCommand(fnSyncTeams),
	Name: "sync-teams",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionGeneral,
		Description: "Synchronize team information and channels from Slack into Matrix",
	},
	RequiresLogin: true,
}

func fnSyncTeams(ce *WrappedCommandEvent) {
	for _, team := range ce.User.Teams {
		ce.User.UpdateTeam(team, true)
	}
	ce.Reply("Done syncing teams.")
}

var cmdDeletePortal = &commands.FullHandler{
	Func:           wrapCommand(fnDeletePortal),
	Name:           "delete-portal",
	RequiresPortal: true,
}

func fnDeletePortal(ce *WrappedCommandEvent) {
	ce.Portal.delete()
	ce.Portal.cleanup(false)
	ce.Log.Infofln("Deleted portal")
}
