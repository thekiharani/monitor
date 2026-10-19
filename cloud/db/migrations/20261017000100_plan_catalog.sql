-- migrate:up
CREATE TABLE plan_catalog (
 plan VARCHAR(16) PRIMARY KEY CHECK (plan IN ('free','starter','pro','business')),
 definition JSONB NOT NULL CHECK (jsonb_typeof(definition)='object'),
 version BIGINT NOT NULL DEFAULT 1 CHECK (version>0),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 editor_id UUID REFERENCES principals(id) ON DELETE SET NULL
);
ALTER TABLE billing_checkouts ADD COLUMN provider_plan_code VARCHAR(128);
ALTER TABLE billing_subscriptions ADD COLUMN provider_plan_code VARCHAR(128);

-- migrate:down
ALTER TABLE billing_subscriptions DROP COLUMN provider_plan_code;
ALTER TABLE billing_checkouts DROP COLUMN provider_plan_code;
DROP TABLE plan_catalog;
