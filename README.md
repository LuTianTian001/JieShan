# JieShan

JieShan is a compact API gateway for aggregating personally managed AI relay
stations. It publishes one downstream API, keeps an explicit user-defined
upstream order, monitors only selected models, and automatically moves traffic
away from unhealthy targets.

This repository is a clean-room rewrite. It does not inherit the history or
application structure of Metapi.

## Product scope

- Add OpenAI-compatible upstream sites and API keys.
- Discover each upstream's models without issuing chat requests.
- Publish selected models through one downstream endpoint.
- Route strictly by drag order, with immediate per-request failover.
- Cool down repeatedly failing model targets and recover them with half-open
  probes.
- Monitor only selected models and show each upstream result beneath the model.
- Issue downstream keys with USD quotas based on versioned official prices.
- Record a request log and complete upstream-attempt timeline without storing
  prompts by default.

Check-in, OAuth management, commerce, weighted routing, and balance-based
routing are deliberately outside the core product.

Upstream balance, subscription, and usage-log adapters are not implemented in
the current release. They do not participate in routing or downstream billing.

## Architecture

- Go modular monolith for the API, routing state machine, jobs, and SQLite
  persistence.
- React, TypeScript, and Vite for the administration panel.
- SQLite in WAL mode; no PostgreSQL or Redis is required.
- One application container, with Caddy recommended as the host reverse proxy.

See [Architecture](docs/architecture.md) for the runtime boundaries and request
flow.

## Quick start with Docker Compose

Requirements: Docker Engine with the Compose plugin and at least 1 GB RAM. Use
a prebuilt image on a one-gigabyte host; two gigabytes is recommended when the
same server also carries a proxy node used by other people.

```bash
cp .env.example .env
chmod 600 .env
openssl rand -hex 32
```

Put the generated value in `JIESHAN_SECRET_KEY`, set a strong
`JIESHAN_ADMIN_PASSWORD`, then start the service:

```bash
docker compose pull
docker compose up -d --no-build
docker compose ps
```

By default the panel listens only on `127.0.0.1:4000`. Put Caddy or another TLS
reverse proxy in front of it. For temporary administration before DNS and TLS
are ready, keep the loopback binding and use an SSH tunnel:

```bash
ssh -L 4000:127.0.0.1:4000 user@your-server
```

Then open `http://127.0.0.1:4000` on the local computer. Never expose the panel
or downstream API to the public Internet over plain HTTP.

The complete production procedure, backup steps, and 2-core/1-GB tuning are in
[Deployment](docs/deployment.md).

## Local development

Recommended tools:

- Go 1.25 or newer
- Node.js 22 LTS
- pnpm 10 (through Corepack)

```bash
cd web
corepack enable
pnpm install --frozen-lockfile
pnpm run build
cd ..
go test ./...
go run ./cmd/jieshan
```

The backend reads the built frontend from `web/dist` by default. Run
`pnpm run dev` inside `web` for Vite development.

## Configuration

All configuration is supplied through environment variables. The authoritative
list and conservative container defaults are documented in `.env.example`.
The two secrets required for a production start are:

- `JIESHAN_ADMIN_PASSWORD`: administrator password enforced at startup.
- `JIESHAN_SECRET_KEY`: 32 random bytes encoded as 64 hexadecimal characters,
  used to protect upstream credentials and session material.

Never commit `.env`, database files, exported backups, downstream keys, or
upstream credentials.

## Repository layout

```text
cmd/jieshan/       Application entry point
internal/          Backend modules and adapters
web/               React administration panel
deploy/            Reverse-proxy and deployment examples
docs/              Architecture and operations documentation
.github/workflows/ Continuous integration and image publishing
```

## License

MIT
