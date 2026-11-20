-- v0 -> v5 (compatible with v1+): Latest schema
CREATE TABLE emoji (
    team_id   TEXT NOT NULL,
    emoji_id  TEXT NOT NULL,
    value     TEXT NOT NULL,
    alias     TEXT,
    image_mxc TEXT,

    PRIMARY KEY (team_id, emoji_id)
);

CREATE INDEX emoji_alias_idx ON emoji (team_id, alias);

CREATE TABLE slack_read_state (
    bridge_id     TEXT NOT NULL,
    user_login_id TEXT NOT NULL,
    channel_id    TEXT NOT NULL,
    last_read     TEXT NOT NULL,

    PRIMARY KEY (bridge_id, user_login_id, channel_id),
    CONSTRAINT slack_read_state_user_login_fkey FOREIGN KEY (bridge_id, user_login_id)
        REFERENCES user_login (bridge_id, id) ON DELETE CASCADE ON UPDATE CASCADE
);
