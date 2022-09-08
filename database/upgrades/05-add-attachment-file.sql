-- v5: add file to attachment table

DROP TABLE attachment;

CREATE TABLE attachment (
	team_id    TEXT NOT NULL,
	user_id    TEXT NOT NULL,
	channel_id TEXT NOT NULL,

	slack_message_id TEXT NOT NULL,
    slack_file_id TEXT NOT NULL,
	matrix_event_id TEXT NOT NULL UNIQUE,

	PRIMARY KEY(slack_message_id, slack_file_id, matrix_event_id),
	FOREIGN KEY(team_id, user_id, channel_id) REFERENCES portal(team_id, user_id, channel_id) ON DELETE CASCADE
);
