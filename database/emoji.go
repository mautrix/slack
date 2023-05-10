package database

import (
	"database/sql"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
)

type EmojiQuery struct {
	db  *Database
	log log.Logger
}

func (eq *EmojiQuery) New() *Emoji {
	return &Emoji{
		db:  eq.db,
		log: eq.log,
	}
}

func (eq *EmojiQuery) GetEmojiCount(slackTeam string) (count int, err error) {
	row := eq.db.QueryRow(`SELECT COUNT(*) FROM emoji WHERE slack_team=?1`, slackTeam)
	err = row.Scan(&count)
	return
}

func (eq *EmojiQuery) GetBySlackID(slackID string, slackTeam string) *Emoji {
	query := `SELECT slack_id, slack_team, alias, image_url FROM emoji WHERE slack_id=$1 AND slack_team=$2`

	row := eq.db.QueryRow(query, slackID, slackTeam)
	if row == nil {
		return nil
	}

	return eq.New().Scan(row)
}

func (eq *EmojiQuery) GetByMXC(mxc id.ContentURI) *Emoji {
	query := `SELECT slack_id, slack_team, alias, image_url FROM emoji WHERE image_url=$1 ORDER BY alias NULLS FIRST`

	row := eq.db.QueryRow(query, mxc.String())
	if row == nil {
		return nil
	}

	return eq.New().Scan(row)
}

type Emoji struct {
	db  *Database
	log log.Logger

	SlackID   string
	SlackTeam string
	Alias     string
	ImageURL  id.ContentURI
}

func (e *Emoji) Scan(row dbutil.Scannable) *Emoji {
	var alias sql.NullString
	var imageURL sql.NullString
	err := row.Scan(&e.SlackID, &e.SlackTeam, &alias, &imageURL)
	if err != nil {
		if err != sql.ErrNoRows {
			e.log.Errorln("Database scan failed:", err)
		}

		return nil
	}

	e.ImageURL, _ = id.ParseContentURI(imageURL.String)
	e.Alias = alias.String

	return e
}

func (e *Emoji) Upsert(txn dbutil.Transaction) {
	query := "INSERT INTO emoji" +
		" (slack_id, slack_team, alias, image_url) VALUES ($1, $2, $3, $4)" +
		" ON CONFLICT (slack_id, slack_team) DO UPDATE" +
		" SET alias = excluded.alias, image_url = excluded.image_url"

	args := []interface{}{e.SlackID, e.SlackTeam, strPtr(e.Alias), strPtr(e.ImageURL.String())}

	var err error
	if txn != nil {
		_, err = txn.Exec(query, args...)
	} else {
		_, err = e.db.Exec(query, args...)
	}

	if err != nil {
		e.log.Warnfln("Failed to insert emoji %s %s: %v", e.SlackID, e.SlackTeam, err)
	}
}

func (e *Emoji) Delete() {
	query := "DELETE FROM emoji" +
		" WHERE slack_id=$1 AND slack_team=$2"

	_, err := e.db.Exec(query, e.SlackID, e.SlackTeam)

	if err != nil {
		e.log.Warnfln("Failed to delete emoji %s %s: %v", e.SlackID, e.SlackTeam, err)
	}
}
