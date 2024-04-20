-- v17: Refactor database
ALTER TABLE message RENAME COLUMN slack_message_id TO message_id;
ALTER TABLE message RENAME COLUMN slack_thread_id TO thread_id;
ALTER TABLE message RENAME COLUMN matrix_message_id TO mxid;
ALTER TABLE message DROP CONSTRAINT message_team_id_channel_id_fkey;
ALTER TABLE message ADD CONSTRAINT message_portal_fkey FOREIGN KEY (team_id, channel_id)
    REFERENCES portal (team_id, channel_id) ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE message RENAME CONSTRAINT message_matrix_message_id_key TO message_mxid_unique;
ALTER TABLE message DROP CONSTRAINT message_pkey;
UPDATE message SET thread_id='' WHERE thread_id IS NULL;
ALTER TABLE message ALTER COLUMN thread_id SET NOT NULL;
ALTER TABLE message ADD COLUMN part_id TEXT NOT NULL DEFAULT '';
ALTER TABLE message ALTER COLUMN part_id DROP DEFAULT;
ALTER TABLE message ADD PRIMARY KEY (team_id, channel_id, message_id, part_id);

INSERT INTO message (team_id, channel_id, message_id, part_id, thread_id, author_id, mxid)
SELECT team_id,
       channel_id,
       slack_message_id,
       'file:0:' || slack_file_id,
       COALESCE(slack_thread_id, ''),
       COALESCE(
           (SELECT author_id
            FROM message targetmsg
            WHERE targetmsg.team_id = attachment.team_id
              AND targetmsg.channel_id = attachment.channel_id
              AND targetmsg.message_id = attachment.slack_message_id),
           ''
       ),
       matrix_event_id
FROM attachment;
DROP TABLE attachment;

ALTER TABLE reaction RENAME COLUMN slack_message_id TO message_id;
ALTER TABLE reaction RENAME COLUMN matrix_event_id TO mxid;
ALTER TABLE reaction RENAME COLUMN slack_name TO emoji_id;
ALTER TABLE reaction DROP COLUMN matrix_name;
ALTER TABLE reaction DROP COLUMN matrix_url;
ALTER TABLE reaction RENAME CONSTRAINT reaction_matrix_event_id_key TO reaction_mxid_unique;
ALTER TABLE reaction DROP CONSTRAINT reaction_slack_name_author_id_slack_message_id_team_id_chan_key;
ALTER TABLE reaction DROP CONSTRAINT reaction_team_id_channel_id_fkey;
ALTER TABLE reaction ADD PRIMARY KEY (team_id, channel_id, message_id, author_id, emoji_id);
ALTER TABLE reaction ADD COLUMN msg_first_part_id TEXT;
UPDATE reaction SET msg_first_part_id=(
    SELECT part_id
    FROM message
    WHERE message.team_id = reaction.team_id
      AND message.channel_id = reaction.channel_id
      AND message.message_id = reaction.message_id
    ORDER BY part_id
    LIMIT 1
);
DELETE FROM reaction WHERE msg_first_part_id IS NULL;
ALTER TABLE reaction ALTER COLUMN msg_first_part_id SET NOT NULL;
ALTER TABLE reaction ADD CONSTRAINT reaction_portal_fkey FOREIGN KEY (team_id, channel_id) REFERENCES portal (team_id, channel_id)
    ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE reaction ADD CONSTRAINT reaction_message_fkey FOREIGN KEY (team_id, channel_id, message_id, msg_first_part_id)
    REFERENCES message(team_id, channel_id, message_id, part_id)
    ON DELETE CASCADE ON UPDATE CASCADE;

ALTER TABLE portal ALTER COLUMN type SET NOT NULL;
ALTER TABLE portal ALTER COLUMN name_set SET NOT NULL;
ALTER TABLE portal ALTER COLUMN topic_set SET NOT NULL;
ALTER TABLE portal ALTER COLUMN avatar_set SET NOT NULL;
ALTER TABLE portal RENAME COLUMN avatar_url TO avatar_mxc;
ALTER TABLE portal ALTER COLUMN in_space SET NOT NULL;
ALTER TABLE portal RENAME CONSTRAINT portal_mxid_key TO portal_mxid_unique;
ALTER TABLE portal ADD COLUMN receiver TEXT NOT NULL DEFAULT '';
ALTER TABLE portal ALTER COLUMN receiver DROP DEFAULT;
ALTER TABLE portal ADD COLUMN more_to_backfill BOOLEAN NOT NULL DEFAULT false;
UPDATE portal SET more_to_backfill=true WHERE more_to_backfill=false AND EXISTS(
    SELECT 1 FROM backfill_state bs
    WHERE bs.team_id=portal.team_id
      AND bs.channel_id=portal.channel_id
      AND backfill_complete=false
);

ALTER TABLE portal DROP COLUMN first_event_id;
ALTER TABLE portal DROP COLUMN next_batch_id;

ALTER TABLE puppet ALTER COLUMN name_set SET NOT NULL;
ALTER TABLE puppet ALTER COLUMN avatar_set SET NOT NULL;
UPDATE puppet SET avatar='' WHERE avatar IS NULL;
ALTER TABLE puppet ALTER COLUMN avatar SET NOT NULL;
ALTER TABLE puppet RENAME COLUMN avatar_url TO avatar_mxc;
ALTER TABLE puppet DROP COLUMN enable_presence;
ALTER TABLE puppet DROP COLUMN enable_receipts;
ALTER TABLE puppet DROP COLUMN next_batch;
ALTER TABLE puppet DROP COLUMN custom_mxid;
ALTER TABLE puppet DROP COLUMN access_token;

ALTER TABLE team_info RENAME TO team_portal;
UPDATE team_portal SET team_domain='' WHERE team_domain IS NULL;
UPDATE team_portal SET team_url='' WHERE team_url IS NULL;
UPDATE team_portal SET team_name='' WHERE team_name IS NULL;
UPDATE team_portal SET avatar='' WHERE avatar IS NULL;
ALTER TABLE team_portal ALTER COLUMN team_domain SET NOT NULL;
ALTER TABLE team_portal ALTER COLUMN team_url SET NOT NULL;
ALTER TABLE team_portal ALTER COLUMN team_name SET NOT NULL;
ALTER TABLE team_portal ALTER COLUMN name_set SET NOT NULL;
ALTER TABLE team_portal ALTER COLUMN avatar_set SET NOT NULL;
ALTER TABLE team_portal RENAME COLUMN team_id TO id;
ALTER TABLE team_portal RENAME COLUMN team_domain TO domain;
ALTER TABLE team_portal RENAME COLUMN team_url TO url;
ALTER TABLE team_portal RENAME COLUMN team_name TO name;
ALTER TABLE team_portal RENAME COLUMN space_room TO mxid;
ALTER TABLE team_portal ALTER COLUMN avatar SET NOT NULL;
ALTER TABLE team_portal RENAME COLUMN avatar_url TO avatar_mxc;
ALTER TABLE team_portal DROP CONSTRAINT team_info_team_id_key;
ALTER TABLE team_portal ADD PRIMARY KEY (id);
ALTER TABLE team_portal ADD CONSTRAINT team_portal_mxid_unique UNIQUE (mxid);

INSERT INTO team_portal (id, domain, url, name, avatar)
SELECT DISTINCT team_id, '', '', '', '' FROM user_team
ON CONFLICT (id) DO NOTHING;
INSERT INTO team_portal (id, domain, url, name, avatar)
SELECT DISTINCT team_id, '', '', '', '' FROM portal
ON CONFLICT (id) DO NOTHING;
ALTER TABLE portal ADD CONSTRAINT portal_team_fkey FOREIGN KEY (team_id) REFERENCES team_portal(id)
    ON DELETE CASCADE ON UPDATE CASCADE;

ALTER TABLE user_team RENAME COLUMN mxid TO user_mxid;
ALTER TABLE user_team ADD CONSTRAINT user_team_user_fkey FOREIGN KEY (user_mxid) REFERENCES "user"(mxid)
    ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE user_team ADD CONSTRAINT user_team_team_fkey FOREIGN KEY (team_id) REFERENCES team_portal(id)
    ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE user_team RENAME COLUMN slack_id TO user_id;
ALTER TABLE user_team RENAME COLUMN slack_email TO email;
ALTER TABLE user_team ALTER COLUMN in_space SET NOT NULL;
UPDATE user_team SET token='' WHERE token IS NULL;
UPDATE user_team SET cookie_token='' WHERE cookie_token IS NULL;
ALTER TABLE user_team ALTER COLUMN token SET NOT NULL;
ALTER TABLE user_team ALTER COLUMN cookie_token SET NOT NULL;
ALTER TABLE user_team DROP COLUMN team_name;
ALTER TABLE user_team_portal DROP CONSTRAINT user_team_portal_slack_team_id_portal_channel_id_fkey;
ALTER TABLE user_team_portal DROP CONSTRAINT user_team_portal_matrix_user_id_slack_user_id_slack_team_i_fkey;
ALTER TABLE user_team DROP CONSTRAINT user_team_pkey;
ALTER TABLE user_team ADD PRIMARY KEY (team_id, user_id);
ALTER TABLE user_team ADD CONSTRAINT user_team_mxid_unique UNIQUE(team_id, user_mxid);
ALTER TABLE user_team_portal RENAME COLUMN matrix_user_id TO user_mxid;
ALTER TABLE user_team_portal RENAME COLUMN slack_user_id TO user_id;
ALTER TABLE user_team_portal RENAME COLUMN slack_team_id TO team_id;
ALTER TABLE user_team_portal RENAME COLUMN portal_channel_id TO channel_id;
DELETE FROM user_team_portal out
WHERE EXISTS (
    SELECT FROM user_team_portal inn
    WHERE inn.team_id=out.team_id
      AND inn.user_id=out.user_id
      AND inn.channel_id=out.channel_id
      AND inn.ctid < out.ctid
);
ALTER TABLE user_team_portal ADD PRIMARY KEY (team_id, user_id, channel_id);
ALTER TABLE user_team_portal ADD CONSTRAINT utp_user_fkey FOREIGN KEY (user_mxid) REFERENCES "user"(mxid)
    ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE user_team_portal ADD CONSTRAINT utp_ut_fkey FOREIGN KEY (team_id, user_id) REFERENCES user_team(team_id, user_id)
    ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE user_team_portal ADD CONSTRAINT utp_portal_fkey FOREIGN KEY (team_id, channel_id) REFERENCES portal(team_id, channel_id)
    ON DELETE CASCADE ON UPDATE CASCADE;
CREATE INDEX user_team_user_idx ON user_team (user_mxid);
CREATE INDEX user_team_portal_user_idx ON user_team_portal (user_mxid);
CREATE INDEX user_team_portal_portal_idx ON user_team_portal (team_id, channel_id);
ALTER TABLE user_team_portal ADD COLUMN backfill_finished BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE user_team_portal ADD COLUMN backfill_priority INTEGER NOT NULL DEFAULT 0;
ALTER TABLE user_team_portal ADD COLUMN backfilled_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE user_team_portal ADD COLUMN backfill_dispatched_at BIGINT;
ALTER TABLE user_team_portal ADD COLUMN backfill_completed_at BIGINT;
ALTER TABLE user_team_portal ADD COLUMN backfill_cooldown_until BIGINT;

ALTER TABLE emoji RENAME COLUMN slack_team TO team_id;
ALTER TABLE emoji RENAME COLUMN slack_id TO emoji_id;
ALTER TABLE emoji RENAME COLUMN image_url TO image_mxc;
ALTER TABLE emoji ADD COLUMN value TEXT NOT NULL DEFAULT '';
ALTER TABLE emoji ALTER COLUMN value DROP DEFAULT;
ALTER TABLE emoji DROP CONSTRAINT emoji_pkey;
ALTER TABLE emoji ADD PRIMARY KEY (team_id, emoji_id);

ALTER TABLE "user" ADD COLUMN access_token TEXT;
