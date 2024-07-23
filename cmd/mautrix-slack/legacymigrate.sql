INSERT INTO portal (
    bridge_id, id, receiver, mxid, parent_id, parent_receiver, relay_bridge_id, relay_login_id,
    name, topic, avatar_id, avatar_hash, avatar_mxc, name_set, avatar_set, topic_set, in_space, room_type,
    metadata
)
SELECT
    '', -- bridge_id
    team_id, -- id
    '', -- receiver
    space_room,
    NULL, -- parent_id
    '', -- parent_receiver
    NULL, -- relay_bridge_id
    NULL, -- relay_login_id
    team_name,
    '', -- topic
    COALESCE(avatar, ''), -- avatar_id,
    '', -- avatar_hash
    COALESCE(avatar_url, ''), -- avatar_mxc
    name_set,
    avatar_set,
    false, -- topic_set
    false, -- in_space
    'space', -- room_type
    '{}' -- metadata
FROM team_info_old;

INSERT INTO portal (
    bridge_id, id, receiver, mxid, parent_id, parent_receiver, relay_bridge_id, relay_login_id, other_user_id,
    name, topic, avatar_id, avatar_hash, avatar_mxc, name_set, avatar_set, topic_set, name_is_custom, in_space, room_type,
    metadata
)
SELECT
    '', -- bridge_id
    team_id || '-' || channel_id, -- id
    '', -- receiver
    mxid,
    team_id, -- parent_id
    '', -- parent_receiver
    NULL, -- relay_bridge_id
    NULL, -- relay_login_id
    CASE WHEN type=2 THEN LOWER(team_id || '-' || dm_user_id) END, -- other_user_id
    name,
    topic,
    avatar, -- avatar_id,
    '', -- avatar_hash
    COALESCE(avatar_url, ''), -- avatar_mxc
    name_set,
    avatar_set,
    topic_set,
    CASE WHEN type=2 THEN false ELSE true END, -- name_is_custom
    in_space,
    CASE
        WHEN type=2 THEN 'dm'
        WHEN type=3 THEN 'group_dm'
        ELSE ''
    END, -- room_type
    '{}' -- metadata
FROM portal_old;

INSERT INTO ghost (
    bridge_id, id, name, avatar_id, avatar_hash, avatar_mxc,
    name_set, avatar_set, contact_info_set, is_bot, identifiers, metadata
)
SELECT
    '', -- bridge_id
    lower(team_id || '-' || user_id), -- id
    name,
    COALESCE(avatar, ''), -- avatar_id
    '', -- avatar_hash
    COALESCE(avatar_url, ''), -- avatar_mxc
    name_set,
    avatar_set,
    contact_info_set, -- contact_info_set
    is_bot, -- is_bot
    '[]', -- identifiers
    '{}' -- metadata
FROM puppet_old
WHERE user_id=UPPER(user_id);

INSERT INTO message (
    bridge_id, id, part_id, mxid, room_id, room_receiver, sender_id,
    sender_mxid, timestamp, edit_count,
    thread_root_id, reply_to_id, reply_to_part_id, metadata
)
SELECT
    '', -- bridge_id
    team_id || '-' || channel_id || '-' || slack_message_id, -- id
    '', -- part_id
    matrix_message_id, -- mxid
    team_id || '-' || channel_id, -- room_id
    '', -- room_receiver
    lower(team_id || '-' || author_id), -- sender_id
    '', -- sender_mxid (not available)
    CAST(CAST(slack_message_id AS FLOAT) * 1000000000 AS BIGINT), -- timestamp
    0, -- edit_count
    CASE WHEN slack_thread_id<>'' THEN
        team_id || '-' || channel_id || '-' || slack_thread_id
    END, -- thread_root_id
    NULL, -- reply_to_id
    NULL, -- reply_to_part_id
    '{}' -- metadata
FROM message_old;

-- Insert fake ghost because attachments don't have senders
INSERT INTO ghost (
    bridge_id, id, name, avatar_id, avatar_hash, avatar_mxc,
    name_set, avatar_set, contact_info_set, is_bot, identifiers, metadata
) VALUES (
    '', '', '', '', '', '', false, false, false, false, '[]', '{}'
);

INSERT INTO message (
    bridge_id, id, part_id, mxid, room_id, room_receiver, sender_id,
    sender_mxid, timestamp, edit_count,
    thread_root_id, reply_to_id, reply_to_part_id, metadata
)
SELECT
    '', -- bridge_id
    team_id || '-' || channel_id || '-' || slack_message_id, -- id
    'file-0-' || slack_file_id, -- part_id
    matrix_event_id, -- mxid
    team_id || '-' || channel_id, -- room_id
    '', -- room_receiver
    '', -- sender_id TODO find correct sender
    '', -- sender_mxid (not available)
    CAST(CAST(slack_message_id AS FLOAT) * 1000000000 AS BIGINT), -- timestamp
    0, -- edit_count
    CASE WHEN slack_thread_id<>'' THEN
        team_id || '-' || channel_id || '-' || slack_thread_id
    END, -- thread_root_id
    NULL, -- reply_to_id
    NULL, -- reply_to_part_id
    '{}' -- metadata
FROM attachment_old
WHERE true
-- hack to prevent exploding when there are multiple rows with the same file_id
ON CONFLICT (bridge_id, room_receiver, id, part_id) DO NOTHING;

UPDATE message
SET part_id=''
FROM (SELECT id, COUNT(*) AS count FROM message GROUP BY id HAVING COUNT(*) = 1) as pc
WHERE pc.count = 1 AND message.id = pc.id;

INSERT INTO reaction (
    bridge_id, message_id, message_part_id, sender_id, emoji_id, emoji,
    room_id, room_receiver, mxid, timestamp, metadata
)
SELECT
    '', -- bridge_id
    team_id || '-' || channel_id || '-' || slack_message_id, -- message_id
    '', -- message_part_id
    lower(team_id || '-' || author_id), -- sender_id
    slack_name, -- emoji_id
    matrix_name, -- emoji
    team_id || '-' || channel_id, -- room_id
    '', -- room_receiver
    matrix_event_id, -- mxid
    CAST(CAST(slack_message_id AS FLOAT) * 1000000000 AS BIGINT), -- timestamp
    '{}' -- metadata
FROM reaction_old
WHERE EXISTS(SELECT 1
                 FROM message
                 WHERE message.id = team_id || '-' || channel_id || '-' || slack_message_id
                   AND message.bridge_id = ''
                   AND message.part_id = ''
                   AND message.room_receiver = '');

INSERT INTO "user" (bridge_id, mxid, management_room, access_token)
SELECT '', mxid, management_room, NULL FROM user_old;

INSERT INTO user_login (bridge_id, user_mxid, id, remote_name, space_room, metadata)
SELECT
    '', -- bridge_id
    mxid, -- user_mxid
    team_id || '-' || slack_id, -- id
    team_name || ' - ' || slack_email, -- remote_name
    NULL, -- space_room
    -- only: postgres
    jsonb_build_object
    -- only: sqlite (line commented)
--  json_object
    ('token', token, 'cookie_token', cookie_token, 'email', slack_email) -- metadata
FROM user_team_old;

INSERT INTO user_portal (bridge_id, user_mxid, login_id, portal_id, portal_receiver, in_space, preferred, last_read)
SELECT DISTINCT
    '', -- bridge_id
    matrix_user_id, -- user_mxid
    slack_team_id || '-' || slack_user_id, -- login_id
    slack_team_id || '-' || portal_channel_id, -- portal_id
    '', -- portal_receiver
    false, -- in_space
    false, -- preferred
    -- only: postgres
    CAST(NULL AS BIGINT) -- last_read
    -- only: sqlite (line commented)
--  NULL -- last_read
FROM user_team_portal_old;

UPDATE portal
SET receiver=ul.user_login_id
FROM (SELECT team_id || '-' || slack_id AS user_login_id, team_id AS parent_id FROM user_team_old) ul
WHERE room_type IN ('dm', 'group_dm') AND portal.parent_id=ul.parent_id;

CREATE TABLE emoji (
    team_id   TEXT NOT NULL,
    emoji_id  TEXT NOT NULL,
    value     TEXT NOT NULL,
    alias     TEXT,
    image_mxc TEXT,

    PRIMARY KEY (team_id, emoji_id)
);

INSERT INTO emoji (team_id, emoji_id, value, alias, image_mxc)
SELECT slack_team, slack_id, '', alias, image_url
FROM emoji_old;

CREATE TABLE slack_version (version INTEGER, compat INTEGER);
INSERT INTO slack_version (version, compat) VALUES (1, 1);

DROP TABLE reaction_old;
DROP TABLE message_old;
DROP TABLE attachment_old;
DROP TABLE user_team_portal_old;
DROP TABLE backfill_state_old;
DROP TABLE portal_old;
DROP TABLE team_info_old;
DROP TABLE user_team_old;
DROP TABLE puppet_old;
DROP TABLE user_old;
DROP TABLE emoji_old;
