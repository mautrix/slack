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
	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

type PuppetQuery struct {
	db  *Database
	log log.Logger
}

func (pq *PuppetQuery) New() *Puppet {
	return &Puppet{
		db:  pq.db,
		log: pq.log,

		EnablePresence: true,
	}
}

func (pq *PuppetQuery) Get(teamID, userID string) *Puppet {
	return pq.get(puppetSelect+" WHERE team_id=$1 AND user_id=$2", teamID, userID)
}

func (pq *PuppetQuery) GetByCustomMXID(mxid id.UserID) *Puppet {
	return pq.get(puppetSelect+" WHERE custom_mxid=$1", mxid)
}

func (pq *PuppetQuery) get(query string, args ...interface{}) *Puppet {
	row := pq.db.QueryRow(query, args...)
	if row == nil {
		return nil
	}

	return pq.New().Scan(row)
}

func (pq *PuppetQuery) GetAll() []*Puppet {
	return pq.getAll(puppetSelect)
}

func (pq *PuppetQuery) GetAllForTeam(teamID string) []*Puppet {
	return pq.getAll(puppetSelect+" WHERE team_id=$1", teamID)
}

func (pq *PuppetQuery) GetAllWithCustomMXID() []*Puppet {
	return pq.getAll(puppetSelect + " WHERE custom_mxid<>''")
}

func (pq *PuppetQuery) getAll(query string, args ...interface{}) []*Puppet {
	rows, err := pq.db.Query(query, args...)
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()

	puppets := []*Puppet{}
	for rows.Next() {
		puppets = append(puppets, pq.New().Scan(rows))
	}

	return puppets
}
