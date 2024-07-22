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
	"github.com/slack-go/slack"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-slack/pkg/slackid"
)

func (s *SlackClient) makePortalKey(ch *slack.Channel) networkid.PortalKey {
	return slackid.MakePortalKey(s.TeamID, ch.ID, s.UserLogin.ID, ch.IsIM || ch.IsMpIM)
}

func (s *SlackClient) makeEventSender(userID string) bridgev2.EventSender {
	return bridgev2.EventSender{
		IsFromMe:    userID == s.UserID,
		SenderLogin: slackid.MakeUserLoginID(s.TeamID, userID),
		Sender:      slackid.MakeUserID(s.TeamID, userID),
	}
}
