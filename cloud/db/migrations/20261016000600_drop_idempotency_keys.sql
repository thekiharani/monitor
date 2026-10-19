-- migrate:up
DROP TABLE idempotency_keys;

-- migrate:down
CREATE TABLE idempotency_keys (
    account_id UUID NOT NULL REFERENCES accounts(id),
    key VARCHAR(128) NOT NULL,
    method VARCHAR(16) NOT NULL,
    path TEXT NOT NULL,
    request_hash BYTEA NOT NULL,
    response_status SMALLINT NOT NULL,
    response_body JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (account_id, key)
);
CREATE INDEX idempotency_keys_expires_idx ON idempotency_keys(expires_at);
ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant ON idempotency_keys USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
GRANT SELECT, INSERT, UPDATE, DELETE ON idempotency_keys TO monitor_app;
