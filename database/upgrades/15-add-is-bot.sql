-- v15: Add is_bot field for puppets

ALTER TABLE puppet ADD COLUMN is_bot BOOLEAN NOT NULL DEFAULT false;
