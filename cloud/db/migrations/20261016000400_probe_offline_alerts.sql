-- migrate:up
ALTER TABLE probes ADD COLUMN offline_alerted_at TIMESTAMPTZ;

-- migrate:down
ALTER TABLE probes DROP COLUMN offline_alerted_at;
