-- v0 -> v1: Latest schema
CREATE TABLE emoji (
    team_id   TEXT NOT NULL,
    emoji_id  TEXT NOT NULL,
    value     TEXT NOT NULL,
    alias     TEXT,
    image_mxc TEXT,

    PRIMARY KEY (team_id, emoji_id)
);
