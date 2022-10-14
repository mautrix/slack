-- v10: Add backfill queue

CREATE TABLE backfill_queue (
    queue_id INTEGER PRIMARY KEY
        -- only: postgres
        GENERATED ALWAYS AS IDENTITY
    ,
    type             INTEGER NOT NULL,
    priority         INTEGER NOT NULL,
    team_id          TEXT,
    channel_id       TEXT,
    time_start       TIMESTAMP,
    time_end         TIMESTAMP,
    completed_at     TIMESTAMP,
    batch_delay      INTEGER,
    max_batch_events INTEGER NOT NULL,
    max_total_events INTEGER,

    FOREIGN KEY (team_id, channel_id) REFERENCES portal(team_id, channel_id) ON DELETE CASCADE
);

CREATE TABLE history_sync_conversation (
    team_id                      TEXT,
    channel_id                   TEXT,
    last_message_id              TEXT,
    marked_as_unread             BOOLEAN,
    unread_count                 INTEGER,
    PRIMARY KEY (conversation_id),
    FOREIGN KEY (team_id, channel_id) REFERENCES portal (team_id, channel_id) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE history_sync_message (
    team_id         TEXT,
    channel_id      TEXT,
    message_id      TEXT,
    data            BYTEA,
    inserted_time   TIMESTAMP,
    PRIMARY KEY (team_id, channel_id, message_id),
    FOREIGN KEY (team_id, channel_id) REFERENCES history_sync_conversation (team_id, channel_id) ON DELETE CASCADE
);
