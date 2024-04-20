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

package database

import (
	"context"
	"database/sql/driver"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/id"
)

type MessageQuery struct {
	*dbutil.QueryHelper[*Message]
}

func newMessage(qh *dbutil.QueryHelper[*Message]) *Message {
	return &Message{qh: qh}
}

const (
	getMessageBaseQuery = `
		SELECT team_id, channel_id, message_id, part_id, thread_id, author_id, mxid FROM message
	`
	getMessageBySlackIDQuery = getMessageBaseQuery + `
		WHERE team_id=$1 AND channel_id=$2 AND message_id=$3
	`
	getFirstMessagePartBySlackIDQuery = getMessageBySlackIDQuery + ` ORDER BY part_id ASC LIMIT 1`
	getLastMessagePartBySlackIDQuery  = getMessageBySlackIDQuery + ` ORDER BY part_id DESC LIMIT 1`
	getMessageByMXIDQuery             = getMessageBaseQuery + `
		WHERE mxid=$1
	`
	getFirstMessageInChannelQuery = getMessageBaseQuery + `
		WHERE team_id=$1 AND channel_id=$2
		ORDER BY message_id ASC LIMIT 1
	`
	getLastNonThreadMessageInChannelQuery = getMessageBaseQuery + `
		WHERE team_id=$1 AND channel_id=$2 AND thread_id=''
		ORDER BY message_id DESC LIMIT 1
	`
	getFirstMessageInThreadQuery = getMessageBaseQuery + `
		WHERE team_id=$1 AND channel_id=$2 AND thread_id=$3
		ORDER BY message_id ASC LIMIT 1
	`
	getLastMessageInThreadQuery = getMessageBaseQuery + `
		WHERE team_id=$1 AND channel_id=$2 AND thread_id=$3
		ORDER BY message_id DESC LIMIT 1
	`
	insertMessageQuery = `
		INSERT INTO message (team_id, channel_id, message_id, part_id, thread_id, author_id, mxid)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	deleteMessageQuery = `
		DELETE FROM message WHERE team_id=$1 AND channel_id=$2 AND message_id=$3 AND part_id=$4
	`
)

func (mq *MessageQuery) GetBySlackID(ctx context.Context, key PortalKey, slackID string) ([]*Message, error) {
	return mq.QueryMany(ctx, getMessageBySlackIDQuery, key.TeamID, key.ChannelID, slackID)
}

func (mq *MessageQuery) GetFirstPartBySlackID(ctx context.Context, key PortalKey, slackID string) (*Message, error) {
	return mq.QueryOne(ctx, getFirstMessagePartBySlackIDQuery, key.TeamID, key.ChannelID, slackID)
}

func (mq *MessageQuery) GetLastPartBySlackID(ctx context.Context, key PortalKey, slackID string) (*Message, error) {
	return mq.QueryOne(ctx, getLastMessagePartBySlackIDQuery, key.TeamID, key.ChannelID, slackID)
}

func (mq *MessageQuery) GetByMXID(ctx context.Context, eventID id.EventID) (*Message, error) {
	return mq.QueryOne(ctx, getMessageByMXIDQuery, eventID)
}

func (mq *MessageQuery) GetFirstInChannel(ctx context.Context, key PortalKey) (*Message, error) {
	return mq.QueryOne(ctx, getFirstMessageInChannelQuery, key.TeamID, key.ChannelID)
}

func (mq *MessageQuery) GetLastNonThreadInChannel(ctx context.Context, key PortalKey) (*Message, error) {
	return mq.QueryOne(ctx, getLastNonThreadMessageInChannelQuery, key.TeamID, key.ChannelID)
}

func (mq *MessageQuery) GetFirstInThread(ctx context.Context, key PortalKey, threadID string) (*Message, error) {
	return mq.QueryOne(ctx, getFirstMessageInThreadQuery, key.TeamID, key.ChannelID, threadID)
}

func (mq *MessageQuery) GetLastInThread(ctx context.Context, key PortalKey, threadID string) (*Message, error) {
	return mq.QueryOne(ctx, getLastMessageInThreadQuery, key.TeamID, key.ChannelID, threadID)
}

type PartType string

const (
	PartTypeFile PartType = "file"
)

type PartID struct {
	Type  PartType
	Index int
	ID    string
}

func (pid PartID) Value() (driver.Value, error) {
	return pid.String(), nil
}

func (pid *PartID) Scan(i any) error {
	strVal, ok := i.(string)
	if !ok {
		return fmt.Errorf("invalid type %T for PartID.Scan", i)
	}
	if strVal == "" {
		return nil
	}
	parts := strings.Split(strVal, ":")
	if len(parts) != 3 {
		return fmt.Errorf("invalid PartID format: %q", strVal)
	}
	pid.Type = PartType(parts[0])
	switch pid.Type {
	case PartTypeFile:
	default:
		return fmt.Errorf("invalid PartID type: %q", pid.Type)
	}
	var err error
	pid.Index, err = strconv.Atoi(parts[1])
	if err != nil {
		return fmt.Errorf("invalid PartID index: %w", err)
	}
	pid.ID = parts[2]
	return nil
}

func (pid *PartID) String() string {
	if pid == nil || pid.Type == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d:%s", pid.Type, pid.Index, pid.ID)
}

type Message struct {
	qh *dbutil.QueryHelper[*Message]

	PortalKey
	MessageID string
	Part      PartID

	ThreadID string
	AuthorID string

	MXID id.EventID
}

func (m *Message) Scan(row dbutil.Scannable) (*Message, error) {
	return dbutil.ValueOrErr(m, row.Scan(
		&m.TeamID,
		&m.ChannelID,
		&m.MessageID,
		&m.Part,
		&m.ThreadID,
		&m.AuthorID,
		&m.MXID,
	))
}

func (m *Message) SlackTS() (time.Time, error) {
	floatID, err := strconv.ParseFloat(m.MessageID, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse message ID: %w", err)
	}
	sec, dec := math.Modf(floatID)
	return time.Unix(int64(sec), int64(dec*float64(time.Second))), nil
}

func (m *Message) SlackURLPath() string {
	path := fmt.Sprintf("/archives/%[1]s/p%[2]s", m.ChannelID, strings.ReplaceAll(m.MessageID, ".", ""))
	if m.ThreadID != "" {
		path = fmt.Sprintf("%s?thread_id=%s&cid=%s", path, m.ThreadID, m.ChannelID)
	}
	return path
}

func (m *Message) sqlVariables() []any {
	return []any{m.TeamID, m.ChannelID, m.MessageID, m.Part, m.ThreadID, m.AuthorID, m.MXID}
}

func (m *Message) Insert(ctx context.Context) error {
	return m.qh.Exec(ctx, insertMessageQuery, m.sqlVariables()...)
}

func (m *Message) Delete(ctx context.Context) error {
	return m.qh.Exec(ctx, deleteMessageQuery, m.TeamID, m.ChannelID, m.MessageID, m.Part)
}
