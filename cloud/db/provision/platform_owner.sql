\set ON_ERROR_STOP on

\if :{?operator_email}
\else
\set operator_email 'thekiharani@gmail.com'
\endif

BEGIN;

SELECT i.principal_id AS operator_principal_id
FROM principal_identities i
JOIN principals p ON p.id = i.principal_id
WHERE lower(i.verified_email) = lower(:'operator_email')
  AND i.deleted_at IS NULL
  AND p.status = 'active'
  AND p.deleted_at IS NULL
\gset

\echo 'granting platform.owner to' :operator_email :operator_principal_id

CREATE TEMP TABLE platform_owner_capabilities (capability VARCHAR(128) PRIMARY KEY) ON COMMIT DROP;

INSERT INTO platform_owner_capabilities (capability) VALUES
    ('platform.accounts.read'),
    ('platform.accounts.suspend'),
    ('platform.accounts.close'),
    ('platform.monitors.read'),
    ('platform.monitors.moderate'),
    ('platform.probes.manage'),
    ('platform.members.read'),
    ('platform.members.remove'),
    ('platform.health.read'),
    ('platform.billing.read'),
    ('platform.billing.reconcile'),
    ('platform.billing.comp'),
    ('platform.plans.manage'),
    ('platform.credentials.read'),
    ('platform.credentials.revoke'),
    ('platform.usage.read'),
    ('platform.jobs.read'),
    ('platform.jobs.retry'),
    ('platform.access.read'),
    ('platform.access.grant'),
    ('platform.audit.read');

INSERT INTO access_grants (principal_id, capability, scope_type, scope_id, grantor_id, reason)
SELECT :'operator_principal_id'::uuid, capability, 'platform', NULL, :'operator_principal_id'::uuid,
       'initial platform owner provisioned with db/provision/platform_owner.sql'
FROM platform_owner_capabilities
ON CONFLICT (principal_id, capability, scope_type, scope_id) DO UPDATE SET
    revoked_at = NULL,
    revoker_id = NULL,
    revocation_reason = NULL,
    expires_at = NULL,
    reason = EXCLUDED.reason;

INSERT INTO audit_events (principal_id, capability, action, target_type, target_id, reason, after_state)
SELECT :'operator_principal_id'::uuid,
       'platform.access.grant',
       'access.granted',
       'principal',
       :'operator_principal_id',
       'initial platform owner provisioned with db/provision/platform_owner.sql',
       jsonb_build_object(
           'permission_set', 'platform.owner',
           'scope_type', 'platform',
           'email', :'operator_email',
           'capabilities', jsonb_agg(capability ORDER BY capability)
       )
FROM platform_owner_capabilities;

\if :{?mint_control_token}
SELECT 'nmon_ctl_' || encode(gen_random_bytes(32), 'hex') AS control_token \gset

INSERT INTO credentials (principal_id, kind, audience, name, key_prefix, secret_hash, assurance, expires_at)
VALUES (
    :'operator_principal_id'::uuid,
    'api_key',
    'control_api',
    'Production control CLI',
    left(:'control_token', 13),
    digest(:'control_token', 'sha256'),
    'multi_factor',
    now() + interval '90 days'
);

INSERT INTO audit_events (principal_id, credential_id, capability, action, target_type, target_id, reason, after_state)
SELECT :'operator_principal_id'::uuid,
       c.id,
       'platform.access.grant',
       'credential.minted',
       'principal',
       :'operator_principal_id',
       'initial control CLI credential provisioned with db/provision/platform_owner.sql',
       jsonb_build_object('audience', c.audience, 'assurance', c.assurance, 'name', c.name, 'expires_at', c.expires_at)
FROM credentials c
WHERE c.secret_hash = digest(:'control_token', 'sha256');
\endif

COMMIT;

SELECT i.verified_email, a.id AS account_id, a.status AS account_status, a.plan, m.role
FROM principal_identities i
JOIN account_memberships m ON m.principal_id = i.principal_id AND m.deleted_at IS NULL
JOIN accounts a ON a.id = m.account_id
WHERE i.principal_id = :'operator_principal_id'::uuid;

SELECT capability, scope_type, created_at
FROM access_grants
WHERE principal_id = :'operator_principal_id'::uuid
  AND scope_type = 'platform'
  AND revoked_at IS NULL
  AND (expires_at IS NULL OR expires_at > now())
ORDER BY capability;

\if :{?mint_control_token}
\set control_token_export 'export MONITOR_CONTROL_TOKEN=' :control_token
\echo ''
\echo :control_token_export
\echo 'store it now; it is not recoverable and expires in 90 days'
\endif
