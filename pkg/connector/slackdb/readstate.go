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

package slackdb

import (
	"context"
	"database/sql"
	"errors"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

type ReadStateQuery struct {
	db       *dbutil.Database
	bridgeID networkid.BridgeID
}

const (
	getReadStateQuery = `
		SELECT last_read FROM slack_read_state WHERE bridge_id=$1 AND user_login_id=$2 AND channel_id=$3
	`
	putReadStateQuery = `
		INSERT INTO slack_read_state (bridge_id, user_login_id, channel_id, last_read)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (bridge_id, user_login_id, channel_id) DO UPDATE SET last_read=excluded.last_read
	`
)

func (rsq *ReadStateQuery) Get(ctx context.Context, loginID networkid.UserLoginID, channelID string) (lastRead string, err error) {
	err = rsq.db.QueryRow(ctx, getReadStateQuery, rsq.bridgeID, loginID, channelID).Scan(&lastRead)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}

func (rsq *ReadStateQuery) Put(ctx context.Context, loginID networkid.UserLoginID, channelID, lastRead string) error {
	_, err := rsq.db.Exec(ctx, putReadStateQuery, rsq.bridgeID, loginID, channelID, lastRead)
	return err
}
