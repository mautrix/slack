-- v9: Add next batch ID to portal

ALTER TABLE portal ADD next_batch_id TEXT;
ALTER TABLE portal ADD first_slack_id TEXT;
