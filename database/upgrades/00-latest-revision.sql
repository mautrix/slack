-- v1 -> v12: Latest revision

CREATE TABLE portal (
	team_id    TEXT,
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
	next_batch_id  TEXT,
	first_slack_id TEXT,

	in_space BOOLEAN DEFAULT false,

	PRIMARY KEY (team_id, channel_id)
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

	is_bot BOOLEAN NOT NULL DEFAULT false,

	contact_info_set BOOLEAN NOT NULL DEFAULT false,

	PRIMARY KEY(team_id, user_id)
);

CREATE TABLE "user" (
	mxid TEXT PRIMARY KEY,

	management_room TEXT,
	space_room      TEXT
);

CREATE TABLE "user_team" (
	mxid TEXT NOT NULL,

	slack_email TEXT NOT NULL,
	slack_id    TEXT NOT NULL,

	team_name TEXT NOT NULL,
	team_id   TEXT NOT NULL,

	token        TEXT,
    cookie_token TEXT,

	in_space BOOLEAN DEFAULT false,

	PRIMARY KEY(mxid, slack_id, team_id)
);

CREATE TABLE user_team_portal (
    matrix_user_id    TEXT NOT NULL,
    slack_user_id     TEXT NOT NULL,
    slack_team_id     TEXT NOT NULL,
    portal_channel_id TEXT NOT NULL,
    FOREIGN KEY(matrix_user_id, slack_user_id, slack_team_id) REFERENCES "user_team"(mxid, slack_id, team_id) ON DELETE CASCADE,
    FOREIGN KEY(slack_team_id, portal_channel_id) REFERENCES portal(team_id, channel_id) ON DELETE CASCADE
);

CREATE TABLE message (
	team_id    TEXT NOT NULL,
	channel_id TEXT NOT NULL,

	slack_message_id  TEXT NOT NULL,
    slack_thread_id   TEXT,
	matrix_message_id TEXT NOT NULL UNIQUE,

	author_id TEXT   NOT NULL,

	PRIMARY KEY(slack_message_id, team_id, channel_id),
	FOREIGN KEY(team_id, channel_id) REFERENCES portal(team_id, channel_id) ON DELETE CASCADE
);

CREATE TABLE reaction (
	team_id    TEXT NOT NULL,
	channel_id TEXT NOT NULL,

	slack_message_id TEXT NOT NULL,
	matrix_event_id  TEXT NOT NULL UNIQUE,

	author_id TEXT NOT NULL,

	matrix_name TEXT,
	matrix_url TEXT,

	slack_name TEXT,

	UNIQUE (slack_name, author_id, slack_message_id, team_id, channel_id),
	FOREIGN KEY(team_id, channel_id) REFERENCES portal(team_id, channel_id) ON DELETE CASCADE
);

CREATE TABLE attachment (
	team_id    TEXT NOT NULL,
	channel_id TEXT NOT NULL,

	slack_message_id TEXT NOT NULL,
    slack_file_id    TEXT NOT NULL,
	matrix_event_id  TEXT NOT NULL UNIQUE,
	slack_thread_id  TEXT,

	PRIMARY KEY(slack_message_id, slack_file_id, matrix_event_id),
	FOREIGN KEY(team_id, channel_id) REFERENCES portal(team_id, channel_id) ON DELETE CASCADE
);

CREATE TABLE "team_info" (
    team_id     TEXT NOT NULL UNIQUE,
    team_domain TEXT,
    team_url    TEXT,
    team_name   TEXT,
    avatar      TEXT,
    avatar_url  TEXT,
	space_room  TEXT,
	name_set    BOOLEAN DEFAULT false,
	avatar_set  BOOLEAN DEFAULT false
);

CREATE TABLE backfill_state (
    team_id            TEXT,
    channel_id         TEXT,
    backfill_complete  BOOLEAN,
	dispatched         BOOLEAN,
	message_count      INTEGER,
	immediate_complete BOOLEAN
    PRIMARY KEY (team_id, channel_id),
    FOREIGN KEY (team_id, channel_id) REFERENCES portal (team_id, channel_id) ON DELETE CASCADE
);

CREATE TABLE backfill_state (
    team_id            TEXT,
    channel_id         TEXT,
    backfill_complete  BOOLEAN,
    dispatched         BOOLEAN,
    message_count      INTEGER,
    immediate_complete BOOLEAN,
    PRIMARY KEY (team_id, channel_id),
    FOREIGN KEY (team_id, channel_id) REFERENCES portal (team_id, channel_id) ON DELETE CASCADE
);

CREATE TABLE emoji (
	slack_id   TEXT NOT NULL,
	slack_team TEXT NOT NULL,
	alias      TEXT,
	image_url  TEXT,

	PRIMARY KEY (slack_id, slack_team)
);
