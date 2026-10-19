-- migrate:up
ALTER TABLE monitor_schedules
    ADD COLUMN claimant_id UUID REFERENCES probes(id),
    ADD COLUMN claim_expires_at TIMESTAMPTZ;

-- migrate:down
ALTER TABLE monitor_schedules DROP COLUMN claim_expires_at, DROP COLUMN claimant_id;
