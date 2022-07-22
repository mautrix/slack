-- v1: Initial revision

CREATE TABLE portal (
	team_id    TEXT,
	user_id    TEXT,
	channel_id TEXT,
	mxid       TEXT UNIQUE,

	type INT DEFAULT 0,
	dm_user_id TEXT,

	plain_name TEXT NOT NULL,
	name       TEXT NOT NULL,
	name_set   BOOLEAN DEFAULT false,
	topic      TEXT NOT NULL,
	topic_set  BOOLEAN DEFAULT false,
	avatar     TEXT NOT NULL,
	avatar_url TEXT,
	avatar_set BOOLEAN DEFAULT FALSE,

	encrypted BOOLEAN NOT NULL DEFAULT false,

	first_event_id TEXT,

	PRIMARY KEY (team_id, user_id, channel_id)
);

CREATE TABLE puppet (
	team_id TEXT NOT NULL,
	user_id TEXT NOT NULL,

	name TEXT NOT NULL,
	name_set BOOLEAN DEFAULT false,

	avatar     TEXT,
	avatar_url TEXT,
	avatar_set BOOLEAN DEFAULT false,

	enable_presence BOOLEAN NOT NULL DEFAULT true,
	enable_receipts BOOLEAN NOT NULL DEFAULT true,

	custom_mxid  TEXT,
	access_token TEXT,
	next_batch   TEXT,

	PRIMARY KEY(team_id, user_id)
);

CREATE TABLE "user" (
	mxid TEXT PRIMARY KEY,

	management_room TEXT
);

CREATE TABLE "user_team" (
	mxid TEXT NOT NULL,

	slack_email TEXT NOT NULL,
	slack_id TEXT NOT NULL,

	team_name TEXT NOT NULL,
	team_id TEXT NOT NULL,

	token TEXT,

	PRIMARY KEY(mxid, slack_id, team_id)
);

CREATE TABLE message (
	team_id    TEXT NOT NULL,
	user_id    TEXT NOT NULL,
	channel_id TEXT NOT NULL,

	slack_message_id TEXT NOT NULL,
	matrix_message_id  TEXT NOT NULL UNIQUE,

	author_id TEXT   NOT NULL,
	timestamp BIGINT NOT NULL,

	PRIMARY KEY(slack_message_id, team_id, user_id, channel_id),
	FOREIGN KEY(team_id, user_id, channel_id) REFERENCES portal(team_id, user_id, channel_id) ON DELETE CASCADE
);

CREATE TABLE reaction (
	team_id    TEXT NOT NULL,
	user_id    TEXT NOT NULL,
	channel_id TEXT NOT NULL,

	slack_message_id TEXT NOT NULL,
	matrix_event_id  TEXT NOT NULL UNIQUE,

	author_id TEXT NOT NULL,

	matrix_name TEXT,
	matrix_url TEXT,

	slack_name TEXT,

	UNIQUE (slack_name, author_id, slack_message_id, team_id, user_id, channel_id),
	FOREIGN KEY(team_id, user_id, channel_id) REFERENCES portal(team_id, user_id, channel_id) ON DELETE CASCADE
);

CREATE TABLE attachment (
	team_id    TEXT NOT NULL,
	user_id    TEXT NOT NULL,
	channel_id TEXT NOT NULL,

	slack_id TEXT NOT NULL,

	matrix_event_id TEXT NOT NULL UNIQUE,

	PRIMARY KEY(slack_id, matrix_event_id),
	FOREIGN KEY(team_id, user_id, channel_id) REFERENCES portal(team_id, user_id, channel_id) ON DELETE CASCADE
);
