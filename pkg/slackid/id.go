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

package slackid

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

func MakeMessageID(teamID, channelID, timestamp string) networkid.MessageID {
	return networkid.MessageID(fmt.Sprintf("%s-%s-%s", teamID, channelID, timestamp))
}

func ParseMessageID(id networkid.MessageID) (teamID, channelID, timestamp string, ok bool) {
	parts := strings.Split(string(id), "-")
	if len(parts) != 3 {
		return
	}
	return parts[0], parts[1], parts[2], true
}

func ParseSlackTimestamp(timestamp string) time.Time {
	parts := strings.Split(timestamp, ".")

	seconds, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Now().UTC()
	}

	var nanoSeconds int64
	if len(parts) > 1 {
		nsec, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			nanoSeconds = 0
		} else {
			nanoSeconds = nsec
		}
	}

	return time.Unix(seconds, nanoSeconds)
}

func MakeUserID(teamID, userID string) networkid.UserID {
	return networkid.UserID(fmt.Sprintf("%s-%s", strings.ToLower(teamID), strings.ToLower(userID)))
}

func MakeUserLoginID(teamID, userID string) networkid.UserLoginID {
	return networkid.UserLoginID(fmt.Sprintf("%s-%s", teamID, userID))
}

func ParseUserID(id networkid.UserID) (teamID, userID string) {
	parts := strings.Split(string(id), "-")
	if len(parts) != 2 {
		return "", ""
	}
	return strings.ToUpper(parts[0]), strings.ToUpper(parts[1])
}

func ParseUserLoginID(id networkid.UserLoginID) (teamID, userID string) {
	parts := strings.Split(string(id), "-")
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func UserIDToUserLoginID(userID networkid.UserID) networkid.UserLoginID {
	return networkid.UserLoginID(strings.ToUpper(string(userID)))
}

func UserLoginIDToUserID(userLoginID networkid.UserLoginID) networkid.UserID {
	return networkid.UserID(strings.ToLower(string(userLoginID)))
}

const TeamPortalChannelID = "@"

func MakeTeamPortalID(teamID string) networkid.PortalID {
	return MakePortalID(teamID, TeamPortalChannelID)
}

func MakePortalID(teamID, channelID string) networkid.PortalID {
	return networkid.PortalID(fmt.Sprintf("%s-%s", teamID, channelID))
}

func ParsePortalID(id networkid.PortalID) (teamID, channelID string) {
	parts := strings.Split(string(id), "-")
	if len(parts) != 2 {
		return "", ""
	}
	if parts[1] == TeamPortalChannelID {
		parts[1] = ""
	}
	return parts[0], parts[1]
}

func MakePortalKey(teamID, channelID string, userLoginID networkid.UserLoginID, isPrivateChat bool) (key networkid.PortalKey) {
	key.ID = MakePortalID(teamID, channelID)
	if isPrivateChat {
		key.Receiver = userLoginID
	}
	return
}

type PartType string

const (
	PartTypeFile PartType = "file"
)

func MakePartID(partType PartType, index int, id string) networkid.PartID {
	return networkid.PartID(fmt.Sprintf("%s-%d-%s", partType, index, id))
}

func ParsePartID(partID networkid.PartID) (partType PartType, index int, id string, ok bool) {
	parts := strings.Split(string(partID), "-")
	if len(parts) != 3 {
		return
	}
	var err error
	index, err = strconv.Atoi(parts[1])
	if err != nil {
		return
	}
	partType = PartType(parts[0])
	id = parts[2]
	ok = true
	return
}
