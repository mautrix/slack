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
	"fmt"
	"net/url"
	"strings"

	"maunium.net/go/mautrix/bridge/commands"
	"maunium.net/go/mautrix/bridge/status"
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
		cmdPing,
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

var cmdPing = &commands.FullHandler{
	Func: wrapCommand(fnPing),
	Name: "ping",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Check which workspaces you're currently signed into",
	},
}

func fnPing(ce *WrappedCommandEvent) {
	var text strings.Builder
	text.WriteString("You're signed into the following Slack workspaces:\n")
	ce.Bridge.userAndTeamLock.Lock()
	isEmpty := len(ce.User.teams) == 0
	for _, ut := range ce.User.teams {
		_, _ = fmt.Fprintf(&text, "* `%s`: %s / %s.slack.com", ut.Team.ID, ut.Team.Name, ut.Team.Domain)
		if ut.RTM == nil {
			text.WriteString(" (not connected)")
		}
		text.WriteByte('\n')
	}
	ce.Bridge.userAndTeamLock.Unlock()
	if isEmpty {
		ce.Reply("You aren't signed into any Slack workspaces")
	} else {
		ce.Reply(text.String())
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

	user := ce.Bridge.GetUserByMXID(ce.User.MXID)
	err := user.LoginTeam(ce.Ctx, ce.Args[0], ce.Args[1], ce.Args[2])
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

	user := ce.Bridge.GetUserByMXID(ce.User.MXID)
	info, err := user.TokenLogin(ce.Ctx, ce.Args[0], cookieToken)
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
	if len(ce.Args) != 1 {
		ce.Reply("**Usage**: $cmdprefix logout <workspace ID>")
		return
	}
	teamID := strings.ToUpper(ce.Args[1])
	if teamID[0] != 'T' {
		ce.Reply("That doesn't look like a workspace ID")
		return
	}
	ut := ce.User.GetTeam(teamID)
	if ut == nil || ut.Token == "" {
		ce.Reply("You're not logged into that team")
		return
	}

	ut.Logout(ce.Ctx, status.BridgeState{StateEvent: status.StateLoggedOut})
	ce.Reply("Logged out %s in %s / %s.slack.com", ut.Email, ut.TeamID, ut.Team.Name, ut.Team.Domain)
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
	//for _, team := range ce.User.Teams {
	//	ce.User.UpdateTeam(team, true)
	//}
	//ce.Reply("Done syncing teams.")
}

var cmdDeletePortal = &commands.FullHandler{
	Func:           wrapCommand(fnDeletePortal),
	Name:           "delete-portal",
	RequiresPortal: true,
	RequiresAdmin:  true, // TODO allow deleting without bridge admin if it's the only user
}

func fnDeletePortal(ce *WrappedCommandEvent) {
	ce.Portal.Delete(ce.Ctx)
	ce.Portal.Cleanup(ce.Ctx)
	ce.ZLog.Info().Msg("Deleted portal")
}
