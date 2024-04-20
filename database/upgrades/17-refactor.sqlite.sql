-- v17: Refactor database
CREATE TABLE message_new (
    team_id    TEXT NOT NULL,
    channel_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    part_id    TEXT,

    thread_id  TEXT,
    author_id  TEXT NOT NULL,

    mxid       TEXT NOT NULL UNIQUE,

    PRIMARY KEY (team_id, channel_id, message_id, part_id),
    CONSTRAINT message_portal_fkey FOREIGN KEY (team_id, channel_id) REFERENCES portal (team_id, channel_id)
        ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT message_mxid_unique UNIQUE (mxid)
);

INSERT INTO message_new (team_id, channel_id, message_id, part_id, thread_id, author_id, mxid)
SELECT team_id, channel_id, slack_message_id, '', slack_thread_id, author_id, matrix_message_id
FROM message;
INSERT INTO message_new (team_id, channel_id, message_id, part_id, thread_id, author_id, mxid)
SELECT team_id,
       channel_id,
       slack_message_id,
       'file:0:' || slack_file_id,
       slack_thread_id,
       COALESCE(
           (SELECT author_id
            FROM message targetmsg
            WHERE targetmsg.team_id = attachment.team_id
              AND targetmsg.channel_id = attachment.channel_id
              AND targetmsg.slack_message_id = attachment.slack_message_id),
           ''
       ),
       matrix_event_id
FROM attachment;
DROP TABLE message;
DROP TABLE attachment;
ALTER TABLE message_new RENAME TO message;

CREATE TABLE reaction_new (
    team_id           TEXT NOT NULL,
    channel_id        TEXT NOT NULL,
    message_id        TEXT NOT NULL,
    msg_first_part_id TEXT NOT NULL,
    author_id         TEXT NOT NULL,
    emoji_id          TEXT NOT NULL,

    mxid              TEXT NOT NULL,

    PRIMARY KEY (team_id, channel_id, message_id, author_id, emoji_id),
    CONSTRAINT reaction_portal_fkey FOREIGN KEY (team_id, channel_id) REFERENCES portal (team_id, channel_id)
        ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT reaction_message_fkey FOREIGN KEY (team_id, channel_id, message_id, msg_first_part_id)
        REFERENCES message (team_id, channel_id, message_id, part_id)
        ON DELETE CASCADE ON UPDATE CASCADE
);
INSERT INTO reaction_new (team_id, channel_id, message_id, msg_first_part_id, author_id, emoji_id, mxid)
SELECT team_id,
       channel_id,
       slack_message_id,
       (SELECT part_id
        FROM message
        WHERE message.team_id = reaction.team_id
          AND message.channel_id = reaction.channel_id
          AND message.message_id = reaction.slack_message_id
        ORDER BY part_id
        LIMIT 1),
       author_id,
       slack_name,
       matrix_event_id
FROM reaction;
DROP TABLE reaction;
ALTER TABLE reaction_new RENAME TO reaction;

ALTER TABLE portal RENAME COLUMN avatar_url TO avatar_mxc;
ALTER TABLE portal DROP COLUMN first_event_id;
ALTER TABLE portal DROP COLUMN next_batch_id;
ALTER TABLE portal ADD COLUMN receiver TEXT NOT NULL DEFAULT '';
ALTER TABLE portal ADD COLUMN more_to_backfill BOOLEAN NOT NULL DEFAULT false;
UPDATE portal SET more_to_backfill=true WHERE more_to_backfill=false AND EXISTS(
    SELECT 1 FROM backfill_state bs
    WHERE bs.team_id=portal.team_id
      AND bs.channel_id=portal.channel_id
      AND backfill_complete=false
);

UPDATE puppet SET avatar='' WHERE avatar IS NULL;
ALTER TABLE puppet RENAME COLUMN avatar_url TO avatar_mxc;
ALTER TABLE puppet DROP COLUMN enable_presence;
ALTER TABLE puppet DROP COLUMN enable_receipts;
ALTER TABLE puppet DROP COLUMN next_batch;
ALTER TABLE puppet DROP COLUMN custom_mxid;
ALTER TABLE puppet DROP COLUMN access_token;

INSERT INTO team_portal (team_id)
SELECT DISTINCT team_id FROM user_team
ON CONFLICT (team_id) DO NOTHING;

ALTER TABLE emoji RENAME COLUMN slack_team TO team_id;
ALTER TABLE emoji RENAME COLUMN slack_id TO emoji_id;
ALTER TABLE emoji RENAME COLUMN image_url TO image_mxc;
ALTER TABLE emoji ADD COLUMN value TEXT NOT NULL DEFAULT '';

ALTER TABLE "user" ADD COLUMN access_token TEXT;

ALTER TABLE team_info RENAME TO team_portal;
UPDATE team_portal SET team_domain='' WHERE team_domain IS NULL;
UPDATE team_portal SET team_url='' WHERE team_url IS NULL;
UPDATE team_portal SET team_name='' WHERE team_name IS NULL;
UPDATE team_portal SET avatar='' WHERE avatar IS NULL;
ALTER TABLE team_portal RENAME COLUMN team_id TO id;
ALTER TABLE team_portal RENAME COLUMN team_domain TO domain;
ALTER TABLE team_portal RENAME COLUMN team_url TO url;
ALTER TABLE team_portal RENAME COLUMN team_name TO name;
ALTER TABLE team_portal RENAME COLUMN avatar_url TO avatar_mxc;

CREATE TABLE user_team_portal_tmp (
    user_mxid  TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    team_id    TEXT NOT NULL,
    channel_id TEXT NOT NULL
);
INSERT INTO user_team_portal_tmp (user_mxid, user_id, team_id, channel_id)
SELECT matrix_user_id, slack_user_id, slack_team_id, portal_channel_id
FROM user_team_portal;
DROP TABLE user_team_portal;

CREATE TABLE user_team_new (
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
INSERT INTO user_team_new (team_id, user_id, user_mxid, email, token, cookie_token, in_space)
SELECT team_id, slack_id, mxid, slack_email, token, cookie_token, in_space
FROM user_team;
DROP TABLE user_team;
ALTER TABLE user_team_new RENAME TO user_team;
CREATE INDEX user_team_user_idx ON user_team (user_mxid);

CREATE TABLE user_team_portal (
    team_id    TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    channel_id TEXT NOT NULL,
    user_mxid  TEXT NOT NULL,

    backfill_finished       BOOLEAN NOT NULL DEFAULT false,
    backfill_priority       INTEGER NOT NULL DEFAULT 0,
    backfilled_count        INTEGER NOT NULL DEFAULT 0,
    backfill_dispatched_at  BIGINT,
    backfill_completed_at   BIGINT,
    backfill_cooldown_until BIGINT,

    PRIMARY KEY (team_id, user_id, channel_id),
    CONSTRAINT utp_user_fkey FOREIGN KEY (user_mxid) REFERENCES "user" (mxid)
        ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT utp_ut_fkey FOREIGN KEY (team_id, user_id) REFERENCES user_team (team_id, user_id)
        ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT utp_portal_fkey FOREIGN KEY (team_id, channel_id) REFERENCES portal (team_id, channel_id)
        ON DELETE CASCADE ON UPDATE CASCADE
);
INSERT INTO user_team_portal (team_id, user_id, channel_id, user_mxid)
SELECT team_id, user_id, channel_id, user_mxid
FROM user_team_portal_tmp;
DROP TABLE user_team_portal_tmp;
CREATE INDEX user_team_portal_user_idx ON user_team_portal (user_mxid);
CREATE INDEX user_team_portal_portal_idx ON user_team_portal (team_id, channel_id);
