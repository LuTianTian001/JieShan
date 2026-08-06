# JieShan

JieShan is a clean-room AI relay aggregator for a single administrator. It
combines personally managed upstream websites behind one OpenAI-compatible API,
keeps routing order under explicit user control, and moves traffic away from
unhealthy targets without turning the panel into a billing or commerce system.

It is a new implementation. The Metapi codebase and application structure are
not used as its foundation.

## What it does

- Models an upstream as `Site -> Endpoint -> API Key`, so one website can have
  multiple API addresses and multiple independently managed keys.
- Discovers a site's model list through its model-list endpoint and records
  per-key coverage instead of assuming every key has identical access.
- Publishes only selected models through `/v1/models`,
  `/v1/chat/completions`, and `/v1/responses`.
- Routes sites strictly in the order selected in the panel. There is no weight,
  random selection, balance-based ordering, or automatic priority rewrite.
- Rotates keys inside the current site for key-local failures such as
  `401`, `402`, and `429`; transport, TLS, first-output timeout, stream failure,
  and retryable server errors move the request to the next site.
- Switches the current request immediately after a retryable site failure, but
  does not cool the site on the first independent failure. The default policy
  cools it for five minutes after the second failure in the failure window.
- Monitors only published models with `monitorEnabled` turned on. The default
  interval is five minutes, and one model can be probed across all configured
  sites on demand.
- Issues downstream API keys with optional USD quotas. Usage is charged from a
  versioned official-price snapshot, including a frozen FX snapshot when an
  official price is published in another currency.
- Keeps upstream account balance, subscription, and usage data in the site's
  original units. Account data is informational and never changes routing or
  downstream charges.
- Stores request summaries and per-attempt timelines, including selected site,
  endpoint, key, latency, first-output time, switch reason, token categories,
  reasoning settings, and the price snapshot used for settlement.

JieShan deliberately has no check-in workflow, provider OAuth management,
weighted routing, storefront, recharge, or balance-based failover. Optional
site account connections exist only to display upstream balance, subscription,
and source usage records.

## Architecture

- Go modular monolith for the public gateway, administration API, health state
  machines, background jobs, and SQLite persistence.
- React, TypeScript, and Vite administration panel served by the Go process in
  production.
- SQLite in WAL mode. PostgreSQL and Redis are not required.
- One application container, with Caddy recommended as the host reverse proxy.

Read [Architecture](docs/architecture.md) for the data model, routing rules,
monitoring behavior, billing boundary, and `/api/v2` surface.

## Quick start

Use Docker Engine with the Compose plugin. A two-core, one-gigabyte server is
enough for one administrator and light request volume when it pulls a prebuilt
image. Two gigabytes is recommended when the same host also provides a proxy or
VPN service to other people.

```bash
cp .env.example .env
chmod 600 .env
openssl rand -hex 32
```

Put the generated value in `JIESHAN_SECRET_KEY`, set a strong
`JIESHAN_ADMIN_PASSWORD`, then start JieShan:

```bash
docker compose pull
docker compose up -d --no-build
docker compose ps
curl --fail http://127.0.0.1:4000/healthz
```

The default Compose file binds the service to `127.0.0.1:4000`. Put Caddy or
another TLS reverse proxy in front of it. Before DNS and TLS are ready, use an
SSH tunnel:

```bash
ssh -L 4000:127.0.0.1:4000 user@your-server
```

Then open `http://127.0.0.1:4000` locally. Do not expose the panel, downstream
API, or credentials over public plain HTTP.

See [Deployment](docs/deployment.md) for production setup, one-gigabyte tuning,
backup, upgrade, rollback, and VPN co-location guidance.

## API surfaces

| Surface | Purpose |
| --- | --- |
| `/v1/models` | List enabled published models |
| `/v1/chat/completions` | OpenAI-compatible chat requests |
| `/v1/responses` | OpenAI-compatible Responses requests |
| `/api/v2/sites/*` | Sites, endpoints, API keys, accounts, discovery |
| `/api/v2/published-models/*` | Publication, ordered route targets, probe runs |
| `/api/v2/monitor/matrix` | Selected-model and per-site health matrix |
| `/api/v2/request-logs*` | Filtered request records and summaries |

Administration endpoints require an administrator session. Public inference
endpoints require a JieShan downstream API key.

## Local development

Recommended tools:

- Go 1.25 or newer
- Node.js 22 LTS
- pnpm 10 through Corepack

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

## Configuration and data

Environment variables and conservative container defaults are documented in
`.env.example`. Production requires:

- `JIESHAN_ADMIN_PASSWORD`: administrator password enforced at startup.
- `JIESHAN_SECRET_KEY`: 32 random bytes encoded as 64 hexadecimal characters,
  used to protect upstream credentials and session material.

Persistent state lives in `/data/jieshan.db` inside the named Docker volume.
Back up both the SQLite data and the deployment secret before every upgrade or
legacy-data migration. Never commit `.env`, databases, backups, downstream
keys, upstream keys, passwords, cookies, or refresh tokens.

Read [Migration](docs/migration.md) before moving data from the legacy
prototype. The migration workflow is preview-first, idempotent, and preserves
legacy logs instead of rewriting their meaning.

## Repository layout

```text
cmd/jieshan/       Application entry point
internal/          Backend modules, adapters, routing, billing, and storage
web/               React administration panel
deploy/            Reverse-proxy and deployment examples
docs/              Architecture, deployment, and migration guides
.github/workflows/ Continuous integration and image publishing
```

## License

MIT
