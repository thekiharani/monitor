-- migrate:up
ALTER TABLE monitors ADD COLUMN request_encrypted TEXT;
ALTER TABLE monitors DROP COLUMN headers, DROP COLUMN body;

-- migrate:down
ALTER TABLE monitors ADD COLUMN headers JSONB NOT NULL DEFAULT '{}', ADD COLUMN body TEXT;
ALTER TABLE monitors ADD CONSTRAINT monitors_body_length CHECK (body IS NULL OR char_length(body) <= 16384);
ALTER TABLE monitors DROP COLUMN request_encrypted;
