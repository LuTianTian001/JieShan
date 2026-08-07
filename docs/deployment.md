# Deployment

This guide targets one administrator running the single JieShan container
behind Caddy on a two-core Linux server.

- **2 cores and 1 GiB RAM** is the minimum practical target for light personal
  traffic when the host pulls a prebuilt image.
- **2 cores and 2 GiB RAM** is the recommended baseline when the same host also
  runs a VPN or proxy for other people, or when request and probe concurrency is
  sustained.

JieShan does not require PostgreSQL, Redis, a separate worker container, or a
separate frontend service.

## Capacity plan

The supplied Compose limits target a two-gigabyte host and leave capacity for
the kernel, Caddy, and a separately limited Mihomo or Clash service:

| Component | Supplied limit or expected range |
| --- | --- |
| JieShan container memory | 768 MiB |
| Container memory plus swap | 896 MiB combined limit |
| Go heap | 560 MiB through `GOMEMLIMIT` |
| JieShan CPU | 1.5 cores |
| Caddy | Usually tens of MiB at light traffic |
| Mihomo, sing-box, or Xray | Target 256-384 MiB; bound caches and logs |
| Host swap | 1-2 GiB for short peaks, not sustained normal use |

Two cores and one gigabyte are reasonable when:

- there is one administrator;
- request concurrency is low;
- only routed models are selected for monitoring;
- probe concurrency stays at the supplied conservative defaults;
- the image is built elsewhere and pulled by the server;
- request, container, and proxy logs are bounded;
- a co-located proxy is mainly for personal use.

Use two gigabytes or a separate proxy host for sustained concurrency, many
selected model-target pairs, long history retention, or a VPN shared with
several people. Repeated OOM kills or sustained swap use are capacity signals,
not reasons to remove memory limits.

## Prepare the host

Install Docker Engine, the Docker Compose plugin, and Caddy from their official
repositories. Do not build the image on a one-gigabyte server, especially when
the host has slow or restricted external network access.

If the host has no swap, create two gigabytes as an emergency buffer:

```bash
sudo fallocate -l 2G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
```

Confirm resources and Docker before deployment:

```bash
nproc
free -h
df -h /
docker version
docker compose version
```

## Configure JieShan

Create a private deployment directory containing `compose.yaml` and
`.env.example`, then create `.env`:

```bash
umask 077
cp .env.example .env
chmod 600 .env
```

The image defaults to `ghcr.io/lutiantian001/jieshan:latest`. For controlled
production upgrades, replace `latest` with a released version or immutable
commit-SHA tag.

`JIESHAN_CONTAINER_NAME` and `JIESHAN_DATA_VOLUME` default to `jieshan` and
`jieshan-data`. Leave them unchanged for a normal single instance. Give both a
unique value, together with a different `JIESHAN_PORT`, when an isolated VNext
instance must run beside an older container during legacy acceptance testing:

```dotenv
JIESHAN_CONTAINER_NAME=jieshan-vnext
JIESHAN_DATA_VOLUME=jieshan-vnext-data
JIESHAN_PORT=4001
```

The two instances must never mount the same writable data volume.

You can initialize a new data volume with explicit credentials:

```dotenv
JIESHAN_ADMIN_PASSWORD=a-long-unique-password
JIESHAN_SECRET_KEY=64-hexadecimal-characters
```

The administrator password must contain at least 12 characters. The secret key
must be exactly 32 random bytes encoded as 64 hexadecimal characters.

Both values can be omitted on the first start. JieShan then writes the generated
administrator password to `/data/initial-admin-password.txt` and creates
`/data/jieshan-secret.key`. Read and remove the bootstrap password after the
first successful login:

```bash
docker compose exec jieshan cat /data/initial-admin-password.txt
docker compose exec jieshan rm /data/initial-admin-password.txt
```

The generated secret key file must remain stable across restarts, upgrades, and
restores. Replacing it makes stored upstream inference and management
credentials unreadable.

Keep `JIESHAN_BIND_IP=127.0.0.1` when Caddy runs on the same host. Keep
`JIESHAN_TRUST_PROXY=true` only for that trusted reverse-proxy path. Set it to
`false` if clients connect directly to JieShan.

Public relay sites need no private-network access. Set
`JIESHAN_ALLOW_PRIVATE_UPSTREAMS=true` only when an intended upstream is on a
private address you control.

## Persisted runtime settings

Routing health, probe interval, watchdog timeouts, maximum attempts, and log
retention are stored as one revisioned global record in SQLite. The defaults
come from `internal/vnext/store/runtime_settings.go`:

| Setting | Default |
| --- | ---: |
| Failure threshold | 2 |
| Failure window | 5 minutes |
| Cooldown | 5 minutes |
| Probe interval | 5 minutes |
| First output timeout | 15 seconds |
| Stream idle timeout | 60 seconds |
| Total request timeout | 5 minutes |
| Maximum attempts | 4 |
| Operational log retention | 30 days |

The corresponding environment values in `compose.yaml` are bootstrap inputs
for an untouched database. After an administrator saves `/api/vnext/settings`,
the database remains authoritative across restarts. Changing an environment
value later does not silently overwrite saved panel settings.

Transport limits such as dial timeout, response-header timeout, connection
pool size, probe execution timeout, and probe concurrency remain deployment
configuration because they protect the whole process rather than define model
routing policy.

The supplied 2C2G profile uses at most 12 live connections to one upstream and
a SQLite pool of four open and two idle connections. SQLite stays in WAL mode,
auto-checkpoints every 1,000 pages, caps retained journal space at 64 MiB, and
runs planner optimization plus a passive checkpoint after the daily retention
pass. Passive checkpointing does not wait for active readers, so maintenance
cannot stall an in-flight streamed response.

## Start and verify

The production host should pull an image built by CI:

```bash
docker compose pull
docker compose up -d --no-build
docker compose ps
```

Compose gives JieShan a 40-second stop grace period. The process cancels
background work while draining HTTP requests for up to 15 seconds, then waits
up to another 15 seconds for bounded service teardown before closing SQLite.
Keep this grace period when customizing the deployment; a shorter container
timeout can turn a normal upgrade into a forced kill.

Verify process health, logs, and resource use:

```bash
curl --fail http://127.0.0.1:4000/healthz
docker compose logs --tail=100 jieshan
CONTAINER_ID="$(docker compose ps -q jieshan)"
docker stats --no-stream "$CONTAINER_ID"
docker inspect "$CONTAINER_ID" --format \
  'memory={{.HostConfig.Memory}} memory_swap={{.HostConfig.MemorySwap}} nano_cpus={{.HostConfig.NanoCpus}} pids={{.HostConfig.PidsLimit}}'
docker inspect "$CONTAINER_ID" --format '{{json .State.Health}}'
```

For the supplied profile, Docker reports approximately 805306368 bytes of
memory, 939524096 bytes of combined memory plus swap, 1500000000 nano-CPUs,
and a PID limit of 128. Treat a missing or zero limit as a deployment error
before adding traffic.

Then verify the product path, not only the process:

1. Sign in and confirm the settings page shows the persisted global defaults.
2. Add one Site, one Endpoint, and one inference API key.
3. Discover or import provider models and publish one model.
4. Put at least two compatible targets into strict route order when available.
5. Select that model for monitoring and run its all-target manual probe.
6. Create a downstream key with a small USD quota and RPM limit.
7. Call the appropriate native surface: OpenAI Chat or Responses, Anthropic
   Messages, or Gemini GenerateContent.
8. Confirm the request log contains the route profile, site, endpoint, API key
   snapshot, timings, attempts, token usage, and price snapshot.
9. If a Site account adapter is configured, confirm exact balance and raw
   upstream usage synchronization. Do not expect package or subscription data.

## HTTPS with Caddy

Copy `deploy/caddy/Caddyfile.example` to the host Caddy configuration, replace
the example domain, point DNS to the server, validate, and reload:

```bash
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

Only ports 22, 80, and 443 need to be public for the panel. Port 4000 remains on
loopback. The example disables response buffering so streaming output is not
held by the reverse proxy.

Before DNS and TLS are ready, keep the service private and use an SSH tunnel:

```bash
ssh -L 4000:127.0.0.1:4000 user@your-server
```

Do not expose the panel, downstream API keys, upstream API keys, cookies, or
management tokens over public plain HTTP.

## Co-locating a VPN or proxy

Run sing-box, Xray, or another proxy as a separate service with its own restart
policy, memory limit, ports, logs, and data directory. Do not place it inside
the JieShan container or mount the JieShan data volume into it.

Caddy normally owns TCP ports 80 and 443. Give the proxy a different explicit
TCP or UDP port and open only the required firewall rules. If the proxy must own
port 443, use another public IP or a deliberately configured protocol
multiplexer; the example Caddy layout cannot share one IP-and-port pair.

On the shared 2C2G host, watch RSS, swap, file descriptors, and connection
tracking after real traffic begins. Keep Mihomo in its own container or system
service with an explicit memory limit and separate log rotation.

## Back up SQLite

Back up before every image upgrade, schema change, or legacy-data conversion.
A short maintenance window is the simplest consistent volume backup:

```bash
mkdir -p backups
CONTAINER_ID="$(docker compose ps -q jieshan)"
DATA_VOLUME="$(docker inspect "$CONTAINER_ID" --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}')"
test -n "$DATA_VOLUME"
docker compose stop jieshan
docker run --rm \
  -v "$DATA_VOLUME:/data:ro" \
  -v "$PWD/backups:/backup" \
  alpine:3.22 \
  sh -c 'tar czf /backup/jieshan-$(date +%Y%m%d-%H%M%S).tar.gz -C /data .'
docker compose start jieshan
```

Copy the archive off the server and verify it contains `jieshan.sqlite`,
`jieshan-secret.key`, and any `-wal` or `-shm` files present in the stopped
volume. Store `.env` separately in an encrypted password manager. A database
backup without its deployment secret cannot restore encrypted credentials.

For a non-container installation, stop the process and copy the database and
adjacent WAL files together, or use SQLite's online backup command. Never copy
only the main database file while the application is actively writing.

## Upgrade and rollback

Pin the current image tag and create a verified backup, then upgrade:

```bash
docker compose pull
docker compose up -d --no-build --remove-orphans
docker compose ps
curl --fail http://127.0.0.1:4000/healthz
docker compose logs --tail=100 jieshan
```

After startup, sign in, inspect the persisted settings and monitor matrix, and
make one small inference call. Keep the previous image tag and pre-upgrade
backup until this verification passes.

To roll back after a schema migration, stop JieShan, select the previous image,
restore the matching pre-upgrade data volume and the same deployment secret,
then start the service. Do not run an older binary against a database migrated
by a newer release unless backward compatibility is explicitly documented.

Read [Migration](migration.md) before using data from an older prototype. The
production runtime does not perform an automatic or in-place legacy import;
the supported path is an explicit offline conversion into a new database.

## Shared 2C2G operations

- Monitor only models that can actually receive traffic.
- Keep model and target probe concurrency at the supplied defaults.
- Keep Docker log rotation enabled as supplied by `compose.yaml`.
- Leave operational history retention bounded; the default is 30 days.
- Build in GitHub Actions or another machine. Do not run `pnpm install` or a
  production build on the shared production server.
- Treat sustained swap, OOM kills, or rising request queue time as a reason to
  increase memory or separate the proxy workload.
