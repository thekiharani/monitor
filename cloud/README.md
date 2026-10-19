# Monitor Cloud

Noria Monitor is a multi-tenant uptime-monitoring service. Accounts add HTTP, keyword, TCP and heartbeat monitors. Probes in several regions run the checks. When a failure is confirmed, an incident opens and alerts go out by email, Telegram, Slack or a signed webhook. Each account can publish public status pages on our domain or its own.

Sign-in uses emailed one-time codes. Accounts have Free, Starter, Pro and Business plans, team members with roles, and audience-bound API keys. Operators use a capability-authorized control plane. Go serves the API, the compiled React dashboard and the status pages from one binary.

This is the hosted product in the Monitor monorepo. Monitor Host is the independent self-hosted server monitor; both use `internal/checks`. Commands below run from the monorepo root unless stated otherwise.

## Status

| Milestone | Scope | State |
|---|---|---|
| 1. Skeleton | Accounts, sign-in, teams, API keys, plans, control plane, billing plumbing, CI | done |
| 2. Monitors and checks | Monitor CRUD, check engine, scheduler, built-in probe, results and daily rollups | done |
| 3. Incidents and alerts | Confirmation, incidents, maintenance windows, email/Telegram/Slack/webhook channels | done |
| 4. Status pages | Public pages, groups, posts, badges, custom domains | done |
| 5. Multi-region probes | `monitor probe`, region-bound probe tokens, majority confirmation | done |
| 6. Plans, API and operations | Limits everywhere, OpenAPI, control commands, server-managed database backups | done |

Plan retention applies to daily uptime history. Raw check results are kept for 31 days on every plan.

The plan never requires taking payments. Billing goes through Paystack and is switched off until `PAYSTACK_SECRET_KEY` is set. Until then every account stays on its plan, and operators assign plans with `control billing comp`.

Currency fields in the database, API and payment requests use ISO codes such as `KES` and `USD`. The interface displays local symbols: `Ksh 500` and `$5`.

KES is the base billing currency. Monthly prices come from `STARTER_PRICE_KES_MINOR`, `PRO_PRICE_KES_MINOR` and `BUSINESS_PRICE_KES_MINOR`, in Kenyan cents. USD prices are the Ksh amount divided by 100, rounded to the nearest US cent: the defaults are Ksh 500 / $5, Ksh 2,000 / $20 and Ksh 5,000 / $50. This is a fixed product pricing rule, independent of market exchange rates.

Customers choose Ksh or USD before their first paid checkout. Ksh supports M-Pesa and cards; USD uses cards. Renewals, upgrades, downgrade schedules and resumed subscriptions retain the original subscription currency. An active subscription cannot switch currencies during a plan change.

For USD card subscriptions, enable USD collection on the Paystack merchant account and create three monthly USD plans at the derived amounts. Set `PAYSTACK_STARTER_USD_PLAN_CODE`, `PAYSTACK_PRO_USD_PLAN_CODE` and `PAYSTACK_BUSINESS_USD_PLAN_CODE` to their codes; the existing plan-code variables remain the KES plans. Missing USD plan codes prevent USD checkout. `PAYSTACK_CURRENCY` remains `KES` as the base. `/api/v1/plans` defaults to KES; `?currency=USD` returns the USD prices. Checkout and renewal responses include their actual charge currency, and control-plane MRR is expressed in KES using the same fixed rule.

## Repository layout

```text
cmd/cloud*/       Cloud server and operating tools
internal/cloud/   Cloud business logic, templates and OpenAPI contracts
internal/checks/  Shared check engines
cloud/dashboard/  React/Vite dashboard
cloud/db/         Migrations and role provisioning
cloud/scripts/    Backup, restore and image build helpers
```

One root `go.mod` builds both products. Cloud configuration lives in `cloud/.env`.

## Local setup

1. Copy `cloud/.env.example` to `cloud/.env`. Fill `OTP_HMAC_KEY` with 32 or more random bytes and `APP_ENCRYPTION_KEY` with exactly 32, both base64 without padding:

   ```text
   openssl rand -base64 32 | tr -d '='
   ```

2. Make sure the shared `norialabs` Docker network is running, with its `noria_postgres` (PostgreSQL 18) and `noria_mailpit` containers. Create the database and its two roles once:

   ```text
   docker exec noria_postgres psql -U admin_444888 -d postgres -c "CREATE ROLE monitor LOGIN PASSWORD 'monitor_local';"
   docker exec noria_postgres psql -U admin_444888 -d postgres -c "CREATE DATABASE monitor_core OWNER monitor;"
   psql "postgres://monitor:monitor_local@localhost:5452/monitor_core" -v app_password=monitor_app_local -f cloud/db/provision/app_role.sql
   ```

3. Apply the migrations:

   ```text
   set -a; source cloud/.env; set +a; go run ./cmd/cloud migrate up
   ```

   `cmd/cloud` embeds the SQL migrations and dbmate's Postgres driver. No separate migration tool or migration directory is needed at runtime. The migration command needs only `DATABASE_URL`.

4. Run the API on `:8910` and the dashboard on `:3910`:

   ```text
   set -a; source cloud/.env; set +a; go run ./cmd/cloud
   cd cloud/dashboard && pnpm install && pnpm dev
   ```

5. Load demo data. `cmd/cloud-seed` wipes the local database and fills it with four workspaces, seven people, 27 monitors with 30 days of history, incidents, alert channels, maintenance windows, status pages, API keys and a remote probe token for every region in `PROBE_REGIONS` other than `local`. It refuses to run unless `APP_ENV=development` and the database is on localhost. It prints the sign-in addresses, the API keys and the command for each remote probe.

   ```text
   set -a; source cloud/.env; set +a; go run ./cmd/cloud-seed
   go run ./cmd/cloud-seed serve
   ```

   `seed serve` answers the seeded monitors on `*.localhost:8920` and `127.0.0.1:8921`, fails the ones the story says are down, and pings the healthy heartbeats, so live checks agree with the history. It needs `PROBE_ALLOW_PRIVATE=true`, which the server refuses in production.

The dashboard dev server proxies `/api` and `/auth` to the API, so `APP_ORIGIN` is the dashboard's origin (`http://localhost:3910`) in development. The ports are 8910 and 3910 so the stack can run next to Kawi's 8900 and 3900.

Sign-in codes go to SMTP. The shared `noria_mailpit` does not publish its SMTP port to the host, so a server run with `go run` needs its own catcher:

```text
docker run -d --name monitor-cloud-mail -p 127.0.0.1:1026:1025 -p 127.0.0.1:8036:8025 axllent/mailpit --smtp-disable-rdns
```

Set `SMTP_PORT=1026` and read codes at `http://localhost:8036`. Without `--smtp-disable-rdns` Mailpit stalls before its greeting and every login times out.

## Two surfaces, one set of handlers

`/api/dashboard/v1/*` authenticates with the `__Host-session` cookie plus an `X-CSRF-Token` header, and answers only on the app origin. `/api/v1/*` authenticates with a bearer API key and answers on the API origin. Every dashboard operation has a bearer twin, and `internal/cloud/app/api_mirror_test.go` fails when a dashboard route lacks one.

An API key carries the role of the person who created it, so `403 forbidden_role` applies on both surfaces. `POST /api/v1/auth/otp/request` and `/verify` give the API origin its own sign-in, which returns a key instead of a cookie.

## Test and build

```text
go vet ./internal/cloud/... ./internal/checks/... ./cmd/cloud*
go test -race ./internal/cloud/... ./internal/checks/... ./cmd/cloud*
cd cloud/dashboard && pnpm check
```

Integration tests need a throwaway PostgreSQL 18 database. They read it only from `TEST_DATABASE_URL` (owner role) and `TEST_APP_DATABASE_URL` (the `NOBYPASSRLS` role), and skip when those are unset. They never touch `DATABASE_URL`, so a development database cannot be wiped by test cleanup. CI provisions Postgres as a service container and runs both suites before any image is built.

## Tenant isolation

Account tables carry Postgres row-level security keyed on `monitor_current_account()`. Customer requests use `APP_DATABASE_URL`, an unprivileged role without `BYPASSRLS`, inside a transaction that sets `monitor.account_id`. Workers and the control plane use the owner connection. `rls_integration_test.go` and `tenant_isolation_integration_test.go` prove both layers. Every new account table gets a policy and a case in those tests.

## Cross-site requests

Cookie-authenticated calls carry the double-submit `__Host-csrf` token. In front of every route, Go's `http.CrossOriginProtection` also refuses state-changing requests that the browser marks as cross-site (`403 cross_origin`). Requests without Fetch metadata, such as the CLI, probes, webhooks and bearer clients, are unaffected, and `CORS_ALLOWED_ORIGINS` entries are trusted.

## Control plane

Operators authenticate with a `control_api` credential (`MONITOR_CONTROL_TOKEN` for the CLI), or with a fresh dashboard session that holds platform grants. Every write is authorized by capability and recorded in the append-only `audit_events`. `db/provision/platform_owner.sql` bootstraps the first operator. The CLI image runs under the compose `control` profile:

```text
docker compose --project-directory cloud --profile control run --rm control accounts list
docker compose --project-directory cloud --profile control run --rm control billing comp <account-id> pro never "early access"
```

The control panel has a **Plans** page at `/control/plans`. Operators with `platform.billing.read` can inspect all plan prices and limits; `platform.plans.manage` permits edits. Definitions are persisted in `plan_catalog`, protected against conflicting edits, and recorded in the audit trail. Environment prices and built-in limits are the defaults until a plan is first saved. The saving app updates immediately; other app instances refresh within 30 seconds. Quotas and retention use the saved limits, including for existing accounts.

Catalog prices apply to new purchases. Pending checkouts and existing subscriptions retain their stored amount, currency and provider plan code. For live price changes, create new monthly Paystack plans first and supply their codes; the server verifies their currency, amount and monthly interval before saving. Existing subscriptions are not repriced. **Plans → Account plans → Manage plan** opens each account's **Plan & subscription** section, where `platform.billing.comp` allows granting, changing or ending a complimentary plan with an audit reason and optional expiry. Paid subscriptions remain owner-managed.

Existing platform owners can grant themselves `platform.plans.manage` through Access grants; new owners provisioned by `db/provision/platform_owner.sql` or the seed receive it automatically.

## Deployment

The repository's `.github/workflows/cloud.yml` tests and builds one `monitor-cloud` image. Its `/monitor` executable provides `serve`, `migrate`, `control`, `probe` and `healthcheck`. The Host workflow independently builds `monitor`.

Compose uses this same Cloud image for the app, the one-shot migration job and the optional operator CLI. `docker compose run --rm migrate status` inspects migration state; `docker compose --profile control run --rm control accounts list` runs the authenticated operator command. Migration startup is serialized with a PostgreSQL advisory lock. Upgrades refuse to proceed if an applied migration version is absent from the release. Keep released SQL migration files unchanged.

Relevant pushes to `main` publish the image to GHCR. Deployment is managed separately; CI does not connect to servers or roll out releases. [compose.prod.yml](compose.prod.yml) provides the production app, migration and optional operator services. [Caddyfile](Caddyfile) provides a reverse-proxy configuration for the app and status-page custom domains.

## Remote probes

The app server checks from `LOCAL_PROBE_REGION`. Every other region in `PROBE_REGIONS` needs a remote probe, which is the app image run as `monitor probe` on a host in that region. It holds no database credentials: it claims due checks from `POST /api/probe/v1/claim` and reports results to `POST /api/probe/v1/results` on the API origin, with a token bound to one region. A monitor goes down when at least half of its regions have failed twice in a row, so a region whose probe is offline cannot open an incident on its own.

```text
docker compose --project-directory cloud --profile control run --rm control probes create eu-west eu-west-1 "first EU probe"
docker run -d --restart unless-stopped -e PROBE_ENDPOINT=https://api.example.com -e PROBE_TOKEN=nmon_prb_... ghcr.io/thekiharani/monitor-cloud:latest probe
docker compose --project-directory cloud --profile control run --rm control probes list
docker compose --project-directory cloud --profile control run --rm control probes revoke <probe-id> "host retired"
```

The token is shown once. Operators can do the same from the admin console's Probes page, which prints the `docker run` command with the right API origin. The regions API reports which regions have a probe seen in the last two minutes, and the monitor form marks the rest as offline. When a probe has not reported for five minutes, or never connected after being created, every operator with `platform.probes.manage` is emailed once, and again when it reports; the email says whether the region still has another probe online.

## Backups

Database backups run at the server level and cover all databases, including Monitor Cloud's database. Cloud does not schedule backups or ship backup scripts, credentials, storage settings or a backup container.

Use the server backup system for retention and restore testing. Include database roles and grants alongside database contents, and verify restoration into an isolated database before relying on recoverability. The monitoring service can still receive heartbeats from external backup jobs.

## Monitor secrets

Request headers and bodies are sealed with `APP_ENCRYPTION_KEY` (AES-GCM) in `monitors.request_encrypted`, like alert channel settings. Header values that look like credentials (authorization, cookies, and anything naming a token, secret, key, password, signature or session) are returned as `********`; sending that value back unchanged keeps the stored one.
