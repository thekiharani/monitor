# Monitor

One Go module for two independently built and deployed products.

| Product | Purpose | Entrypoint | Source | Operating guide |
|---|---|---|---|---|
| Monitor Host | Self-hosted host, container, database and service monitoring | `cmd/host` | `internal/host` | [Host guide](HOST.md) |
| Monitor Cloud | Managed uptime monitoring, accounts, billing and public status pages | `cmd/cloud` | `internal/cloud` | [Cloud guide](cloud/README.md) |

## Build and run

Use Go 1.27.2 or later, from this directory:

```sh
make host
make cloud
make test
make vet
```

Builds strip debug symbols with `-s -w`, matching the container builds.

Outputs are `bin/monitor-host` and `bin/monitor-cloud`. Cloud includes `serve`, `migrate`, `control`, `probe` and `healthcheck` subcommands. Migrations are embedded in the Cloud binary; `bin/monitor-cloud migrate up` needs only `DATABASE_URL`.

Host embeds its own dashboard and email templates. Run `bin/monitor-host` locally; native collectors are selected automatically.

Cloud needs its own PostgreSQL database and configuration. Copy `cloud/.env.example` to `cloud/.env` and follow the Cloud guide for provisioning and migrations. For a complete local dashboard build:

```sh
make dashboard
set -a
source cloud/.env
set +a
DASHBOARD_DIR="$PWD/cloud/dashboard/dist" bin/monitor-cloud
```

The optional development tools are `go run ./cmd/cloud-seed`, `go run ./cmd/cloud-email-preview`, and the Cloud binary's `probe` subcommand.

## Shared code and boundaries

`internal/checks` owns reusable network checks, network error descriptions, target validation and public-address filtering. Host uses `checks.RunHost` with its existing local-network access and status rules. Cloud uses `checks.Run` with configurable HTTP/TCP checks, timing, keywords and public-only enforcement. The APIs retain their existing response formats.

Host collectors, SQLite history, alert policies, templates and dashboard stay in `internal/host`. Cloud tenancy, PostgreSQL access, billing, scheduling, alerts, templates and API contracts stay in `internal/cloud`; its frontend, database migrations and operating assets stay in `cloud`.

Neither service imports the other's application packages. Share reusable mechanisms in `internal` when both need them; keep business rules, storage and configuration with their owning service. A single `go.mod` manages dependencies, while Go links only the packages imported by each binary.

## Independent container images

Build from the repository root:

```sh
docker build -t monitor-host:local .
docker build -f cloud/Dockerfile -t monitor-cloud:local .
```

The single Host image includes `smartctl` and runs as UID/GID `65532:65532` by default. Set `MONITOR_SMART_ENABLED=true` and `MONITOR_SMART_INTERVAL=15m` to run disk-health collection in the monitor process; Linux disk access also requires the hardware permissions in `deploy/compose.smart.yml`. From `deploy/`, set `COMPOSE_FILE=compose.yml:compose.smart.yml` and run `docker compose up -d --remove-orphans` to replace the old SMART companion container. CI now publishes only `monitor` for Host, for both amd64 and arm64. Existing optional collectors keep their environment-based configuration.

The Cloud image includes its compiled React dashboard, embedded SQL migrations and operator commands. Production builds exactly two images: `monitor` and `monitor-cloud`. Cloud migrations run as a separate one-shot container from the app image and must succeed before app startup. Database backups are managed at the server level; Cloud ships no backup container or backup tooling. Each product has independent configuration, data storage, images and deployment. Existing published image names and in-container executable paths remain compatible: Host uses `ghcr.io/thekiharani/monitor` and Cloud uses `ghcr.io/thekiharani/monitor-cloud`.

Host Compose files remain at `compose.local.yml` and `deploy/`. Cloud Compose files live in `cloud/`; for example, `docker compose --project-directory cloud -f cloud/compose.prod.yml config`.

Host release automation is `.github/workflows/release.yml`; Cloud automation is `.github/workflows/cloud.yml`. Both workflows run for pushes and pull requests targeting `main`. Each compares changes against its own last successful push run, so failed image releases are retried on the next push. Host code builds only Host images; Cloud code and dashboard changes build only Cloud images; shared `internal` packages, module dependencies, the Docker ignore file and shared CI scripts build both. Documentation-only changes skip image builds, and Go test-only changes run the relevant tests. Pull requests validate affected images without publishing; pushes publish them to GHCR. Changes to common code or module dependencies trigger both. Cloud uses its own `cloud-build/main/*` build tags, separate from Host's `build/main/*` tags. Existing Cloud production deployment secrets must also be configured in the Monitor repository before Cloud CI can deploy.

## Migration

The former `monitor-cloud` working tree was copied into this repository, including pending frontend edits. The original checkout was deleted after both products built and ran locally from this monorepo, and the full race suite passed against a fresh PostgreSQL database. The retirement archive and temporary migration copies were subsequently deleted at the user’s request; ongoing development belongs here. Existing Host edits were preserved while moving their source files. Neither remote repository nor running deployment was changed.

Cloud secrets remain untracked in `cloud/.env`. Production machines keep their existing deployment directories and image names.

## Local verification

On 19 October 2026, both optimized native macOS arm64 binaries ran concurrently from this repository. Host served its dashboard and `/api/host`; Cloud served its login dashboard and `/health/ready` against a freshly migrated, isolated PostgreSQL instance. Both binary health-check commands passed, as did `go test -race -count=1 ./...` with both database test URLs configured. Verification processes and the temporary database were stopped afterward.

| Binary | macOS arm64 | Linux amd64 (CGO disabled) |
|---|---:|---:|
| `monitor-host` | 26.22 MiB | 26.88 MiB |
| `monitor-cloud` | 24.85 MiB | 26.13 MiB |

Native outputs are in `bin/`; cross-compiled outputs are in `bin/linux-amd64/`. Cloud's compiled dashboard is separate from its executable and is included in its Docker image.

## Docker verification

On 19 October 2026, local Linux arm64 runtime images passed dashboard, API, compiled frontend asset, database readiness and Docker health checks using a fresh PostgreSQL container. The Cloud image applied migrations and passed status, operator bootstrap and authenticated stats checks using its own executable. Reapplying migrations is idempotent, absent applied versions block upgrades, and an advisory-lock integration test verifies serialization. A failing migration prevented Compose from starting the app. Test containers and their isolated network were removed afterward; the images remain available locally.

| Image | Docker-reported size |
|---|---:|
| `monitor-host:local` (arm64) | 34.93 MiB |
| `monitor-host:local-amd64` (amd64) | 37.14 MiB |
| `monitor-cloud:local` (arm64) | 27.63 MiB |
| `monitor-cloud:local-amd64` (amd64) | 29.70 MiB |

Cloud's arm64 and amd64 images passed migrations, authenticated control, dashboard/assets, readiness and migration-failure gating checks. The amd64 checks ran through local emulation with a writable `/tmp`. The tested images used their Docker-compiled Go backend and the production dashboard already built from `cloud/dashboard`. The all-in-Docker frontend dependency install repeatedly timed out at the npm registry, so that build path was not fully verified. The temporary packaging Dockerfile is `/tmp/monitor-cloud-local-assets.Dockerfile`; it uses the `dashboard-assets` named build context pointed at `cloud/dashboard/dist` and native Go cross compilation for amd64. Verification results are recorded in `/tmp/monitor-docker-verification/result.json`.

Host and Cloud share the Docker Go module cache. Cloud's compile step also mounts that module cache so it can reuse dependencies downloaded in the preceding step.

The Host image was subsequently consolidated to include `smartctl` and only its required runtime libraries. The final arm64 and amd64 images passed both SMART-disabled and SMART-enabled container checks: dashboard/API readiness, report generation only when enabled, rejection of invalid intervals and clean shutdown. The default image user remains `65532:65532`. Unit tests use a simulated disk to verify health-report contents; no physical host disks were exposed during Docker smoke tests. The separate SMART image, container and shared report volume have been removed from CI and deployment configuration.

The amd64 container checks ran through local Rosetta emulation with a writable `/tmp`; the CI amd64 runner is native. Both architecture builds also verified `smartctl --version` as the default non-root user.
