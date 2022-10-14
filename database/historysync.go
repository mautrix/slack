// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2022 Tulir Asokan, Sumner Evans, Max Sandholm
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
	"encoding/json"
	"errors"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/slack-go/slack"
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
)

type HistorySyncQuery struct {
	db  *Database
	log log.Logger
}

type HistorySyncConversation struct {
	db  *Database
	log log.Logger

	PortalKey      *PortalKey
	LastMessageId  string
	MarkedAsUnread bool
	UnreadCount    uint32
}

func (hsq *HistorySyncQuery) NewConversation() *HistorySyncConversation {
	return &HistorySyncConversation{
		db:        hsq.db,
		log:       hsq.log,
		PortalKey: &PortalKey{},
	}
}

func (hsq *HistorySyncQuery) NewConversationWithValues(
	portalKey *PortalKey,
	lastMessageId string,
	markedAsUnread bool,
	unreadCount uint32) *HistorySyncConversation {
	return &HistorySyncConversation{
		db:             hsq.db,
		log:            hsq.log,
		PortalKey:      portalKey,
		LastMessageId:  lastMessageId,
		MarkedAsUnread: markedAsUnread,
		UnreadCount:    unreadCount,
	}
}

const (
	getNMostRecentConversations = `
		SELECT team_id, channel_id, last_message_id, marked_as_unread, unread_count
		  FROM history_sync_conversation
		 WHERE user_mxid=$1
		 ORDER BY last_message_id DESC
		 LIMIT $2
	`
	getConversationByPortal = `
		SELECT team_id, channel_id, last_message_id, marked_as_unread, unread_count
		  FROM history_sync_conversation
		 WHERE team_id=$2
		   AND channel_id=$3
	`
)

func (hsc *HistorySyncConversation) Upsert() {
	_, err := hsc.db.Exec(`
		INSERT INTO history_sync_conversation (team_id, channel_id, last_message_id, marked_as_unread, unread_count)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (team_id, channel_id)
		DO UPDATE SET
			last_message_id=EXCLUDED.last_message_id,
			end_of_history_transfer_type=EXCLUDED.end_of_history_transfer_type
	`,
		hsc.PortalKey.TeamID,
		hsc.PortalKey.ChannelID,
		hsc.LastMessageId,
		hsc.MarkedAsUnread,
		hsc.UnreadCount)
	if err != nil {
		hsc.log.Warnfln("Failed to insert history sync conversation %s: %v", hsc.PortalKey, err)
	}
}

func (hsc *HistorySyncConversation) Scan(row dbutil.Scannable) *HistorySyncConversation {
	err := row.Scan(
		&hsc.PortalKey.TeamID,
		&hsc.PortalKey.ChannelID,
		&hsc.LastMessageId,
		&hsc.MarkedAsUnread,
		&hsc.UnreadCount)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			hsc.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	return hsc
}

func (hsq *HistorySyncQuery) GetNMostRecentConversations(userID id.UserID, n int) (conversations []*HistorySyncConversation) {
	nPtr := &n
	// Negative limit on SQLite means unlimited, but Postgres prefers a NULL limit.
	if n < 0 && hsq.db.Dialect == dbutil.Postgres {
		nPtr = nil
	}
	rows, err := hsq.db.Query(getNMostRecentConversations, userID, nPtr)
	defer rows.Close()
	if err != nil || rows == nil {
		return nil
	}
	for rows.Next() {
		conversations = append(conversations, hsq.NewConversation().Scan(rows))
	}
	return
}

func (hsq *HistorySyncQuery) GetConversation(portalKey *PortalKey) (conversation *HistorySyncConversation) {
	rows, err := hsq.db.Query(getConversationByPortal, portalKey.TeamID, portalKey.ChannelID)
	defer rows.Close()
	if err != nil || rows == nil {
		return nil
	}
	if rows.Next() {
		conversation = hsq.NewConversation().Scan(rows)
	}
	return
}

// func (hsq *HistorySyncQuery) DeleteAllConversations(userID id.UserID) {
// 	_, err := hsq.db.Exec("DELETE FROM history_sync_conversation WHERE user_mxid=$1", userID)
// 	if err != nil {
// 		hsq.log.Warnfln("Failed to delete historical chat info for %s/%s: %v", userID, err)
// 	}
// }

const (
	getMessagesBetween = `
		SELECT data FROM history_sync_message
		WHERE conversation_id=$2
			%s
		ORDER BY timestamp DESC
		%s
	`
)

type HistorySyncMessage struct {
	db  *Database
	log log.Logger

	portalKey *PortalKey
	MessageID string
	Data      []byte
}

func (hsq *HistorySyncQuery) NewMessageWithValues(portalKey *PortalKey, message *slack.Message) (*HistorySyncMessage, error) {
	msgData, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	return &HistorySyncMessage{
		db:        hsq.db,
		log:       hsq.log,
		portalKey: portalKey,
		MessageID: message.Timestamp,
		Data:      msgData,
	}, nil
}

func (hsm *HistorySyncMessage) Insert() {
	_, err := hsm.db.Exec(`
		INSERT INTO history_sync_message (team_id, channel_id, message_id, data, inserted_time)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (conversation_id, message_id) DO NOTHING
	`, hsm.portalKey.TeamID, hsm.portalKey.ChannelID, hsm.MessageID, hsm.Data, time.Now())
	if err != nil {
		hsm.log.Warnfln("Failed to insert history sync message %s/%s: %v", hsm.portalKey, hsm.MessageID, err)
	}
}

// func (hsq *HistorySyncQuery) GetMessagesBetween(userID id.UserID, portalKey *PortalKey, startTime, endTime *time.Time, limit int) (messages []*slack.Message) {
// 	whereClauses := ""
// 	args := []interface{}{userID, portalKey.TeamID, portalKey.ChannelID}
// 	argNum := 3
// 	if startTime != nil {
// 		whereClauses += fmt.Sprintf(" AND timestamp >= $%d", argNum)
// 		args = append(args, startTime)
// 		argNum++
// 	}
// 	if endTime != nil {
// 		whereClauses += fmt.Sprintf(" AND timestamp <= $%d", argNum)
// 		args = append(args, endTime)
// 	}

// 	limitClause := ""
// 	if limit > 0 {
// 		limitClause = fmt.Sprintf("LIMIT %d", limit)
// 	}

// 	rows, err := hsq.db.Query(fmt.Sprintf(getMessagesBetween, whereClauses, limitClause), args...)
// 	defer rows.Close()
// 	if err != nil || rows == nil {
// 		if err != nil && !errors.Is(err, sql.ErrNoRows) {
// 			hsq.log.Warnfln("Failed to query messages between range: %v", err)
// 		}
// 		return nil
// 	}

// 	var msgData []byte
// 	for rows.Next() {
// 		err = rows.Scan(&msgData)
// 		if err != nil {
// 			hsq.log.Errorfln("Database scan failed: %v", err)
// 			continue
// 		}
// 		var historySyncMsg slack.Message
// 		err = json.Unmarshal(msgData, &historySyncMsg)
// 		if err != nil {
// 			hsq.log.Errorfln("Failed to unmarshal history sync message: %v", err)
// 			continue
// 		}
// 		messages = append(messages, &historySyncMsg)
// 	}
// 	return
// }

func (hsq *HistorySyncQuery) DeleteMessages(portalKey *PortalKey, messages []*slack.Message) error {
	var err error
	for _, message := range messages {
		_, err = hsq.db.Exec(`DELETE FROM history_sync_message
		WHERE team_id=$1 AND channel_id=$2 AND message_id=$3`, portalKey.TeamID, portalKey.ChannelID, message.Timestamp)
	}
	// TODO: slack message IDs aren't sortable in SQL; should convert them to an SQL timestamp so this original code works?
	// newest := messages[0]
	// beforeTS := time.Unix(int64(newest.GetMessageTimestamp())+1, 0)
	// oldest := messages[len(messages)-1]
	// afterTS := time.Unix(int64(oldest.GetMessageTimestamp())-1, 0)
	// _, err := hsq.db.Exec(deleteMessagesBetweenExclusive, userID, conversationID, beforeTS, afterTS)
	return err
}

// func (hsq *HistorySyncQuery) DeleteAllMessages(userID id.UserID) {
// 	_, err := hsq.db.Exec("DELETE FROM history_sync_message WHERE user_mxid=$1", userID)
// 	if err != nil {
// 		hsq.log.Warnfln("Failed to delete historical messages for %s: %v", userID, err)
// 	}
// }

func (hsq *HistorySyncQuery) DeleteAllMessagesForPortal(portalKey PortalKey) {
	_, err := hsq.db.Exec(`
		DELETE FROM history_sync_message
		WHERE team_id=$1 AND channel_id=$2
	`, portalKey.TeamID, portalKey.ChannelID)
	if err != nil {
		hsq.log.Warnfln("Failed to delete historical messages for %s: %v", portalKey, err)
	}
}
