-- v0 -> v17: Latest revision

CREATE TABLE team_portal (
    id         TEXT    NOT NULL,
    mxid       TEXT,
    domain     TEXT    NOT NULL,
    url        TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    avatar     TEXT    NOT NULL,
    avatar_mxc TEXT,
    name_set   BOOLEAN NOT NULL DEFAULT false,
    avatar_set BOOLEAN NOT NULL DEFAULT false,

    PRIMARY KEY (id),
    CONSTRAINT team_portal_mxid_unique UNIQUE (mxid)
);

CREATE TABLE portal (
    team_id        TEXT    NOT NULL,
    channel_id     TEXT    NOT NULL,
    receiver       TEXT    NOT NULL,
    mxid           TEXT,

    type           INT     NOT NULL DEFAULT 0,
    dm_user_id     TEXT,

    plain_name     TEXT    NOT NULL,
    name           TEXT    NOT NULL,
    name_set       BOOLEAN NOT NULL DEFAULT false,
    topic          TEXT    NOT NULL,
    topic_set      BOOLEAN NOT NULL DEFAULT false,
    avatar         TEXT    NOT NULL,
    avatar_mxc     TEXT,
    avatar_set     BOOLEAN NOT NULL DEFAULT false,
    encrypted      BOOLEAN NOT NULL DEFAULT false,
    in_space       BOOLEAN NOT NULL DEFAULT false,

    first_slack_id TEXT,

    PRIMARY KEY (team_id, channel_id, receiver),
    CONSTRAINT portal_mxid_unique UNIQUE (mxid),
    CONSTRAINT portal_team_fkey FOREIGN KEY (team_id) REFERENCES team_portal (id)
        ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE puppet (
    team_id          TEXT    NOT NULL,
    user_id          TEXT    NOT NULL,

    name             TEXT    NOT NULL,
    avatar           TEXT    NOT NULL,
    avatar_mxc       TEXT,
    is_bot           BOOLEAN NOT NULL DEFAULT false,
    name_set         BOOLEAN NOT NULL DEFAULT false,
    avatar_set       BOOLEAN NOT NULL DEFAULT false,
    contact_info_set BOOLEAN NOT NULL DEFAULT false,

    PRIMARY KEY (team_id, user_id)
);

CREATE TABLE "user" (
    mxid            TEXT PRIMARY KEY,

    management_room TEXT,
    space_room      TEXT,
    access_token    TEXT
);

CREATE TABLE user_team (
    team_id      TEXT    NOT NULL,
    user_id      TEXT    NOT NULL,
    user_mxid    TEXT    NOT NULL,

    email        TEXT    NOT NULL,
    token        TEXT    NOT NULL,
    cookie_token TEXT    NOT NULL,

    in_space     BOOLEAN NOT NULL DEFAULT false,

    PRIMARY KEY (team_id, user_id),
    CONSTRAINT user_team_mxid_unique UNIQUE (team_id, user_mxid),
    CONSTRAINT user_team_user_fkey FOREIGN KEY (user_mxid) REFERENCES "user" (mxid)
        ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT user_team_team_fkey FOREIGN KEY (team_id) REFERENCES team_portal (id)
        ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE INDEX user_team_user_idx ON user_team (user_mxid);

CREATE TABLE user_team_portal (
    team_id    TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    channel_id TEXT NOT NULL,
    user_mxid  TEXT NOT NULL,
    PRIMARY KEY (team_id, user_id, channel_id),
    CONSTRAINT utp_user_fkey FOREIGN KEY (user_mxid) REFERENCES "user" (mxid)
        ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT utp_ut_fkey FOREIGN KEY (team_id, user_id) REFERENCES user_team (team_id, user_id)
        ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT utp_portal_fkey FOREIGN KEY (team_id, channel_id) REFERENCES portal (team_id, channel_id)
        ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE INDEX user_team_portal_user_idx ON user_team_portal (user_mxid);
CREATE INDEX user_team_portal_portal_idx ON user_team_portal (team_id, channel_id);

CREATE TABLE message (
    team_id    TEXT NOT NULL,
    channel_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    part_id    TEXT NOT NULL,

    thread_id  TEXT NOT NULL,
    author_id  TEXT NOT NULL,
    mxid       TEXT NOT NULL,

    PRIMARY KEY (team_id, channel_id, message_id, part_id),
    CONSTRAINT message_portal_fkey FOREIGN KEY (team_id, channel_id) REFERENCES portal (team_id, channel_id)
        ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT message_mxid_unique UNIQUE (mxid)
);

CREATE TABLE reaction (
    team_id           TEXT NOT NULL,
    channel_id        TEXT NOT NULL,
    message_id        TEXT NOT NULL,
    msg_first_part_id TEXT NOT NULL,
    author_id         TEXT NOT NULL,
    emoji_id          TEXT NOT NULL,

    mxid              TEXT NOT NULL,

    PRIMARY KEY (team_id, channel_id, message_id, author_id, emoji_id),
    CONSTRAINT reaction_mxid_unique UNIQUE (mxid),
    CONSTRAINT reaction_portal_fkey FOREIGN KEY (team_id, channel_id) REFERENCES portal (team_id, channel_id)
        ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT reaction_message_fkey FOREIGN KEY (team_id, channel_id, message_id, msg_first_part_id)
        REFERENCES message (team_id, channel_id, message_id, part_id)
        ON DELETE CASCADE ON UPDATE CASCADE
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
    team_id   TEXT NOT NULL,
    emoji_id  TEXT NOT NULL,
    value     TEXT NOT NULL,
    alias     TEXT,
    image_mxc TEXT,

    PRIMARY KEY (team_id, emoji_id)
);
