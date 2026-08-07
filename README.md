# JieShan

JieShan is a clean-room, self-hosted AI API gateway for one administrator or a
small trusted team. It combines independently managed relay sites behind one
stable downstream API, keeps model routing under explicit operator control,
and automatically moves a request away from an unhealthy upstream target.

This repository is a new implementation. It does not run or fall back to the
old Metapi application packages.

## Product scope

- Models an upstream as one `Site` with separate `Endpoint` and `API Key`
  resources. One website can expose several API addresses and use several
  independently managed inference keys.
- Discovers models from an endpoint, records per-key availability, and lets the
  operator publish only the models that should be visible downstream.
- Routes each published model in strict drag order. There is no weighting,
  random selection, balance-based sorting, or hidden priority rewrite.
- Switches the current request immediately after a retryable target failure.
  Under the default policy, one failure marks the target suspect; a second
  independent failure inside five minutes starts a five-minute cooldown.
- Rotates API keys inside the current site for credential-local failures such
  as authentication, quota, and rate-limit errors without cooling the whole
  route target.
- Probes only models explicitly selected for monitoring. The global default
  probe interval is five minutes, and one selected model can be probed across
  all of its ordered targets on demand.
- Supports native OpenAI Chat Completions and Responses, Anthropic Messages,
  and Gemini GenerateContent request and streaming surfaces.
- Issues downstream API keys with optional USD quota, RPM limit, expiry, and a
  selectable routing profile. Custom profiles inherit the global default route
  unless they explicitly override a model.
- Charges downstream usage from an immutable, versioned official-price
  snapshot. Unknown or unverified prices fail closed instead of being treated
  as free.
- Optionally connects a relay site's management API to display its exact
  balance and synchronize raw upstream usage records. These values never
  control routing or downstream billing.
- Stores request summaries and per-attempt timelines with the selected model,
  route profile, site, endpoint, API key snapshot, latency, first output,
  switch reason, token categories, reasoning data, and settlement snapshot.

JieShan deliberately has no check-in workflow, package or subscription
inference, provider OAuth management, storefront, recharge system, weighted
routing, or upstream-balance-based failover. Session refresh inside a site
adapter is implementation plumbing for balance and raw-log synchronization,
not an OAuth product feature.

## Architecture

- Go modular monolith for the public gateway, administrator API, routing,
  monitoring, account synchronization, pricing, retention, and SQLite storage.
- React, TypeScript, and Vite panel served by the Go process in production.
- SQLite in WAL mode. PostgreSQL and Redis are not required.
- One application container. Caddy is recommended as the host reverse proxy.

Read [Architecture](docs/architecture.md) for the data model, global runtime
policy, failure handling, monitoring, accounting boundary, and `/api/vnext`
administration surface.

## Quick start

Use Docker Engine with the Compose plugin. The supplied limits target a
two-core, two-gigabyte server shared with Caddy and a separately limited
Mihomo/Clash service. A one-gigabyte host remains possible only for light
personal traffic with a prebuilt image and tighter limits.

```bash
cp .env.example .env
chmod 600 .env
docker compose pull
docker compose up -d --no-build
docker compose ps
curl --fail http://127.0.0.1:4000/healthz
```

`JIESHAN_ADMIN_PASSWORD` and `JIESHAN_SECRET_KEY` are optional only for a new
data volume. When the password is omitted, read the generated one-time
bootstrap file after the first start, store it securely, and remove the file:

```bash
docker compose exec jieshan cat /data/initial-admin-password.txt
docker compose exec jieshan rm /data/initial-admin-password.txt
```

When `JIESHAN_SECRET_KEY` is omitted, JieShan creates
`/data/jieshan-secret.key`. That file must remain with the database because it
protects stored upstream credentials.

The supplied Compose file binds `127.0.0.1:4000`. Put Caddy or another TLS
reverse proxy in front of it. Before DNS and TLS are ready, use an SSH tunnel:

```bash
ssh -L 4000:127.0.0.1:4000 user@your-server
```

Then open `http://127.0.0.1:4000` locally. Do not expose the panel, downstream
API, or credentials over public plain HTTP.

See [Deployment](docs/deployment.md) for production setup, 2C2G tuning,
backup, upgrade, rollback, and VPN co-location guidance.

## API surfaces

Public inference endpoints require a JieShan downstream API key:

| Surface | Purpose |
| --- | --- |
| `/v1/models` | OpenAI-style model list |
| `/v1/chat/completions` | OpenAI Chat Completions |
| `/v1/responses` | OpenAI Responses |
| `/v1/messages` | Anthropic Messages |
| `/v1beta/models` | Gemini model list |
| `/v1beta/models/{model}:generateContent` | Gemini non-streaming generation |
| `/v1beta/models/{model}:streamGenerateContent` | Gemini streaming generation |

Administrator endpoints require an administrator session and use one
versionless namespace:

| Group | Responsibility |
| --- | --- |
| `/api/vnext/auth` | Login, logout, and session status |
| `/api/vnext/inventory` | Sites, endpoints, API keys, discovery, and provider models |
| `/api/vnext/downstream-keys` | Downstream keys, limits, profile binding, and visible models |
| `/api/vnext/site-accounts` | Account adapters, exact balance, and raw upstream usage |
| `/api/vnext/pricing` | Official catalog preview, import, activation, and state |
| `/api/vnext/request-logs` | Request search, summary, and attempt details |
| `/api/vnext/monitor` | Selected-model matrix, probes, and target history |
| `/api/vnext/settings` | Persisted global routing, timeout, probe, and retention policy |

There is no `/api/v2` compatibility surface in the VNext runtime.

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
`.env.example`. Runtime routing, timeout, probe interval, and log-retention
values are persisted as one revisioned global settings record. Environment
values initialize an untouched database only; later panel changes survive
restarts and are not overwritten by container recreation.

Persistent state lives in `/data/jieshan.sqlite` inside the named Docker
volume. Back up the SQLite files and `jieshan-secret.key` before every upgrade.
Never commit `.env`, databases, backups, downstream keys, upstream keys,
passwords, cookies, access tokens, or refresh tokens.

Read [Migration](docs/migration.md) before upgrading a schema or preserving
configuration from an older prototype. The production runtime never mutates a
legacy database in place; `cmd/jieshan-migrate` performs an explicit offline,
read-only-source conversion into a new VNext database.

## Repository layout

```text
cmd/jieshan/          Application entry point
internal/app/         Process lifecycle and HTTP server
internal/config/      Environment bootstrap configuration
internal/vnext/       Complete production gateway and control-plane modules
internal/vnextmigration/ Offline legacy preview and conversion library
web/                  React administration panel
deploy/               Reverse-proxy examples
docs/                 Architecture, deployment, and migration guides
.github/workflows/    Continuous integration and image publishing
```

The former legacy runtime package tree has been deleted. Production code is
composed from `internal/vnext` and cannot silently fall back to the old store,
gateway, routing, billing, account, or HTTP implementations.

## License

MIT
