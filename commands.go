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
	"fmt"
	"go.mau.fi/mautrix-slack/database"
	"net/url"
	"strings"

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
		cmdPing,
		cmdLoginPassword,
		cmdLoginToken,
		cmdLogout,
		cmdSyncTeams,
		cmdDeletePortal,
		cmdBackfillPortal,
		cmdBackfillAllPortals,
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
		Description: "Check which teams you're currently signed into",
	},
}

func fnPing(ce *WrappedCommandEvent) {
	if len(ce.User.Teams) == 0 {
		ce.Reply("You are not signed in to any Slack teams.")
		return
	}
	var text strings.Builder
	text.WriteString("You are signed in to the following Slack teams:\n")
	for _, team := range ce.User.Teams {
		teamInfo := ce.Bridge.DB.TeamInfo.GetBySlackTeam(team.Key.TeamID)
		text.WriteString(fmt.Sprintf("%s - %s - %s.slack.com", teamInfo.TeamID, teamInfo.TeamName, teamInfo.TeamDomain))
		if team.RTM == nil {
			text.WriteString(" (Error: not connected to Slack)")
		}
		text.WriteRune('\n')
	}
	ce.Reply(text.String())
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

	user := ce.Bridge.GetUserByMXID(ce.User.MXID)
	err := user.LoginTeam(ce.Args[0], ce.Args[1], ce.Args[2])
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
	info, err := user.TokenLogin(ce.Args[0], cookieToken)
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
	domain := strings.TrimSuffix(ce.Args[1], ".slack.com")
	userTeam := ce.User.bridge.DB.UserTeam.GetBySlackDomain(ce.User.MXID, ce.Args[0], domain)

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

var cmdBackfillPortal = &commands.FullHandler{
	Func:           wrapCommand(fnBackfillPortal),
	Name:           "backfill-portal",
	RequiresPortal: true,
}

func fnBackfillPortal(ce *WrappedCommandEvent) {
	userTeam := ce.User.GetUserTeam(ce.Portal.Key.TeamID)
	ce.User.establishTeamClient(userTeam)

	backfillSinglePortal(ce.Portal, userTeam)
	ce.Log.Infofln("Backfilled portal")
}

var cmdBackfillAllPortals = &commands.FullHandler{
	Func:          wrapCommand(fnBackfillAllPortals),
	Name:          "backfill-all-portals",
	RequiresLogin: true,
}

func fnBackfillAllPortals(ce *WrappedCommandEvent) {
	if len(ce.Args) != 2 {
		ce.Reply("**Usage**: $cmdprefix backfill-all-portals <email> <domain>")
		return
	}

	domain := strings.TrimSuffix(ce.Args[1], ".slack.com")
	userTeam := ce.Bridge.DB.UserTeam.GetBySlackDomain(ce.User.MXID, ce.Args[0], domain)
	ce.User.establishTeamClient(userTeam)

	portals := ce.Bridge.DB.Portal.GetAllForUserTeam(userTeam.Key)
	portalCount := len(portals)
	for i, dbPortal := range portals {
		portal := ce.Bridge.GetPortalByID(dbPortal.Key)
		backfillSinglePortal(portal, userTeam)
		ce.Log.Infofln("Completed backfill for %d of %d portals", i+1, portalCount)
	}
}

func backfillSinglePortal(portal *Portal, userTeam *database.UserTeam) {
	portal.slackMessageLock.Lock()
	defer portal.slackMessageLock.Unlock()

	portal.traditionalBackfill(userTeam)
}
