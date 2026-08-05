# Deployment

This guide targets one administrator running JieShan, Caddy, and a light
sing-box or Xray node on a two-core, one-gigabyte Debian server. That host size
is suitable for low concurrency. Use two gigabytes when the proxy node serves
other people or carries sustained traffic.

## Host layout

The default Compose limits reserve room for the operating system and the proxy
node:

| Component | Suggested limit or working range |
| --- | --- |
| JieShan | 640 MiB memory, 768 MiB memory-plus-swap, 480 MiB Go limit |
| Caddy | Usually tens of MiB under light traffic |
| sing-box or Xray | Configuration-dependent; keep its cache and logs bounded |
| Swap | 1 GiB, used for brief peaks rather than normal operation |

Do not install PostgreSQL, Redis, a desktop environment, or a large server
panel on this host. Build images in GitHub Actions or another machine; the
server should normally pull and run the image.

## Prepare the server

Install Docker Engine, the Docker Compose plugin, and Caddy from their official
repositories. Enable a one-gigabyte swap file if the server has none:

```bash
sudo fallocate -l 1G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
```

Verify available capacity before deployment:

```bash
nproc
free -h
df -h /
docker version
docker compose version
```

## Configure JieShan

Create a deployment directory containing `compose.yaml` and `.env.example`, then
create the private environment file:

```bash
umask 077
cp .env.example .env
chmod 600 .env
openssl rand -hex 32
```

Edit `.env` and set at least:

```dotenv
JIESHAN_ADMIN_PASSWORD=a-long-unique-password
JIESHAN_SECRET_KEY=the-64-character-value-generated-above
```

Keep the default `JIESHAN_BIND_IP=127.0.0.1` when Caddy is installed on the
host. Do not expose port 4000 to the public Internet over plain HTTP. Before
DNS and TLS are ready, administer the panel through an SSH tunnel from the
local computer:

```bash
ssh -L 4000:127.0.0.1:4000 user@your-server
```

Keep `JIESHAN_TRUST_PROXY=true` for the documented loopback Caddy setup. Set it
to `false` if requests reach JieShan without a trusted reverse proxy.

Public upstreams require no extra network setting. If an upstream intentionally
runs on your own LAN or Docker host, set
`JIESHAN_ALLOW_PRIVATE_UPSTREAMS=true`. Leave it disabled otherwise; metadata,
link-local, and multicast addresses remain blocked in either mode.

## Start and verify

The production Compose file only pulls and runs the image published by GitHub
Actions. Do not compile the frontend or Go binary on a one-gigabyte server:

```bash
docker compose pull
docker compose up -d --no-build
```

If the GitHub Container Registry package is private, authenticate Docker with a
read-only package token before pulling it. Never place that token in
`compose.yaml` or `.env`.

Verify health and bounded resource use:

```bash
docker compose ps
curl --fail http://127.0.0.1:4000/healthz
docker stats --no-stream jieshan
docker compose logs --tail=100 jieshan
```

## HTTPS with Caddy

Copy `deploy/caddy/Caddyfile.example` to the host Caddy configuration, replace
`panel.example.com`, point the domain's DNS records to the server, and reload
Caddy:

```bash
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

Only ports 22, 80, and 443 need to be publicly reachable for the panel. Keep
port 4000 bound to loopback. The example disables response buffering so model
streaming reaches clients immediately.

Caddy owns TCP ports 80 and 443 in this layout. Give sing-box or Xray a
different explicit TCP or UDP port and open only that port in the firewall. If
the proxy protocol must own port 443, use another public IP or design an
explicit protocol multiplexer; the provided Caddy example does not share one
IP-and-port pair with the proxy node.

## Upgrade

The workflow publishes `latest`, branch, semantic-version, and commit-SHA image
tags. Pin a semantic version or SHA for a controlled production rollout.

```bash
docker compose pull
docker compose up -d --no-build --remove-orphans
docker compose ps
curl --fail http://127.0.0.1:4000/healthz
docker compose logs --tail=100 jieshan
```

Check `/healthz`, sign in, and make one small API request after each upgrade.
Keep the previous image tag available until that verification passes. Only
after verification should unused images be removed:

```bash
docker image prune -f
```

## Back up SQLite

For the first release, use a short maintenance window to produce a consistent
volume backup:

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

Store backups somewhere other than this server. The deployment secret is needed
to decrypt upstream credentials, so back up `.env` separately in an encrypted
password manager. Do not put it inside the database archive.

To restore, stop JieShan, preserve the current volume, extract the selected
archive into a new empty volume, and start the pinned application version. Test
the procedure before relying on it.

## Operations on a one-gigabyte host

- Keep probe concurrency low and monitor only models that are actually routed.
- Retain a bounded number of request and attempt logs.
- Leave Docker's log rotation enabled as provided by `compose.yaml`.
- Avoid building on the server; `pnpm install` and Go compilation can cause avoidable
  memory peaks.
- Watch swap activity. Sustained swap usage means traffic or retention settings
  exceed the host's practical capacity.
- Run the personal proxy node as a separate service with its own restart policy,
  limits, and logs. It should not share JieShan's container or data volume.

If the container is killed for memory repeatedly, first lower probe concurrency
and log retention. Upgrade to two gigabytes before removing the memory limit.
