-- v8: Add thread ID to attachment messages

ALTER TABLE attachment ADD slack_thread_id TEXT;
