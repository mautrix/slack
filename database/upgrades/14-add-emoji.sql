-- v14: Add table for custom Slack emojis

CREATE TABLE emoji (
	slack_id   TEXT NOT NULL,
	slack_team TEXT NOT NULL,
	alias      TEXT,
	image_url  TEXT,

	PRIMARY KEY (slack_id, slack_team)
);
