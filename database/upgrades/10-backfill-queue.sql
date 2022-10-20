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
    dispatch_time    TIMESTAMP,
    completed_at     TIMESTAMP,
    batch_delay      INTEGER,
    max_batch_events INTEGER NOT NULL,
    max_total_events INTEGER,

    FOREIGN KEY (team_id, channel_id) REFERENCES portal(team_id, channel_id) ON DELETE CASCADE
);

CREATE TABLE backfill_state (
    team_id           TEXT,
    channel_id        TEXT,
    processing_batch  BOOLEAN,
    backfill_complete BOOLEAN,
    PRIMARY KEY (team_id, channel_id),
    FOREIGN KEY (team_id, channel_id) REFERENCES portal (team_id, channel_id) ON DELETE CASCADE
);
