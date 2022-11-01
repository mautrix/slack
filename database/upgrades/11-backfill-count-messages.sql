-- v11: Count messages for backfill state

ALTER TABLE backfill_state DROP COLUMN processing_batch;
ALTER TABLE backfill_state ADD dispatched BOOLEAN;
ALTER TABLE backfill_state ADD message_count INTEGER;
ALTER TABLE backfill_state ADD immediate_complete BOOLEAN;
DROP TABLE backfill_queue;
