# Monitor

A live dashboard for a Linux server: host resources, Docker containers, systemd services, databases, caches, web servers, queues, endpoint checks, and drive health in one page. A Go binary serves the embedded interface and streams updates to your browser.

## Features

- CPU usage per core, load, temperature, memory, swap, and pressure stall information.
- Disk throughput, latency, queue depth, filesystem capacity, inodes, and filesystem errors.
- Network traffic, busiest processes, and Docker container health and resource usage.
- A page for every service and check, with 24 hours of history and live detail: running queries and lock waits, process lists, current operations, slow logs and clients, queues, check events, and every systemd unit.
- A page for every container: live CPU, memory, disk, and network charts, logs, processes, health-check history, limits, security settings, networks, ports, mounts, environment variable names, labels, and Compose siblings.
- Optional statistics for PostgreSQL, MySQL, MariaDB, MongoDB, Redis, Memcached, RabbitMQ, and the web servers and proxies Nginx, Caddy, Traefik, HAProxy, and Apache, any number of instances each.
- Optional HTTP, HTTPS, and TCP checks for anything else, with certificate expiry.
- Optional systemd unit health, covering services installed directly on the host.
- Optional SMART drive health.
- A health summary explaining resource pressure, failed services, drive problems, and pending upkeep; select a finding to jump to its card.
- A 24-hour health strip that stays in view: hover it to see what was wrong at any minute, or select a moment to chart it.
- Charts from 5 minutes to 24 hours that share one time cursor: hover or use the arrow keys to read every chart at the same moment, and click to pin it.
- Search everything with `/` or ⌘K: containers, their logs, services, checks, systemd units, findings, disks, and sections.
- A section index that stays in view and marks sections with problems; filters for containers by state, Compose project, or text, and for services with problems.
- The browser tab's title and icon show the current health, and the page marks itself stale when updates stop.
- Light and dark themes that follow the system or a saved choice.
- Host metrics update every second; container and service metrics every 5 to 15 seconds.

Second-level history stays in memory for 30 minutes. Minute averages are saved locally for up to 24 hours in the `data` volume; container, service, and SMART details are live only. The dashboard shows health findings; it does not send alert notifications.

## Quick start

Use a Linux host with Docker Engine and Docker Compose v2. Published images support `linux/amd64` and `linux/arm64`. Container resource metrics use cgroups v2. Host paths and the Docker socket are configurable.

```sh
git clone https://github.com/thekiharani/monitor.git
cd monitor/deploy
cp .env.example .env
chmod 600 .env
```

Set a long, unique `MONITOR_PASSWORD` in `.env`. Optional collectors are disabled in the example configuration; enable only what the server runs:

```dotenv
MONITOR_PASSWORD=replace-with-a-long-unique-password
MYSQL_URL=
REDIS_URL=
CHECKS=
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
| `POSTGRES_URL` | PostgreSQL connection URLs; empty disables collection. |
| `MYSQL_URL` | MySQL or MariaDB connection URLs; empty disables collection. |
| `MONGODB_URL` | MongoDB connection URLs; empty disables collection. |
| `REDIS_URL` | Redis connection URLs; empty disables collection. |
| `MEMCACHED_URL` | Memcached addresses; empty disables collection. |
| `NGINX_URL` | Nginx `stub_status` URLs; empty disables collection. |
| `CADDY_URL` | Caddy admin API URLs; empty disables collection. |
| `TRAEFIK_URL` | Traefik API URLs; empty disables collection. |
| `HAPROXY_URL` | HAProxy stats page URLs; empty disables collection. |
| `APACHE_URL` | Apache `mod_status` URLs; empty disables collection. |
| `RABBITMQ_URL` | RabbitMQ management API URLs; empty disables collection. |
| `CHECKS` | HTTP, HTTPS, or TCP endpoints to check; empty disables checks. |
| `SYSTEMD_UNITS` | Units that must stay active, used with `compose.systemd.yml`. |
| `HOST_DBUS_SOCKET_PATH` | Host system D-Bus socket for `compose.systemd.yml`; defaults to `/run/dbus/system_bus_socket`. |
| `COMPOSE_PROFILES` | Set to `smart` to start the drive-health helper; empty disables it. |
| `SMART_INTERVAL` | Drive scan interval; defaults to `15m`. |
| `HOST_PROC_PATH`, `HOST_SYS_PATH`, `HOST_ROOT_PATH`, `HOST_DEV_PATH` | Host mount sources; default `/proc`, `/sys`, `/`, `/dev`. |
| `DOCKER_SOCKET_PATH` | Host socket path; defaults to `/var/run/docker.sock`. |
| `DOCKER_PROXY_USER` | Proxy UID:GID; defaults to `0:0`. Match socket permissions for rootless Docker. |
| `MONITOR_DATA_PATH`, `MONITOR_SMART_PATH` | Optional absolute host directories instead of managed data/SMART volumes. |
| `MONITOR_DOCKER_HOST` | Collector endpoint; defaults to the bundled proxy. Empty disables container metrics. |
| `MONITOR_CONTAINER_LOGS` | Set to `false` to stop the proxy serving container logs; defaults to `true`. |
| `MONITOR_QUERY_TEXT` | Set to `false` to hide running query and command text on service pages; defaults to `true`. |

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
| `CONTAINER_LOGS` | `true` | Whether the proxy serves container logs. |
| `SYSTEMD_SOCKET` | `/run/dbus/system_bus_socket` | System D-Bus socket the proxy reads units from. |
| `SYSTEMD_URL` | Unset | Unit list endpoint; `compose.systemd.yml` sets `http://docker-proxy:2375/systemd/units`. |

`DOCKER_HOST` accepts HTTP or `tcp://` endpoints; direct Unix socket URLs are unsupported by the collector. In Compose, set `MONITOR_DOCKER_HOST` to configure the collector without changing the Docker CLI's own `DOCKER_HOST`. `HOST_*_PATH` variables select mount sources; `HOST_PROC`, `HOST_SYS`, and `HOST_ROOT` select the matching container mount locations.

## Optional collectors

Every service variable takes one or more space-separated URLs, and each becomes its own card. Cards are labelled with the host and port; prefix a URL with `name=` to choose the label:

```dotenv
MYSQL_URL="main=mysql://monitor:PASSWORD@db:3306 replica=mysql://monitor:PASSWORD@db-replica:3306"
```

URL-encode special characters in credentials. A service that stops answering turns its card red and adds a critical finding to the summary.

### Services on the host

Services in other containers are reached by container name once the monitor shares their network (see [Configuration](#configuration)). Services installed directly on the host are reached through `host.docker.internal`, which Compose maps to the host:

```dotenv
MYSQL_URL=mysql://monitor:PASSWORD@host.docker.internal:3306
```

The service must listen on an address containers can reach, not only `127.0.0.1`: for example `bind-address = 0.0.0.0` with a firewall that blocks outside access, or the Docker bridge address (usually `172.17.0.1`). Host firewalls such as UFW may also need to allow traffic from Docker's subnets.

### PostgreSQL

Create a dedicated monitoring login using a database administrator connection:

```sql
CREATE ROLE monitor LOGIN PASSWORD 'replace-with-a-unique-password';
GRANT CONNECT ON DATABASE postgres TO monitor;
GRANT pg_monitor TO monitor;
```

The PostgreSQL [`pg_monitor` role](https://www.postgresql.org/docs/current/predefined-roles.html) provides access to monitoring statistics. Set `POSTGRES_URL` with that login, for example `postgres://monitor:PASSWORD@postgres:5432/postgres?sslmode=disable`. Configure TLS for connections outside a trusted private network.

The card shows availability, connections, active queries, longest query duration, transactions per second, cache hit ratio, and the five largest databases.

### MySQL and MariaDB

Create a monitoring login:

```sql
CREATE USER 'monitor'@'%' IDENTIFIED BY 'replace-with-a-unique-password';
GRANT PROCESS, REPLICATION CLIENT ON *.* TO 'monitor'@'%';
```

`PROCESS` shows other sessions' queries and `REPLICATION CLIENT` shows replica status. Database sizes cover only schemas the login can see; grant `SHOW VIEW` or `SELECT` on a schema to include it. Set `MYSQL_URL`, for example `mysql://monitor:PASSWORD@mysql:3306`; `mariadb://` works too. Add `?tls=true`, `?tls=skip-verify`, or `?tls=preferred` for TLS.

The card names the server MySQL or MariaDB and shows connections, running queries, longest query duration, queries per second, slow queries, InnoDB buffer pool hit ratio, the five largest databases, and replication status on replicas. A stopped replication thread is critical; lag over 5 minutes is a warning.

### MongoDB

Create a monitoring user in the `admin` database:

```js
db.getSiblingDB("admin").createUser({ user: "monitor", pwd: "replace-with-a-unique-password", roles: ["clusterMonitor"] })
```

Set `MONGODB_URL` to a standard connection string, for example `mongodb://monitor:PASSWORD@mongo:27017/?authSource=admin`; `mongodb+srv://` is supported. To read one replica set member rather than the primary, add `directConnection=true`.

The card shows connections, operations per second, resident memory, the five largest databases, and replica set role. On replica sets it also warns about unreachable members and secondaries more than 5 minutes behind.

### Redis

Set `REDIS_URL`, for example `redis://monitor:PASSWORD@redis:6379`. The account needs permission to run [`INFO`](https://redis.io/docs/latest/commands/info/); password-only authentication also works with `redis://:PASSWORD@redis:6379`.

The card shows clients, memory, operations per second, cache hit ratio, evictions, and total keys across databases. The collector uses plain TCP; `rediss://` does not enable TLS, and URL database paths do not select a database. Use a trusted private connection.

### Memcached

Set `MEMCACHED_URL` to an address such as `memcached:11211`. The card shows connections, memory, commands per second, hit ratio, items, and evictions. Memcached has no authentication on its text protocol; keep it on a private network.

### Nginx

Expose `stub_status` on a location only the monitor can reach:

```nginx
location = /stub_status {
    stub_status;
    allow 172.16.0.0/12;
    deny all;
}
```

Set `NGINX_URL` to that page, for example `http://nginx/stub_status`. The card shows requests per second and connections by state. Connections dropped because `worker_connections` ran out raise a warning.

### Caddy

Expose the admin API to the monitor and turn on metrics in the Caddyfile's global options:

```caddyfile
{
	admin 0.0.0.0:2019
	metrics
}
```

Set `CADDY_URL`, for example `http://caddy:2019`. The admin API can change Caddy's configuration, so never publish port 2019: keep it on a private Docker network shared with the monitor, or bind it to an address only the monitor can reach. For Caddy installed on the host, bind the admin API to an address containers can reach, such as the Docker bridge address, and point `CADDY_URL` at `host.docker.internal`; if Caddy answers `403` because of the Host header, add that address to the admin `origins` option.

The card shows reverse-proxy upstreams and their health, requests per second, requests in flight, the share of server errors, average response time, and whether the latest config loaded. Without `metrics`, upstream health still works and the request figures stay empty. An unhealthy upstream is a warning, and all of them unhealthy is critical. The page lists every upstream, each server's listen addresses, hosts, handlers, and upstreams, and response counts by status.

### Traefik

Enable the API and Prometheus metrics in Traefik's static configuration, for example with `--api.insecure=true --metrics.prometheus=true`. Both are served on Traefik's internal `traefik` entry point, port 8080 by default; never publish it. Set `TRAEFIK_URL`, for example `http://traefik:8080`.

The card shows routers, services, backend servers that are up or down, requests per second, server errors, and open connections. A down server or a router with errors, such as one pointing at a missing service, raises a finding. The page lists every service with each server's status and every router with its rule, entry points, TLS, and errors.

### HAProxy

Add a stats frontend:

```haproxy
frontend stats
  bind :8404
  stats enable
  stats uri /stats
  stats auth monitor:replace-with-a-unique-password
```

Set `HAPROXY_URL` to the stats page, for example `http://monitor:PASSWORD@haproxy:8404/stats`; the monitor reads its CSV form. The card shows sessions, requests per second, servers up and down, and server errors. A backend that is down is critical; a server that is down is a warning. The page lists every backend and server with status, health check, sessions, totals, and when it last changed.

### Apache

Enable `mod_status` and allow the monitor to read it:

```apache
ExtendedStatus On
<Location /server-status>
  SetHandler server-status
  Require ip 172.16.0.0/12
</Location>
```

Set `APACHE_URL`, for example `http://apache/server-status`; the monitor reads its `?auto` form. The card shows requests per second, busy and idle workers, traffic, and connections, and warns when no idle workers are left. The page breaks workers down by state and lists the server's own figures.

### RabbitMQ

Enable the management plugin and create a user with the `monitoring` tag:

```sh
rabbitmqctl add_user monitor 'replace-with-a-unique-password'
rabbitmqctl set_user_tags monitor monitoring
```

Set `RABBITMQ_URL` to the management API, for example `http://monitor:PASSWORD@rabbitmq:15672`. The card shows queued, ready, and unacknowledged messages, publish and delivery rates, connections, queues, consumers, and each node. A stopped node, or a memory or disk alarm that blocks publishers, is critical.

### Endpoint checks

`CHECKS` covers anything without its own collector. Use `tcp://` to confirm a port accepts connections and `http://` or `https://` to confirm a URL answers below HTTP 400:

```dotenv
CHECKS="api=https://api.example.com/health elastic=http://elasticsearch:9200 smtp=tcp://mail:25"
```

Checks run every 15 seconds and show response time. Redirects count as up and are not followed. HTTPS checks warn 21 days before the certificate expires and turn critical at 7 days.

### Systemd units

Add the systemd override to `.env` and run `docker compose up -d`:

```dotenv
COMPOSE_FILE=compose.yml:compose.systemd.yml
SYSTEMD_UNITS="nginx mariadb ssh"
```

The override mounts the host's system D-Bus socket read-only into the Docker proxy, which answers the monitor's unit list request. The card counts running services and lists failed units. Every failed unit is critical, as is any unit in `SYSTEMD_UNITS` that is not active; names without a suffix mean `.service`. Combine overrides with colons, for example `compose.yml:compose.network.yml:compose.systemd.yml`.

The proxy reads units because D-Bus rejects connections from users unknown to the host, such as the monitor's UID `65532`. Hosts without systemd and D-Bus cannot use this override.

### Drive health and host upkeep

Set `COMPOSE_PROFILES=smart` and run `docker compose up -d`. The helper scans with `smartctl` every `SMART_INTERVAL` and writes its report to the shared `smart` volume. It has no network access; the dashboard reads that report once a minute.

SMART needs accessible physical drives. Cloud VMs may expose none. The supplied helper grants `SYS_RAWIO` and block-device access; NVMe health logs also need `SYS_ADMIN` and access to the relevant NVMe controller character device. Adjust the helper's capabilities and device cgroup rules for your hardware.

Host upkeep checks reboot flags and unattended-upgrade timestamps every minute. Update counts are checked every 30 minutes using the host's dpkg database and uncompressed APT `*_Packages` lists. Missing data appears as unknown; the monitor does not refresh indexes or install updates.

## Service pages

Select a service card's title, a check, a finding, or a search result to open its page at `#/services/<kind>/<name>`, where kind is `postgres`, `mysql`, `mongo`, `redis`, `memcached`, `nginx`, `caddy`, `traefik`, `haproxy`, `apache`, `rabbitmq`, `check`, or `systemd`. Its status, availability over the chosen range, and related findings stay at the top. History is recorded for every service from when the monitor starts, kept in memory for up to 24 hours, and drawn on the shared time cursor. Detail is fetched only while a page is open, every 5 seconds, so watched servers see no extra queries otherwise.

| Service | Activity tab | Details tab |
| --- | --- | --- |
| PostgreSQL | Client sessions with state, wait event, duration, and query; sessions waiting on another's lock are marked with the blocking PID | Databases with size, cache hits, deadlocks, and temporary files; replicas and replay lag; key settings |
| MySQL, MariaDB | Process list with command, time, state, and query | Schemas with data and index size; replication status; counters such as temporary tables on disk and row lock waits; key variables |
| MongoDB | Operations in progress with namespace, client, duration, and command | Databases with storage, documents, and indexes; replica set members with state and lag; server facts |
| Redis | Slow log and connected clients | Keys per database; memory, persistence, client, stats, and replication facts |
| RabbitMQ | Queues with messages, consumers, and rates; queues holding messages without consumers are marked | Each node's memory, disk, file handles, and processes |
| Memcached | — | Every statistic the server reports |
| Nginx | — | — |
| Caddy | Upstreams with health, active requests, and failures | Sites with listen addresses, hosts, handlers, and upstreams; responses by status; requests by server |
| Traefik | Services with each server's status; routers with rule, entry points, TLS, and errors | Entry points and providers |
| HAProxy | Backends and servers with status, checks, sessions, and server errors | Frontends |
| Apache | — | Workers by state; server figures |
| Checks | Each change between passing and failing, with the error | — |
| systemd | Every loaded unit, failed first, with watched units marked | — |

Running query and command text can contain customer data or secrets. Set `MONITOR_QUERY_TEXT=false` to keep it off the dashboard; the pages then show everything else. The PostgreSQL `pg_monitor` role, MySQL `PROCESS` privilege, and MongoDB `clusterMonitor` role described above are enough for every tab; Redis needs permission for `SLOWLOG` and `CLIENT LIST` to fill the Activity tab.

## Container pages

Select a container in the dashboard, search for it, or select a finding about one to open its page at `#/containers/<name>`; the address is shareable and survives a recreated container with the same name. Its status stays at the top, and four tabs hold the rest, each with its own address such as `#/containers/<name>/logs`:

- **Overview:** live CPU, memory, disk, and network charts on the shared time cursor, key facts and published ports, resource limits with the memory makeup, and health-check results with their output. Container history is kept in memory for up to 24 hours from when the monitor starts, sampled every 5 seconds.
- **Logs:** refreshed every 2 seconds at full window height, with a text filter and views of all lines, lines that mention errors, failures, or timeouts, or standard error only. The tab counts problem lines. Scrolling up pauses following; select **Paused** to jump back to the newest line. The page keeps the latest 5,000 lines.
- **Processes:** running processes with memory use, filterable by command, user, or PID; CPU and start-time columns appear where the Docker engine supports `ps aux`.
- **Configuration:** command and working directory, security settings, networks, mounts, environment variable names, labels, and the other containers in its Compose project, with a filter across all of them. The tab counts risky security settings.

Network figures come from each container's own network namespace; containers using the host network show none. Environment variable values never reach the dashboard: the proxy removes them before answering. Logs can contain secrets; set `MONITOR_CONTAINER_LOGS=false` to turn them off at the proxy.

## Access and security

Keep the default loopback binding and use an SSH tunnel, or publish through an HTTPS reverse proxy with authentication. Preserve streaming responses on `/api/stream`; disable proxy buffering and allow long-lived connections.

The server runs as a non-root user with a read-only root filesystem and read-only host mounts. Treat it as trusted host software: it can read host information allowed by filesystem permissions. A separate root proxy receives the Docker socket on an internal network and permits only GET requests that list containers, inspect one with its environment values removed, list its processes, or read its logs unless `MONITOR_CONTAINER_LOGS=false`. With `compose.systemd.yml` it also receives the system D-Bus socket and answers only a GET request for the unit list. Keep that proxy private.

## Updates and troubleshooting

Run these commands from `deploy/` after changing `.env` or selecting a release:

```sh
docker compose pull
docker compose up -d
docker compose logs --tail=100 monitor docker-proxy
```

Images are `ghcr.io/thekiharani/monitor` and `ghcr.io/thekiharani/monitor-smart`. `latest` and `main-latest` follow main; `1.0.N`, `main-1.0.N`, and `main-<7-character-sha>` identify builds. Pin `IMAGE_TAG` to a published build for repeatable deployments; select an earlier build and repeat the commands to roll back.

`docker compose down` stops the stack while preserving its named volumes. Removing the `data` volume deletes saved history. After a restart, short ranges refill from live samples; persisted minute history remains available in ranges of an hour or longer.

- **No service card:** check the variable is set and the URLs are space-separated. Connection failures appear in the card; check credentials, permissions, and network reachability.
- **Host service unreachable:** use `host.docker.internal` and make the service listen beyond `127.0.0.1`; see [Services on the host](#services-on-the-host).
- **Systemd card unreachable:** check the override is in `COMPOSE_FILE` and the D-Bus socket exists at `HOST_DBUS_SOCKET_PATH`.
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
| `/api/services/<kind>/<name>` | One service's live detail for its Activity and Details tabs. |
| `/api/services/<kind>/<name>/history?range=1h` | One service's metric history, downsampled to at most 240 points. |
| `/api/containers/<name>` | One container's configuration, limits, health checks, and processes. |
| `/api/containers/<name>/history?range=1h` | One container's usage history, downsampled to at most 240 points. |
| `/api/containers/<name>/logs?since=<id>` | Up to 500 recent log lines, or those after the given line `id`. |

History ranges: `5m`, `15m`, `30m`, `1h`, `3h`, `6h`, `12h`, `24h`; default `5m`.

```sh
curl -u monitor http://127.0.0.1:9100/api/host
curl -N -u monitor http://127.0.0.1:9100/api/stream
```

These commands prompt for the configured password.

## Development and contributions

Use Go 1.27.2 or newer. The interface is embedded at build time; no frontend build is required. Image builds run `go run ./cmd/webbuild web` first, which minifies the HTML, CSS, and JavaScript and writes gzip and Brotli copies beside them; the server sends the smallest encoding each browser accepts, with an ETag so unchanged files are revalidated rather than downloaded again. Local builds without that step serve the readable files, compressed with gzip on the fly.

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

Collection lives in `system.go`, `containers.go`, `services.go`, and `health.go`; history in `history.go`; HTTP and streaming in `main.go`; the interface in `web/`.

Contributions go through pull requests to main with owner review and passing CI. External contributors' workflow runs require approval. Report bugs and feature requests in [Issues](https://github.com/thekiharani/monitor/issues).

CI runs for PRs targeting main and pushes to main. It builds images only when runtime Go files, modules, embedded web assets, the Dockerfile, Docker ignore rules, or the build workflow change. Test-only changes run Go checks; deployment-only changes validate Compose; docs-only changes pass the required checks without building. Main compares against its last successful run so unpublished changes are included.

Only relevant main pushes reserve a version and publish images. Each image builds in parallel on native `ubuntu-26.04` (amd64) and `ubuntu-26.04-arm` runners, then their digests are merged into a multi-platform image.
