-- v2: Add thread support

ALTER TABLE message ADD COLUMN slack_thread_id TEXT;
