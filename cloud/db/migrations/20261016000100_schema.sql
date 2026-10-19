-- migrate:up
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE FUNCTION monitor_current_account() RETURNS uuid
    LANGUAGE sql STABLE PARALLEL SAFE
    AS $$ SELECT nullif(current_setting('monitor.account_id', true), '')::uuid $$;

CREATE FUNCTION monitor_current_principal() RETURNS uuid
    LANGUAGE sql STABLE PARALLEL SAFE
    AS $$ SELECT nullif(current_setting('monitor.principal_id', true), '')::uuid $$;

CREATE TABLE principals (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    kind VARCHAR(16) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    display_name VARCHAR(256),
    timezone VARCHAR(64),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT principals_display_name_length CHECK (
        display_name IS NULL OR char_length(btrim(display_name)) BETWEEN 1 AND 128
    ),
    CONSTRAINT principals_timezone_length CHECK (
        timezone IS NULL OR char_length(timezone) BETWEEN 1 AND 64
    )
);

CREATE TABLE principal_identities (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    principal_id UUID NOT NULL REFERENCES principals(id),
    provider VARCHAR(32) NOT NULL,
    provider_subject VARCHAR(256) NOT NULL,
    verified_email VARCHAR(256),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    CONSTRAINT principal_identities_email_length CHECK (
        verified_email IS NULL OR char_length(verified_email) BETWEEN 3 AND 254
    )
);

CREATE INDEX principal_identities_principal_idx ON principal_identities(principal_id) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX principal_identities_provider_subject_active_idx ON principal_identities(provider, provider_subject) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX principal_identities_verified_email_active_idx ON principal_identities(verified_email) WHERE deleted_at IS NULL;
CREATE INDEX principal_identities_email_trgm_idx ON principal_identities USING gin (verified_email gin_trgm_ops) WHERE verified_email IS NOT NULL AND deleted_at IS NULL;

CREATE TABLE accounts (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    name VARCHAR(128),
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    plan VARCHAR(16) NOT NULL DEFAULT 'free',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    purged_at TIMESTAMPTZ,
    CONSTRAINT accounts_name_length CHECK (
        name IS NULL OR char_length(btrim(name)) BETWEEN 1 AND 128
    )
);

CREATE INDEX accounts_pending_purge_idx ON accounts(deleted_at) WHERE status = 'closed' AND purged_at IS NULL;

CREATE TABLE account_memberships (
    principal_id UUID NOT NULL REFERENCES principals(id),
    account_id UUID NOT NULL REFERENCES accounts(id),
    role VARCHAR(16) NOT NULL DEFAULT 'owner',
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    PRIMARY KEY (principal_id, account_id)
);

CREATE INDEX account_memberships_account_idx ON account_memberships(account_id);
CREATE INDEX account_memberships_account_active_idx ON account_memberships(account_id) WHERE deleted_at IS NULL;
CREATE INDEX account_memberships_principal_active_idx ON account_memberships(principal_id) WHERE status = 'active' AND deleted_at IS NULL;

CREATE TABLE account_invites (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    account_id UUID NOT NULL REFERENCES accounts(id),
    email VARCHAR(256) NOT NULL,
    role VARCHAR(16) NOT NULL DEFAULT 'member',
    token_hash BYTEA NOT NULL,
    inviter_id UUID NOT NULL REFERENCES principals(id),
    expires_at TIMESTAMPTZ NOT NULL,
    accepted_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT account_invites_email_lowercase CHECK (email = lower(email))
);

CREATE UNIQUE INDEX account_invites_pending_idx ON account_invites(account_id, email) WHERE accepted_at IS NULL AND revoked_at IS NULL;
CREATE UNIQUE INDEX account_invites_token_idx ON account_invites(token_hash);
CREATE INDEX account_invites_email_pending_idx ON account_invites(email) WHERE accepted_at IS NULL AND revoked_at IS NULL;

CREATE TABLE auth_challenges (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    email VARCHAR(256) NOT NULL,
    code_mac BYTEA NOT NULL,
    purpose VARCHAR(32) NOT NULL DEFAULT 'login',
    principal_id UUID REFERENCES principals(id),
    request_ip INET,
    attempts SMALLINT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT auth_challenges_purpose CHECK (purpose IN ('login', 'email_change')),
    CONSTRAINT auth_challenges_change_needs_principal CHECK (
        purpose <> 'email_change' OR principal_id IS NOT NULL
    )
);

CREATE INDEX auth_challenges_principal_created_idx ON auth_challenges(principal_id, created_at DESC) WHERE principal_id IS NOT NULL;
CREATE INDEX auth_challenges_email_created_idx ON auth_challenges(email, created_at DESC);
CREATE INDEX auth_challenges_ip_created_idx ON auth_challenges(request_ip, created_at DESC);
CREATE INDEX auth_challenges_expires_idx ON auth_challenges(expires_at);

CREATE TABLE credentials (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    principal_id UUID NOT NULL REFERENCES principals(id),
    account_id UUID REFERENCES accounts(id),
    kind VARCHAR(16) NOT NULL,
    audience VARCHAR(32) NOT NULL,
    name VARCHAR(64),
    key_prefix VARCHAR(32),
    secret_hash BYTEA NOT NULL,
    assurance VARCHAR(32) NOT NULL DEFAULT 'single_factor',
    created_ip INET,
    user_agent TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX credentials_secret_hash_idx ON credentials(secret_hash);
CREATE INDEX credentials_principal_idx ON credentials(principal_id, created_at DESC);
CREATE INDEX credentials_account_idx ON credentials(account_id, created_at DESC) WHERE account_id IS NOT NULL;
CREATE INDEX credentials_expires_idx ON credentials(expires_at);
CREATE INDEX credentials_revoked_idx ON credentials(revoked_at) WHERE revoked_at IS NOT NULL;
CREATE INDEX credentials_account_audience_active_idx ON credentials(account_id, audience) WHERE revoked_at IS NULL;

CREATE TABLE access_grants (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    principal_id UUID NOT NULL REFERENCES principals(id),
    capability VARCHAR(128) NOT NULL,
    scope_type VARCHAR(16) NOT NULL,
    scope_id UUID,
    grantor_id UUID REFERENCES principals(id),
    reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    revoker_id UUID REFERENCES principals(id),
    revocation_reason TEXT,
    CONSTRAINT access_grants_capability_length CHECK (char_length(capability) BETWEEN 3 AND 100),
    CONSTRAINT access_grants_reason_length CHECK (char_length(reason) BETWEEN 3 AND 1000)
);

CREATE UNIQUE INDEX access_grants_unique_idx ON access_grants(principal_id, capability, scope_type, scope_id) NULLS NOT DISTINCT;
CREATE INDEX access_grants_principal_idx ON access_grants(principal_id) WHERE revoked_at IS NULL;

CREATE TABLE audit_events (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    account_id UUID,
    principal_id UUID REFERENCES principals(id),
    credential_id UUID REFERENCES credentials(id),
    capability VARCHAR(128) NOT NULL,
    action VARCHAR(128) NOT NULL,
    target_type VARCHAR(128) NOT NULL,
    target_id VARCHAR(256),
    reason TEXT NOT NULL,
    before_state JSONB,
    after_state JSONB,
    request_id VARCHAR(128),
    source_ip INET,
    user_agent TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX audit_events_created_idx ON audit_events(created_at DESC);
CREATE INDEX audit_events_account_idx ON audit_events(account_id, created_at DESC) WHERE account_id IS NOT NULL;
CREATE INDEX audit_events_principal_idx ON audit_events(principal_id, created_at DESC);
CREATE INDEX audit_events_target_idx ON audit_events(target_type, target_id, created_at DESC);

CREATE FUNCTION reject_audit_event_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_events are append-only';
END;
$$;

CREATE TRIGGER audit_events_append_only
    BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION reject_audit_event_mutation();

CREATE TABLE account_monthly_usage (
    account_id UUID NOT NULL REFERENCES accounts(id),
    period DATE NOT NULL,
    api_requests BIGINT NOT NULL DEFAULT 0 CHECK (api_requests >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, period)
);

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

CREATE TABLE billing_checkouts (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    account_id UUID NOT NULL REFERENCES accounts(id),
    provider VARCHAR(32) NOT NULL,
    provider_reference VARCHAR(128) NOT NULL,
    provider_access_code VARCHAR(128),
    provider_transaction_id VARCHAR(128),
    plan VARCHAR(16) NOT NULL,
    amount_minor BIGINT NOT NULL,
    currency VARCHAR(16) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    checkout_type VARCHAR(16) NOT NULL DEFAULT 'subscription',
    billing_mode VARCHAR(16) NOT NULL DEFAULT 'auto',
    plan_amount_minor BIGINT NOT NULL DEFAULT 0,
    credit_minor BIGINT NOT NULL DEFAULT 0,
    source_subscription_code VARCHAR(128),
    upgrade_subscription_code VARCHAR(128),
    renewal_at TIMESTAMPTZ,
    authorization_url TEXT,
    provisioned_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT billing_checkouts_amount_positive CHECK (amount_minor > 0),
    CONSTRAINT billing_checkouts_plan_amount_nonnegative CHECK (plan_amount_minor >= 0),
    CONSTRAINT billing_checkouts_credit_nonnegative CHECK (credit_minor >= 0),
    CONSTRAINT billing_checkouts_credit_within_plan CHECK (credit_minor <= plan_amount_minor)
);
CREATE UNIQUE INDEX billing_checkouts_reference_idx ON billing_checkouts(provider_reference);
CREATE INDEX billing_checkouts_account_idx ON billing_checkouts(account_id, created_at DESC);
CREATE INDEX billing_checkouts_pending_idx ON billing_checkouts(created_at) WHERE status IN ('pending', 'initialized');
CREATE INDEX billing_checkouts_unprovisioned_idx ON billing_checkouts(created_at DESC) WHERE checkout_type = 'upgrade' AND status = 'successful' AND provisioned_at IS NULL;

CREATE TABLE billing_subscriptions (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    account_id UUID NOT NULL REFERENCES accounts(id),
    provider VARCHAR(32) NOT NULL,
    provider_customer_code VARCHAR(128),
    provider_subscription_code VARCHAR(128) NOT NULL,
    provider_email_token_encrypted TEXT,
    plan VARCHAR(16) NOT NULL,
    status VARCHAR(32) NOT NULL,
    billing_mode VARCHAR(16) NOT NULL DEFAULT 'auto',
    amount_minor BIGINT NOT NULL,
    currency VARCHAR(16) NOT NULL,
    current_period_start TIMESTAMPTZ,
    current_period_end TIMESTAMPTZ,
    grace_until TIMESTAMPTZ,
    cancel_at_period_end BOOLEAN NOT NULL DEFAULT false,
    effective_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    scheduled_plan VARCHAR(16),
    scheduled_plan_effective_at TIMESTAMPTZ,
    renewal_reminded_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT billing_subscriptions_amount_positive CHECK (
        CASE WHEN billing_mode = 'comp' THEN amount_minor = 0 ELSE amount_minor > 0 END
    ),
    CONSTRAINT billing_subscriptions_complimentary_has_no_provider CHECK (
        billing_mode <> 'comp' OR (
            provider = 'internal'
            AND provider_customer_code IS NULL
            AND provider_email_token_encrypted IS NULL
            AND scheduled_plan IS NULL
            AND cancel_at_period_end = false
        )
    )
);
CREATE UNIQUE INDEX billing_subscriptions_code_idx ON billing_subscriptions(provider_subscription_code);
CREATE INDEX billing_subscriptions_account_idx ON billing_subscriptions(account_id);
CREATE INDEX billing_subscriptions_effective_idx ON billing_subscriptions(account_id, effective_at, created_at DESC);
CREATE INDEX billing_subscriptions_reconcile_idx ON billing_subscriptions(status, provider_subscription_code) WHERE status IN ('active', 'past_due', 'non_renewing');
CREATE INDEX billing_subscriptions_manual_idx ON billing_subscriptions(account_id, plan) WHERE billing_mode = 'manual';
CREATE INDEX billing_subscriptions_scheduled_idx ON billing_subscriptions(scheduled_plan_effective_at) WHERE scheduled_plan IS NOT NULL;
CREATE UNIQUE INDEX billing_subscriptions_complimentary_idx ON billing_subscriptions(account_id) WHERE billing_mode = 'comp' AND status = 'active';
CREATE INDEX billing_subscriptions_complimentary_expiry_idx ON billing_subscriptions(current_period_end) WHERE billing_mode = 'comp' AND status = 'active' AND current_period_end IS NOT NULL;

CREATE TABLE billing_events (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    provider VARCHAR(32) NOT NULL,
    payload_hash BYTEA NOT NULL,
    event_type VARCHAR(128) NOT NULL,
    provider_reference VARCHAR(128),
    account_id UUID REFERENCES accounts(id),
    payload JSONB NOT NULL,
    status VARCHAR(16) NOT NULL,
    error_message TEXT,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX billing_events_payload_hash_idx ON billing_events(payload_hash);
CREATE INDEX billing_events_received_idx ON billing_events(received_at);
CREATE INDEX billing_events_account_idx ON billing_events(account_id, received_at DESC) WHERE account_id IS NOT NULL;

CREATE TABLE email_jobs (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    account_id UUID REFERENCES accounts(id),
    to_email VARCHAR(256) NOT NULL,
    template VARCHAR(128) NOT NULL,
    payload JSONB NOT NULL,
    attempts SMALLINT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    sent_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX email_jobs_pending_idx ON email_jobs(next_attempt_at) WHERE sent_at IS NULL;

CREATE UNLOGGED TABLE rate_buckets (
    bucket_key VARCHAR(128) PRIMARY KEY,
    tokens DOUBLE PRECISION NOT NULL,
    refilled_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE monitors (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    account_id UUID NOT NULL REFERENCES accounts(id),
    name VARCHAR(128) NOT NULL,
    kind VARCHAR(16) NOT NULL,
    target VARCHAR(2048),
    method VARCHAR(8) NOT NULL DEFAULT 'GET',
    headers JSONB NOT NULL DEFAULT '{}',
    body TEXT,
    expected_statuses SMALLINT[] NOT NULL DEFAULT '{}',
    keyword VARCHAR(256),
    keyword_absent BOOLEAN NOT NULL DEFAULT false,
    follow_redirects BOOLEAN NOT NULL DEFAULT true,
    interval_seconds INTEGER NOT NULL,
    timeout_ms INTEGER NOT NULL DEFAULT 10000,
    regions VARCHAR(32)[] NOT NULL,
    cert_warn_days SMALLINT NOT NULL DEFAULT 14,
    cert_warned_for TIMESTAMPTZ,
    heartbeat_token VARCHAR(64),
    grace_seconds INTEGER NOT NULL DEFAULT 0,
    state VARCHAR(16) NOT NULL DEFAULT 'pending',
    state_changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_checked_at TIMESTAMPTZ,
    last_status_code SMALLINT,
    last_response_ms REAL,
    last_error VARCHAR(512),
    cert_expires_at TIMESTAMPTZ,
    paused_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT monitors_name_length CHECK (char_length(btrim(name)) BETWEEN 1 AND 128),
    CONSTRAINT monitors_kind CHECK (kind IN ('http', 'keyword', 'tcp', 'heartbeat')),
    CONSTRAINT monitors_target_present CHECK ((kind = 'heartbeat') = (target IS NULL)),
    CONSTRAINT monitors_heartbeat_token CHECK ((kind = 'heartbeat') = (heartbeat_token IS NOT NULL)),
    CONSTRAINT monitors_grace_range CHECK (grace_seconds BETWEEN 0 AND 86400),
    CONSTRAINT monitors_state CHECK (state IN ('pending', 'up', 'down', 'paused')),
    CONSTRAINT monitors_interval_range CHECK (interval_seconds BETWEEN 30 AND 86400),
    CONSTRAINT monitors_timeout_range CHECK (timeout_ms BETWEEN 1000 AND 60000),
    CONSTRAINT monitors_cert_warn_range CHECK (cert_warn_days BETWEEN 0 AND 365),
    CONSTRAINT monitors_keyword_present CHECK (kind <> 'keyword' OR char_length(keyword) >= 1),
    CONSTRAINT monitors_body_length CHECK (body IS NULL OR char_length(body) <= 16384),
    CONSTRAINT monitors_regions_present CHECK (cardinality(regions) <= 16 AND (kind = 'heartbeat' OR cardinality(regions) >= 1))
);

CREATE INDEX monitors_account_idx ON monitors(account_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX monitors_heartbeat_token_idx ON monitors(heartbeat_token) WHERE heartbeat_token IS NOT NULL;
CREATE INDEX monitors_heartbeat_due_idx ON monitors(last_checked_at) WHERE kind = 'heartbeat' AND deleted_at IS NULL AND paused_at IS NULL;

CREATE TABLE monitor_schedules (
    monitor_id UUID NOT NULL REFERENCES monitors(id),
    account_id UUID NOT NULL REFERENCES accounts(id),
    region VARCHAR(32) NOT NULL,
    next_run_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    last_checked_at TIMESTAMPTZ,
    PRIMARY KEY (monitor_id, region)
);

CREATE INDEX monitor_schedules_due_idx ON monitor_schedules(region, next_run_at);
CREATE INDEX monitor_schedules_account_idx ON monitor_schedules(account_id);

CREATE TABLE check_results (
    monitor_id UUID NOT NULL REFERENCES monitors(id),
    account_id UUID NOT NULL REFERENCES accounts(id),
    region VARCHAR(32) NOT NULL,
    checked_at TIMESTAMPTZ NOT NULL,
    up BOOLEAN NOT NULL,
    status_code SMALLINT,
    total_ms REAL,
    dns_ms REAL,
    connect_ms REAL,
    tls_ms REAL,
    ttfb_ms REAL,
    error VARCHAR(512),
    cert_expires_at TIMESTAMPTZ,
    PRIMARY KEY (monitor_id, checked_at, region)
) PARTITION BY RANGE (checked_at);

CREATE INDEX check_results_account_idx ON check_results(account_id, checked_at DESC);

CREATE TABLE monitor_daily (
    monitor_id UUID NOT NULL REFERENCES monitors(id),
    account_id UUID NOT NULL REFERENCES accounts(id),
    day DATE NOT NULL,
    checks INTEGER NOT NULL DEFAULT 0 CHECK (checks >= 0),
    failures INTEGER NOT NULL DEFAULT 0 CHECK (failures >= 0),
    response_ms_sum DOUBLE PRECISION NOT NULL DEFAULT 0,
    response_count INTEGER NOT NULL DEFAULT 0 CHECK (response_count >= 0),
    PRIMARY KEY (monitor_id, day)
);

CREATE INDEX monitor_daily_account_day_idx ON monitor_daily(account_id, day);

CREATE TABLE incidents (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    monitor_id UUID NOT NULL REFERENCES monitors(id),
    account_id UUID NOT NULL REFERENCES accounts(id),
    started_at TIMESTAMPTZ NOT NULL,
    resolved_at TIMESTAMPTZ,
    cause VARCHAR(512) NOT NULL,
    regions_down VARCHAR(32)[] NOT NULL DEFAULT '{}',
    acknowledged_at TIMESTAMPTZ,
    acknowledger_id UUID REFERENCES principals(id),
    CONSTRAINT incidents_resolved_after_start CHECK (resolved_at IS NULL OR resolved_at >= started_at)
);

CREATE UNIQUE INDEX incidents_open_idx ON incidents(monitor_id) WHERE resolved_at IS NULL;
CREATE INDEX incidents_account_idx ON incidents(account_id, started_at DESC);
CREATE INDEX incidents_monitor_idx ON incidents(monitor_id, started_at DESC);

CREATE TABLE alert_channels (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    account_id UUID NOT NULL REFERENCES accounts(id),
    kind VARCHAR(16) NOT NULL,
    name VARCHAR(128) NOT NULL,
    config_encrypted TEXT NOT NULL,
    all_monitors BOOLEAN NOT NULL DEFAULT true,
    enabled BOOLEAN NOT NULL DEFAULT true,
    last_delivered_at TIMESTAMPTZ,
    last_error VARCHAR(512),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT alert_channels_kind CHECK (kind IN ('email', 'telegram', 'slack', 'webhook')),
    CONSTRAINT alert_channels_name_length CHECK (char_length(btrim(name)) BETWEEN 1 AND 128)
);

CREATE INDEX alert_channels_account_idx ON alert_channels(account_id, created_at) WHERE deleted_at IS NULL;

CREATE TABLE monitor_channels (
    monitor_id UUID NOT NULL REFERENCES monitors(id),
    channel_id UUID NOT NULL REFERENCES alert_channels(id),
    account_id UUID NOT NULL REFERENCES accounts(id),
    PRIMARY KEY (monitor_id, channel_id)
);

CREATE INDEX monitor_channels_channel_idx ON monitor_channels(channel_id);
CREATE INDEX monitor_channels_account_idx ON monitor_channels(account_id);

CREATE TABLE notifications (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    account_id UUID NOT NULL REFERENCES accounts(id),
    channel_id UUID NOT NULL REFERENCES alert_channels(id),
    monitor_id UUID REFERENCES monitors(id),
    incident_id UUID REFERENCES incidents(id),
    event VARCHAR(16) NOT NULL,
    payload JSONB NOT NULL,
    attempts SMALLINT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at TIMESTAMPTZ,
    last_error VARCHAR(512),
    provider_message_id VARCHAR(128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT notifications_event CHECK (event IN ('down', 'up', 'certificate', 'test'))
);

CREATE INDEX notifications_pending_idx ON notifications(next_attempt_at) WHERE sent_at IS NULL;
CREATE INDEX notifications_account_idx ON notifications(account_id, created_at DESC);
CREATE INDEX notifications_incident_idx ON notifications(incident_id, channel_id) WHERE incident_id IS NOT NULL;

CREATE TABLE maintenance_windows (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    account_id UUID NOT NULL REFERENCES accounts(id),
    title VARCHAR(256) NOT NULL,
    starts_at TIMESTAMPTZ NOT NULL,
    ends_at TIMESTAMPTZ NOT NULL,
    monitor_ids UUID[] NOT NULL DEFAULT '{}',
    creator_id UUID REFERENCES principals(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT maintenance_windows_title_length CHECK (char_length(btrim(title)) BETWEEN 1 AND 256),
    CONSTRAINT maintenance_windows_order CHECK (ends_at > starts_at)
);

CREATE INDEX maintenance_windows_account_idx ON maintenance_windows(account_id, starts_at DESC) WHERE deleted_at IS NULL;

CREATE TABLE status_pages (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    account_id UUID NOT NULL REFERENCES accounts(id),
    slug VARCHAR(48) NOT NULL,
    title VARCHAR(128) NOT NULL,
    description VARCHAR(512),
    homepage_url VARCHAR(2048),
    logo_url VARCHAR(2048),
    theme VARCHAR(8) NOT NULL DEFAULT 'auto',
    show_response_times BOOLEAN NOT NULL DEFAULT false,
    published BOOLEAN NOT NULL DEFAULT true,
    custom_domain VARCHAR(253),
    domain_verified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT status_pages_slug_format CHECK (slug ~ '^[a-z0-9]([a-z0-9-]{1,46}[a-z0-9])$'),
    CONSTRAINT status_pages_title_length CHECK (char_length(btrim(title)) BETWEEN 1 AND 128),
    CONSTRAINT status_pages_theme CHECK (theme IN ('auto', 'light', 'dark')),
    CONSTRAINT status_pages_domain_format CHECK (
        custom_domain IS NULL OR custom_domain ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$'
    ),
    CONSTRAINT status_pages_https_links CHECK (
        (homepage_url IS NULL OR homepage_url ~ '^https://') AND (logo_url IS NULL OR logo_url ~ '^https://')
    )
);

CREATE UNIQUE INDEX status_pages_slug_idx ON status_pages(slug) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX status_pages_domain_idx ON status_pages(custom_domain) WHERE deleted_at IS NULL AND custom_domain IS NOT NULL;
CREATE INDEX status_pages_account_idx ON status_pages(account_id, created_at) WHERE deleted_at IS NULL;

CREATE TABLE status_page_items (
    status_page_id UUID NOT NULL REFERENCES status_pages(id),
    monitor_id UUID NOT NULL REFERENCES monitors(id),
    account_id UUID NOT NULL REFERENCES accounts(id),
    group_name VARCHAR(128),
    display_name VARCHAR(128),
    position SMALLINT NOT NULL,
    PRIMARY KEY (status_page_id, monitor_id),
    CONSTRAINT status_page_items_position CHECK (position BETWEEN 0 AND 999)
);

CREATE INDEX status_page_items_monitor_idx ON status_page_items(monitor_id);
CREATE INDEX status_page_items_account_idx ON status_page_items(account_id);

CREATE TABLE status_posts (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    status_page_id UUID NOT NULL REFERENCES status_pages(id),
    account_id UUID NOT NULL REFERENCES accounts(id),
    kind VARCHAR(16) NOT NULL,
    title VARCHAR(256) NOT NULL,
    impact VARCHAR(16) NOT NULL DEFAULT 'minor',
    status VARCHAR(16) NOT NULL,
    starts_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ends_at TIMESTAMPTZ,
    resolved_at TIMESTAMPTZ,
    author_id UUID REFERENCES principals(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT status_posts_kind CHECK (kind IN ('incident', 'maintenance')),
    CONSTRAINT status_posts_impact CHECK (impact IN ('none', 'minor', 'major', 'critical')),
    CONSTRAINT status_posts_status CHECK (status IN ('investigating', 'identified', 'monitoring', 'resolved', 'scheduled', 'in_progress', 'completed')),
    CONSTRAINT status_posts_title_length CHECK (char_length(btrim(title)) BETWEEN 1 AND 256),
    CONSTRAINT status_posts_window CHECK (ends_at IS NULL OR ends_at > starts_at)
);

CREATE INDEX status_posts_page_idx ON status_posts(status_page_id, starts_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX status_posts_account_idx ON status_posts(account_id);

CREATE TABLE status_post_updates (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    status_post_id UUID NOT NULL REFERENCES status_posts(id),
    account_id UUID NOT NULL REFERENCES accounts(id),
    status VARCHAR(16) NOT NULL,
    body TEXT NOT NULL,
    author_id UUID REFERENCES principals(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT status_post_updates_body_length CHECK (char_length(btrim(body)) BETWEEN 1 AND 5000)
);

CREATE INDEX status_post_updates_post_idx ON status_post_updates(status_post_id, created_at);
CREATE INDEX status_post_updates_account_idx ON status_post_updates(account_id);

ALTER TABLE accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_invites ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_monthly_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing_checkouts ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing_subscriptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE email_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE principals ENABLE ROW LEVEL SECURITY;
ALTER TABLE principal_identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE monitors ENABLE ROW LEVEL SECURITY;
ALTER TABLE monitor_schedules ENABLE ROW LEVEL SECURITY;
ALTER TABLE check_results ENABLE ROW LEVEL SECURITY;
ALTER TABLE monitor_daily ENABLE ROW LEVEL SECURITY;
ALTER TABLE incidents ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_channels ENABLE ROW LEVEL SECURITY;
ALTER TABLE monitor_channels ENABLE ROW LEVEL SECURITY;
ALTER TABLE notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE maintenance_windows ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_pages ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_page_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_posts ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_post_updates ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant ON accounts USING (id = monitor_current_account()) WITH CHECK (id = monitor_current_account());
CREATE POLICY tenant ON account_memberships USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON account_invites USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON account_monthly_usage USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON credentials USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON idempotency_keys USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON billing_checkouts USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON billing_subscriptions USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON billing_events USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON email_jobs USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON monitors USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON monitor_schedules USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON check_results USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON monitor_daily USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON incidents USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON alert_channels USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON monitor_channels USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON notifications USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON maintenance_windows USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON status_pages USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON status_page_items USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON status_posts USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());
CREATE POLICY tenant ON status_post_updates USING (account_id = monitor_current_account()) WITH CHECK (account_id = monitor_current_account());

CREATE POLICY tenant ON principals FOR SELECT USING (
    EXISTS (
        SELECT 1 FROM account_memberships m
        WHERE m.principal_id = principals.id AND m.account_id = monitor_current_account()
    )
);

CREATE POLICY tenant ON principal_identities FOR SELECT USING (
    EXISTS (
        SELECT 1 FROM account_memberships m
        WHERE m.principal_id = principal_identities.principal_id AND m.account_id = monitor_current_account()
    )
);

CREATE POLICY self ON principals FOR UPDATE
    USING (
        id = monitor_current_principal()
        AND EXISTS (
            SELECT 1 FROM account_memberships m
            WHERE m.principal_id = principals.id AND m.account_id = monitor_current_account()
        )
    )
    WITH CHECK (
        id = monitor_current_principal()
        AND EXISTS (
            SELECT 1 FROM account_memberships m
            WHERE m.principal_id = principals.id AND m.account_id = monitor_current_account()
        )
    );

GRANT USAGE ON SCHEMA public TO monitor_app;
GRANT EXECUTE ON FUNCTION monitor_current_principal() TO monitor_app;
GRANT UPDATE (display_name, timezone, updated_at) ON principals TO monitor_app;
GRANT EXECUTE ON FUNCTION monitor_current_account() TO monitor_app;

GRANT SELECT, INSERT, UPDATE, DELETE ON
    accounts, account_memberships, account_invites, account_monthly_usage,
    credentials, idempotency_keys,
    billing_checkouts, billing_subscriptions, billing_events, email_jobs
TO monitor_app;

GRANT SELECT, INSERT, UPDATE, DELETE ON monitors, monitor_schedules, alert_channels, monitor_channels, maintenance_windows,
    status_pages, status_page_items, status_posts, status_post_updates TO monitor_app;
GRANT SELECT, INSERT ON notifications TO monitor_app;
GRANT SELECT, UPDATE ON incidents TO monitor_app;
GRANT SELECT ON principals, principal_identities, check_results, monitor_daily TO monitor_app;
GRANT INSERT ON audit_events TO monitor_app;

-- migrate:down
DROP FUNCTION IF EXISTS monitor_current_principal() CASCADE;
DROP FUNCTION IF EXISTS monitor_current_account() CASCADE;

DROP TABLE IF EXISTS status_post_updates;
DROP TABLE IF EXISTS status_posts;
DROP TABLE IF EXISTS status_page_items;
DROP TABLE IF EXISTS status_pages;
DROP TABLE IF EXISTS maintenance_windows;
DROP TABLE IF EXISTS notifications;
DROP TABLE IF EXISTS monitor_channels;
DROP TABLE IF EXISTS alert_channels;
DROP TABLE IF EXISTS incidents;
DROP TABLE IF EXISTS monitor_daily;
DROP TABLE IF EXISTS check_results;
DROP TABLE IF EXISTS monitor_schedules;
DROP TABLE IF EXISTS monitors;
DROP TABLE IF EXISTS rate_buckets;
DROP TABLE IF EXISTS email_jobs;
DROP TABLE IF EXISTS billing_events;
DROP TABLE IF EXISTS billing_subscriptions;
DROP TABLE IF EXISTS billing_checkouts;
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS account_monthly_usage;
DROP TABLE IF EXISTS audit_events;
DROP FUNCTION IF EXISTS reject_audit_event_mutation() CASCADE;
DROP TABLE IF EXISTS access_grants;
DROP TABLE IF EXISTS credentials;
DROP TABLE IF EXISTS auth_challenges;
DROP TABLE IF EXISTS account_invites;
DROP TABLE IF EXISTS account_memberships;
DROP TABLE IF EXISTS accounts;
DROP TABLE IF EXISTS principal_identities;
DROP TABLE IF EXISTS principals;
