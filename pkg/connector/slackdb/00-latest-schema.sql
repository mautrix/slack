-- v0 -> v2 (compatible with v1+): Latest schema
CREATE TABLE emoji (
    team_id   TEXT NOT NULL,
    emoji_id  TEXT NOT NULL,
    value     TEXT NOT NULL,
    alias     TEXT,
    image_mxc TEXT,

    PRIMARY KEY (team_id, emoji_id)
);

CREATE INDEX emoji_alias_idx ON emoji (team_id, alias);
