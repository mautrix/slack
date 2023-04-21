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

package database

import (
	"database/sql"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
)

const (
	puppetSelect = "SELECT team_id, user_id, name, name_set, avatar," +
		" avatar_url, avatar_set, enable_presence, custom_mxid, access_token," +
		" next_batch, enable_receipts, contact_info_set" +
		" FROM puppet "
)

type Puppet struct {
	db  *Database
	log log.Logger

	TeamID string
	UserID string

	Name    string
	NameSet bool

	Avatar    string
	AvatarURL id.ContentURI
	AvatarSet bool

	EnablePresence bool

	CustomMXID  id.UserID
	AccessToken string

	NextBatch string

	EnableReceipts bool

	ContactInfoSet bool
}

func (p *Puppet) Scan(row dbutil.Scannable) *Puppet {
	var teamID, userID, avatar, avatarURL sql.NullString
	var enablePresence sql.NullBool
	var customMXID, accessToken, nextBatch sql.NullString

	err := row.Scan(&teamID, &userID, &p.Name, &p.NameSet, &avatar, &avatarURL,
		&p.AvatarSet, &enablePresence, &customMXID, &accessToken, &nextBatch,
		&p.EnableReceipts, &p.ContactInfoSet)

	if err != nil {
		if err != sql.ErrNoRows {
			p.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	p.TeamID = teamID.String
	p.UserID = userID.String
	p.Avatar = avatar.String
	p.AvatarURL, _ = id.ParseContentURI(avatarURL.String)
	p.EnablePresence = enablePresence.Bool
	p.CustomMXID = id.UserID(customMXID.String)
	p.AccessToken = accessToken.String
	p.NextBatch = nextBatch.String

	return p
}

func (p *Puppet) Insert() {
	query := "INSERT INTO puppet" +
		" (team_id, user_id, name, name_set, avatar, avatar_url, avatar_set," +
		" enable_presence, custom_mxid, access_token, next_batch," +
		" enable_receipts, contact_info_set)" +
		" VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)"

	_, err := p.db.Exec(query, p.TeamID, p.UserID, p.Name, p.NameSet, p.Avatar,
		p.AvatarURL.String(), p.AvatarSet, p.EnablePresence, p.CustomMXID,
		p.AccessToken, p.NextBatch, p.EnableReceipts, p.ContactInfoSet)

	if err != nil {
		p.log.Warnfln("Failed to insert %s-%s: %v", p.TeamID, p.UserID, err)
	}
}

func (p *Puppet) Update() {
	query := "UPDATE puppet" +
		" SET name=$1, name_set=$2, avatar=$3, avatar_url=$4, avatar_set=$5," +
		"     enable_presence=$6, custom_mxid=$7, access_token=$8," +
		"     next_batch=$9, enable_receipts=$10, contact_info_set=$11" +
		" WHERE team_id=$12 AND user_id=$13"

	_, err := p.db.Exec(query, p.Name, p.NameSet, p.Avatar,
		p.AvatarURL.String(), p.AvatarSet, p.EnablePresence, p.CustomMXID,
		p.AccessToken, p.NextBatch, p.EnableReceipts, p.ContactInfoSet, p.TeamID, p.UserID)

	if err != nil {
		p.log.Warnfln("Failed to update %s-%s: %v", p.TeamID, p.UserID, err)
	}
}
