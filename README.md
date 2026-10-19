# Monitor

Server and uptime monitoring in Go, with two independently deployed services.

| Service | What it does | Guide |
| --- | --- | --- |
| **Monitor Host** | Live Linux and macOS metrics, service and endpoint checks, Linux container and drive health, and Telegram/email alerts. | [Host setup and configuration](HOST.md) |
| **Monitor Cloud** | Multi-tenant HTTP, keyword, TCP and heartbeat monitoring, regional probes, incident alerts, public status pages, teams and billing. | [Cloud setup and configuration](cloud/README.md) |

## Get started

For native Host monitoring, install Go 1.27.2 or later and run from the repository root:

```sh
go run ./cmd/host
```

Open [localhost:9100](http://localhost:9100). For Linux Docker deployment and optional collectors, follow the [Host guide](HOST.md#quick-start).

Cloud requires PostgreSQL, email delivery and application keys. Follow the [Cloud setup guide](cloud/README.md#local-setup); its React dashboard uses Node.js 24 and the pnpm version pinned in [package.json](cloud/dashboard/package.json).

## Build and check

Run from the repository root:

```sh
make build       # bin/monitor-host and bin/monitor-cloud
make dashboard   # cloud/dashboard/dist
make test
make vet
cd cloud/dashboard && pnpm check
```

`make dashboard` installs dashboard dependencies. Cloud database integration tests require isolated databases configured through `TEST_DATABASE_URL` and `TEST_APP_DATABASE_URL`; they skip when those variables are unset. See the [Cloud test instructions](cloud/README.md#test-and-build).

## Containers

| Service | Published image | Compose configuration |
| --- | --- | --- |
| Host | `ghcr.io/thekiharani/monitor` | [deploy/compose.yml](deploy/compose.yml) |
| Cloud | `ghcr.io/thekiharani/monitor-cloud` | [cloud/compose.prod.yml](cloud/compose.prod.yml) |

Host includes `smartctl`; drive access requires the [SMART configuration](HOST.md#drive-health-and-host-upkeep). Cloud includes its dashboard, migrations, operator CLI and remote probe command. Its Compose stack runs migrations before starting the app. Database backups are managed outside the application.

To build images locally:

```sh
docker build -t monitor-host:local .
docker build -f cloud/Dockerfile -t monitor-cloud:local .
```

CI checks affected services on pull requests to `main`. Relevant pushes to `main` publish images; the Cloud workflow also deploys to the configured VPS.

## Source layout

Both services share one Go module and reusable network checks in `internal/checks`. Host code and its embedded dashboard live in `internal/host`; Cloud code lives in `internal/cloud`, with its frontend, migrations and deployment files in `cloud`. Their configuration and storage are independent.
