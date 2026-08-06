# Deployment

This guide targets one administrator running JieShan behind Caddy on a
two-core, one-gigabyte Linux server. That size is suitable for light personal
traffic when the server pulls a prebuilt image. If the same server also runs a
VPN or proxy for other people, two gigabytes is the safer baseline.

## Capacity plan

The supplied Compose limits leave part of a one-gigabyte host available to the
kernel, Caddy, and a light personal proxy:

| Component | Suggested limit or working range |
| --- | --- |
| JieShan container | 640 MiB memory, 768 MiB memory plus swap |
| Go heap | 480 MiB through `GOMEMLIMIT` |
| Caddy | Usually tens of MiB at light traffic |
| sing-box or Xray | Configuration-dependent; bound caches and logs |
| Swap | 1 GiB for short peaks, not sustained normal use |

Two cores and one gigabyte are enough when:

- there is one administrator;
- request concurrency is low;
- only routed models are monitored;
- images are built elsewhere;
- request, container, and proxy logs are bounded;
- the co-located proxy is primarily personal use.

Use two gigabytes or a separate proxy server for sustained concurrency, many
monitored model-site pairs, large log retention, or a VPN shared with multiple
people. Do not add PostgreSQL, Redis, a desktop environment, or a large server
panel to the one-gigabyte layout.

## Prepare the host

Install Docker Engine, the Docker Compose plugin, and Caddy from their official
repositories. If the server has no swap, create one gigabyte:

```bash
sudo fallocate -l 1G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
```

Confirm available resources before deployment:

```bash
nproc
free -h
df -h /
docker version
docker compose version
```

## Configure JieShan

Create a deployment directory containing `compose.yaml` and `.env.example`,
then create a private environment file:

```bash
umask 077
cp .env.example .env
chmod 600 .env
openssl rand -hex 32
```

Set at least:

```dotenv
JIESHAN_ADMIN_PASSWORD=a-long-unique-password
JIESHAN_SECRET_KEY=the-64-character-value-generated-above
```

The secret key must remain stable across restarts and restores. Replacing it
makes stored upstream credentials unreadable.

Keep `JIESHAN_BIND_IP=127.0.0.1` when Caddy runs on the same host. Keep
`JIESHAN_TRUST_PROXY=true` only for the documented trusted reverse proxy path.
Set it to `false` when requests reach JieShan directly.

Public relay sites require no private-network access. Set
`JIESHAN_ALLOW_PRIVATE_UPSTREAMS=true` only when an intended upstream runs on a
private address you control. Leave it disabled otherwise.

## Start and verify

The production host should pull an image built by CI rather than compile Go or
install frontend dependencies:

```bash
docker compose pull
docker compose up -d --no-build
docker compose ps
```

Verify the process, health endpoint, logs, and resource use:

```bash
curl --fail http://127.0.0.1:4000/healthz
docker compose logs --tail=100 jieshan
docker stats --no-stream jieshan
```

Then sign in and verify the product path, not just the process:

1. Add one Site, one Endpoint, and one API Key.
2. Run model discovery and publish one model.
3. Enable monitoring for that model and run its all-sites manual probe.
4. Create a downstream key and make one small API call.
5. Confirm the request log shows its Site, Endpoint, API Key, token usage, and
   attempt timeline.

## HTTPS with Caddy

Copy `deploy/caddy/Caddyfile.example` to the host Caddy configuration, replace
the example domain, point DNS to the server, validate, and reload:

```bash
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

Only ports 22, 80, and 443 need to be public for the panel. Port 4000 remains on
loopback. The example disables response buffering so streaming output reaches
clients without being held by the reverse proxy.

Before DNS and TLS are ready, keep the service private and use an SSH tunnel:

```bash
ssh -L 4000:127.0.0.1:4000 user@your-server
```

## Co-locating a VPN or proxy

Run sing-box, Xray, or another proxy as a separate service with its own restart
policy, memory limit, ports, logs, and data directory. Do not put it inside the
JieShan container or mount the JieShan data volume into it.

Caddy normally owns TCP ports 80 and 443. Give the proxy an explicit different
TCP or UDP port and open only the required firewall rules. If the proxy must
own port 443, use another public IP or an intentionally designed protocol
multiplexer; the example Caddy layout cannot share one IP-and-port pair.

On a one-gigabyte host, watch both RSS and swap after real traffic begins. A
small personal proxy can fit beside JieShan, but a shared VPN can create memory,
file-descriptor, bandwidth, and connection-tracking pressure unrelated to the
number of JieShan administrators.

## Back up SQLite

Back up before every upgrade and before applying a legacy-data migration. A
short maintenance window is the simplest consistent volume backup:

```bash
mkdir -p backups
docker compose stop jieshan
docker run --rm \
  -v jieshan-data:/data:ro \
  -v "$PWD/backups:/backup" \
  alpine:3.22 \
  sh -c 'tar czf /backup/jieshan-$(date +%Y%m%d-%H%M%S).tar.gz -C /data .'
docker compose start jieshan
```

Copy the archive off the server and verify that it contains `jieshan.db` plus
any `-wal` or `-shm` files present in the stopped volume. Store `.env`
separately in an encrypted password manager. The database backup alone is not
enough because the deployment secret is required to decrypt upstream
credentials.

For a non-container installation, stop the process and copy the database and
its adjacent WAL files together, or use SQLite's online backup command. Never
copy only the main database file while the application is actively writing.

## Upgrade and rollback

Pin a semantic version or commit-SHA image for a controlled rollout. Back up,
then upgrade:

```bash
docker compose pull
docker compose up -d --no-build --remove-orphans
docker compose ps
curl --fail http://127.0.0.1:4000/healthz
docker compose logs --tail=100 jieshan
```

After startup, sign in, inspect the monitor matrix, and make one small API call.
Keep the previous image tag and database backup until this verification passes.

To roll back after a schema migration, stop JieShan, point Compose at the
previous image, restore the matching pre-upgrade data volume, restore the same
deployment secret, and start the service. Do not run an older binary against a
database already migrated by a newer release unless that release explicitly
documents backward compatibility.

## One-gigabyte operations

- Monitor only models that can receive traffic.
- Keep discovery and probe concurrency at conservative defaults.
- Keep Docker log rotation enabled as supplied by `compose.yaml`.
- Apply a bounded request-log retention policy and review database growth.
- Build in GitHub Actions or another machine; do not run `pnpm install` or a
  production Go build on the one-gigabyte server.
- Treat sustained swap use, repeated OOM kills, or rising request queue time as
  a capacity problem. Upgrade memory before removing the container limit.
