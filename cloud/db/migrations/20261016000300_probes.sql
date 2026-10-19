-- migrate:up
CREATE TABLE probes (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    region VARCHAR(32) NOT NULL,
    name VARCHAR(64) NOT NULL,
    key_prefix VARCHAR(32) NOT NULL,
    secret_hash BYTEA NOT NULL,
    creator_id UUID REFERENCES principals(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ,
    last_ip INET,
    version VARCHAR(64),
    revoked_at TIMESTAMPTZ,
    revoker_id UUID REFERENCES principals(id),
    CONSTRAINT probes_name_length CHECK (char_length(btrim(name)) BETWEEN 1 AND 64),
    CONSTRAINT probes_region_format CHECK (region ~ '^[a-z0-9][a-z0-9-]{0,31}$')
);
CREATE UNIQUE INDEX probes_secret_idx ON probes(secret_hash);
CREATE INDEX probes_region_idx ON probes(region) WHERE revoked_at IS NULL;

-- migrate:down
DROP TABLE probes;
