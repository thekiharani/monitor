# Monitor

A live dashboard for a Linux server: host resources, Docker containers, PostgreSQL, Redis, and drive health in one page. A Go binary serves the embedded interface and streams updates to your browser.

## Features

- CPU usage per core, load, temperature, memory, swap, and pressure stall information.
- Disk throughput, latency, queue depth, filesystem capacity, inodes, and filesystem errors.
- Network traffic, busiest processes, and Docker container health and resource usage.
- Optional PostgreSQL statistics, Redis statistics, and SMART drive health.
- A health summary explaining resource pressure, failed services, drive problems, and pending upkeep.
- Charts from 5 minutes to 24 hours. Host metrics update every second; container and database metrics every 5 seconds.

Second-level history stays in memory for 30 minutes. Minute averages are saved locally for up to 24 hours in the `data` volume; container, database, and SMART details are live only. The dashboard shows health findings; it does not send alert notifications.

## Quick start

Use a Linux host with Docker Engine and Docker Compose v2. Published images support `linux/amd64` and `linux/arm64`. Container resource metrics use cgroups v2. Host paths and the Docker socket are configurable.

```sh
git clone https://github.com/thekiharani/monitor.git
cd monitor/deploy
cp .env.example .env
chmod 600 .env
```

Set a long, unique `MONITOR_PASSWORD` in `.env`. Optional collectors are disabled in the example configuration:

```dotenv
MONITOR_PASSWORD=replace-with-a-long-unique-password
POSTGRES_URL=
REDIS_URL=
COMPOSE_PROFILES=
```

Start the stack; Compose creates its own networks and volumes:

```sh
docker compose up -d
docker compose ps
```

Open [http://127.0.0.1:9100](http://127.0.0.1:9100). HTTP Basic authentication accepts any username and the configured password. For a remote server, run this on your computer and open the same URL:

```sh
ssh -N -L 9100:127.0.0.1:9100 user@server
```

## Configuration

Compose reads settings from `deploy/.env`:

| Variable | Purpose |
| --- | --- |
| `MONITOR_PASSWORD` | Dashboard and API password. Empty disables authentication. |
| `MONITOR_BIND` | Published host address; defaults to `127.0.0.1`. |
| `MONITOR_PORT` | Published host port; defaults to `9100`. |
| `COMPOSE_PROJECT_NAME` | Namespaces containers, networks, and volumes; defaults to `monitor`. |
| `COMPOSE_FILE` | Optional Compose file list; see network and other overrides below. |
| `APP_NETWORK` | Existing network name, used only with `compose.network.yml`. |
| `IMAGE_TAG` | Shared tag for both images; defaults to `latest`. |
| `MONITOR_IMAGE` | Full override for the monitor and Docker proxy image. |
| `MONITOR_SMART_IMAGE` | Full override for the SMART helper image. |
| `POSTGRES_URL` | PostgreSQL connection URL; empty disables collection. |
| `REDIS_URL` | Redis connection URL; empty disables collection. |
| `COMPOSE_PROFILES` | Set to `smart` to start the drive-health helper; empty disables it. |
| `SMART_INTERVAL` | Drive scan interval; defaults to `15m`. |
| `HOST_PROC_PATH`, `HOST_SYS_PATH`, `HOST_ROOT_PATH`, `HOST_DEV_PATH` | Host mount sources; default `/proc`, `/sys`, `/`, `/dev`. |
| `DOCKER_SOCKET_PATH` | Host socket path; defaults to `/var/run/docker.sock`. |
| `DOCKER_PROXY_USER` | Proxy UID:GID; defaults to `0:0`. Match socket permissions for rootless Docker. |
| `MONITOR_DATA_PATH`, `MONITOR_SMART_PATH` | Optional absolute host directories instead of managed data/SMART volumes. |
| `MONITOR_DOCKER_HOST` | Collector endpoint; defaults to the bundled proxy. Empty disables container metrics. |

To join containers on an existing network, add these settings to `.env`:

```dotenv
COMPOSE_FILE=compose.yml:compose.network.yml
APP_NETWORK=your-existing-network
```

Then run `docker compose up -d`. The optional override joins only the monitor to that network; the Docker proxy stays isolated. Database hostnames must resolve from the monitor container. For multiple instances, choose a distinct `COMPOSE_PROJECT_NAME` and `MONITOR_PORT` for each.

Use a standard `compose.override.yml` for other changes, including additional networks, bind mounts, devices, capabilities, or container ports. If `COMPOSE_FILE` is set, include that override explicitly in its file list. Bind-mounted data directories must be writable by the image's user (UID/GID `65532:65532`); keep `HISTORY_FILE` under `/data` and `SMART_FILE` under `/shared` to use the supplied mounts.

The application also accepts these settings for custom deployments:

| Variable | Default | Purpose |
| --- | --- | --- |
| `ADDR` | `:8080` for the server; `:2375` for the proxy | HTTP listening address. |
| `HOST_PROC` | `/host/proc` | Host procfs mount. |
| `HOST_SYS` | `/host/sys` | Host sysfs mount. |
| `HOST_ROOT` | `/host/root` | Host root filesystem mount. |
| `HISTORY_FILE` | `/data/history.json` | Persisted minute averages; parent directory must be writable. |
| `SMART_FILE` | `/shared/smart.json` | SMART report shared by the helper and server. |
| `DOCKER_HOST` | Unset | Docker HTTP endpoint; Compose sets `http://docker-proxy:2375`. |
| `DOCKER_SOCKET` | `/var/run/docker.sock` | Socket used by the proxy command. |

`DOCKER_HOST` accepts HTTP or `tcp://` endpoints; direct Unix socket URLs are unsupported by the collector. In Compose, set `MONITOR_DOCKER_HOST` to configure the collector without changing the Docker CLI's own `DOCKER_HOST`. `HOST_*_PATH` variables select mount sources; `HOST_PROC`, `HOST_SYS`, and `HOST_ROOT` select the matching container mount locations.

## Optional collectors

### PostgreSQL

Create a dedicated monitoring login using a database administrator connection:

```sql
CREATE ROLE monitor LOGIN PASSWORD 'replace-with-a-unique-password';
GRANT CONNECT ON DATABASE postgres TO monitor;
GRANT pg_monitor TO monitor;
```

The PostgreSQL [`pg_monitor` role](https://www.postgresql.org/docs/current/predefined-roles.html) provides access to monitoring statistics. Set `POSTGRES_URL` with that login and a reachable hostname, for example `postgres://monitor:YOUR_PASSWORD@postgres:5432/postgres?sslmode=disable`. URL-encode special characters in credentials and configure TLS for connections outside a trusted private network.

The dashboard shows availability, connections, active queries, longest query duration, transactions per second, cache hit ratio, and the five largest databases.

### Redis

Set `REDIS_URL`, for example `redis://monitor:YOUR_PASSWORD@redis:6379`. The account needs permission to run [`INFO`](https://redis.io/docs/latest/commands/info/); password-only authentication also works with `redis://:YOUR_PASSWORD@redis:6379`.

The dashboard shows clients, memory, operations per second, cache hit ratio, evictions, and total keys across databases. The collector uses plain TCP; `rediss://` does not enable TLS, and URL database paths do not select a database. Use a trusted private connection.

### Drive health and host upkeep

Set `COMPOSE_PROFILES=smart` and run `docker compose up -d`. The helper scans with `smartctl` every `SMART_INTERVAL` and writes its report to the shared `smart` volume. It has no network access; the dashboard reads that report once a minute.

SMART needs accessible physical drives. Cloud VMs may expose none. The supplied helper grants `SYS_RAWIO` and block-device access; NVMe health logs also need `SYS_ADMIN` and access to the relevant NVMe controller character device. Adjust the helper's capabilities and device cgroup rules for your hardware.

Host upkeep checks reboot flags and unattended-upgrade timestamps every minute. Update counts are checked every 30 minutes using the host's dpkg database and uncompressed APT `*_Packages` lists. Missing data appears as unknown; the monitor does not refresh indexes or install updates.

## Access and security

Keep the default loopback binding and use an SSH tunnel, or publish through an HTTPS reverse proxy with authentication. Preserve streaming responses on `/api/stream`; disable proxy buffering and allow long-lived connections.

The server runs as a non-root user with a read-only root filesystem and read-only host mounts. Treat it as trusted host software: it can read host information allowed by filesystem permissions. A separate root proxy receives the Docker socket and only permits container-listing GET requests on an internal network. Keep that proxy private.

## Updates and troubleshooting

Run these commands from `deploy/` after changing `.env` or selecting a release:

```sh
docker compose pull
docker compose up -d
docker compose logs --tail=100 monitor docker-proxy
```

Images are `ghcr.io/thekiharani/monitor` and `ghcr.io/thekiharani/monitor-smart`. `latest` and `main-latest` follow main; `1.0.N`, `main-1.0.N`, and `main-<7-character-sha>` identify builds. Pin `IMAGE_TAG` to a published build for repeatable deployments; select an earlier build and repeat the commands to roll back.

`docker compose down` stops the stack while preserving its named volumes. Removing the `data` volume deletes saved history. After a restart, short ranges refill from live samples; persisted minute history remains available in ranges of an hour or longer.

- **No database card:** check the URL is set. Connection failures appear in the card; check credentials, permissions, and network reachability.
- **Zero container resource values:** check host cgroups v2 visibility and Docker cgroup paths. Rootless Docker or custom layouts may need mount changes.
- **No drive data:** check `docker compose logs smart`, hardware visibility, and helper device permissions.
- **Dashboard stays at “Connecting”:** check server logs and reverse-proxy streaming settings.
- **No temperature or update count:** those values depend on available host sensors and package metadata.

## HTTP API

All endpoints use GET and share dashboard authentication, except `/healthz`:

| Endpoint | Response |
| --- | --- |
| `/healthz` | Public HTTP liveness probe returning `ok`; independent of monitored host health. |
| `/api/host` | Host identity, OS, kernel, CPU, and application version as JSON. |
| `/api/history?range=1h` | Host history as JSON, downsampled to at most 240 points. |
| `/api/stream` | Server-sent events containing live JSON snapshots. |

History ranges: `5m`, `15m`, `30m`, `1h`, `3h`, `6h`, `12h`, `24h`; default `5m`.

```sh
curl -u monitor http://127.0.0.1:9100/api/host
curl -N -u monitor http://127.0.0.1:9100/api/stream
```

These commands prompt for the configured password.

## Development and contributions

Use Go 1.27.2 or newer. The interface is embedded at build time; no frontend build is required.

```sh
go test -race ./...
go vet ./...
go build -trimpath -o monitor .
```

For a local run on Linux:

```sh
ADDR=127.0.0.1:8080 HOST_PROC=/proc HOST_SYS=/sys HOST_ROOT=/ \
  HISTORY_FILE=/tmp/monitor-history.json ./monitor
```

Open [http://127.0.0.1:8080](http://127.0.0.1:8080). Optional collectors stay disabled until configured. Binary commands are `serve` (default), `healthcheck` (container liveness), `docker-proxy`, and `smart` (requires `smartctl` on `PATH`).

Collection lives in `system.go`, `services.go`, and `health.go`; history in `history.go`; HTTP and streaming in `main.go`; the interface in `web/`.

Contributions go through pull requests to main with owner review and passing CI. External contributors' workflow runs require approval. Report bugs and feature requests in [Issues](https://github.com/thekiharani/monitor/issues).

CI runs for PRs targeting main and pushes to main. It builds images only when runtime Go files, modules, embedded web assets, the Dockerfile, Docker ignore rules, or the build workflow change. Test-only changes run Go checks; deployment-only changes validate Compose; docs-only changes pass the required checks without building. Main compares against its last successful run so unpublished changes are included.

Only relevant main pushes reserve a version and publish images. Each image builds in parallel on native `ubuntu-26.04` (amd64) and `ubuntu-26.04-arm` runners, then their digests are merged into a multi-platform image.
