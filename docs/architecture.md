# VNext architecture

JieShan VNext is a modular monolith. One Go process owns the public inference
API, administrator API, ordered routing, target health, monitoring, account
synchronization, official-price accounting, retention, and SQLite database.
The React panel is compiled to static assets and served by the same process in
production.

The runtime is intentionally small enough for light personal traffic on a
two-core, two-gigabyte host shared with a separately limited proxy while
keeping routing and accounting behavior explicit and auditable.

## Design invariants

1. The operator's drag order is the route order. Health can temporarily skip a
   target, but it never rewrites or re-sorts the stored order.
2. A website, an inference endpoint, an inference API key, a management-account
   connection, a provider model, and a published model are separate resources.
3. A retryable target failure switches the current request immediately. The
   default circuit policy needs two independent target failures inside five
   minutes before starting a five-minute cooldown.
4. Credential-local failures rotate credentials inside the same target and do
   not penalize the whole site or model route.
5. Only explicitly selected published models are probed. The scheduler does
   not crawl every discovered model.
6. Routing and watchdog policy is one persisted, revisioned global record. A
   published model does not own an independent timeout or cooldown policy.
7. Downstream quota uses versioned official-price USD accounting. Upstream
   balance and raw usage stay in the exact values and units reported by the
   source site.
8. Every request switch is explainable from a persisted attempt timeline.
9. Management API failures, including balance or usage synchronization, never
   take the inference data plane down.

## Domain model

```text
Site
|- Endpoint 1..n
|  `- Provider Model Target 0..n
|- Inference API Key 1..n
|  `- Endpoint and model availability
`- Site Account Connection 0..1
   |- Exact Balance Snapshots
   `- Raw Upstream Usage Records

Published Model
`- Default Ordered Targets 1..n

Routing Profile
`- Sparse Published Model Overrides

Downstream Key
`- Default or selected Routing Profile

Global Runtime Settings
`- Health, watchdog, probe interval, attempts, and retention
```

### Site

A `Site` represents one upstream website or operator. It owns a display name,
dashboard URL, enabled state, and child resources. Balance is not a Site field
because inference endpoints and management accounts have different credentials
and lifecycles.

### Endpoint

An `Endpoint` is one inference API address under a Site. It records the base
URL, wire protocol, native API surface, adapter kind, authentication scheme,
optional header template, enabled state, and stable position. A Site can expose
several endpoints without becoming several route priorities.

The registered native protocol and surface pairs are:

| Protocol | Surface |
| --- | --- |
| OpenAI | Chat Completions |
| OpenAI | Responses |
| Anthropic | Messages |
| Gemini | GenerateContent and streamGenerateContent |

Endpoint capability is explicit. A target is routable only when the registered
adapter implements discovery, request encoding, response or stream decoding,
usage extraction, and error classification for that exact protocol surface.

### Inference API key

An inference API key belongs to a Site and can be bound to one or more of that
Site's endpoints. Keys have explicit order and independent runtime states such
as active, invalid, exhausted, or temporarily cooling. Secret material is
encrypted at rest and is never returned by an administration read API.

Discovery stores per-key model availability because relay sites can expose
different model catalogs to different keys. The router uses this coverage to
avoid a key known not to provide the requested provider model.

### Provider model and published model

A `Provider Model Target` is a model name discovered or entered for one
Endpoint. It retains the upstream model identifier and protocol capability.

A `Published Model` is the stable model name visible to downstream clients. Its
default route contains an ordered list of provider model targets. The public
name defaults to the upstream name but can be an operator-defined alias. The
published model stores its official-price SKU; it does not store cooldown,
timeout, retry, or probe-interval policy.

### Routing profile

The default routing profile is the canonical route for every published model.
A custom profile is sparse: it inherits each default model route until the
operator explicitly overrides that model's enabled state or target order. A
downstream key either follows the default profile or binds to one custom
profile.

Inheritance is resolved before a request starts and the effective profile,
source profile, model revision, and target order are snapshotted into the
request log. A concurrent panel edit therefore cannot change an in-flight
request's meaning.

### Site account connection

A `Site Account Connection` is an optional adapter-backed management
connection. Its only product-facing data is:

- the latest exact balance snapshot, including the upstream-defined unit;
- raw upstream usage records normalized enough for search and display while
  retaining source identifiers, timestamps, token fields, and raw metadata.

The built-in adapter registry supports Ciii, New API, and One API management
surfaces. Ciii can refresh a short-lived session when required. This is adapter
authentication plumbing, not provider OAuth management.

There is no subscription, package, plan, check-in, or inferred monetary value
in the account model. Account data is informational and never changes target
order, health, quota, or downstream settlement.

## Live request flow

1. Authenticate the downstream API key, apply its expiry and rolling RPM
   limit, and resolve its default or selected routing profile.
2. Resolve the requested published model for the incoming native API protocol
   and snapshot the effective ordered targets.
3. Select the model's official-price SKU and reserve quota against the active
   immutable price catalog. An unpriced metered model fails closed.
4. Walk target candidates once in stored order. Open circuits are skipped; an
   expired circuit can grant one half-open trial.
5. For the current target, try eligible inference API keys in configured order.
   Credential-local failures can move to the next key without changing the
   cross-target priority.
6. Network, DNS, TLS, response-read, first-output timeout, stream-idle timeout,
   invalid-success, and retryable upstream failures advance immediately to the
   next target, subject to the global request deadline and attempt limit.
7. Stop replaying after semantic response output has been committed to the
   downstream client. Headers, keepalive frames, or an empty success do not by
   themselves count as semantic output.
8. Persist the site, endpoint, credential snapshot, provider model, protocol,
   timings, status, error class, and switch reason for every attempt.
9. Extract actual token usage, settle against the same catalog snapshot used at
   admission, and release unused reserved quota.

The public data plane exposes:

- `/v1/models`, `/v1/chat/completions`, and `/v1/responses` for OpenAI clients;
- `/v1/models` with Anthropic authentication semantics and `/v1/messages` for
  Anthropic clients;
- `/v1beta/models` and
  `/v1beta/models/{model}:generateContent|streamGenerateContent` for Gemini
  clients.

All surfaces use the same route resolver, health vocabulary, quota ledger, and
attempt log. JieShan does not claim arbitrary cross-protocol translation; a
candidate must support the protocol and surface requested by the client.

## Global runtime policy

`runtime_settings` contains one durable singleton record. `/api/vnext/settings`
updates it with compare-and-swap revision checks, then publishes one immutable
in-process snapshot. Concurrent requests therefore never observe a partially
updated policy.

The authoritative defaults are defined in
`internal/vnext/store/runtime_settings.go`:

| Setting | Default |
| --- | ---: |
| Failure threshold | 2 independent failures |
| Failure window | 5 minutes |
| Cooldown | 5 minutes |
| Probe interval | 5 minutes |
| First semantic output timeout | 15 seconds |
| Stream idle timeout | 60 seconds |
| Total request timeout | 5 minutes |
| Maximum attempts | 4 |
| Operational log retention | 30 days |

Environment variables can seed health and watchdog values only while the
migration-created settings row is untouched. Once settings have been saved,
the database is authoritative across restarts and container recreation.

The global probe interval is materialized into each selected monitor row so
the due-job query remains indexable and restart-safe. That stored interval is a
scheduler implementation detail, not an independent per-model runtime policy.

## Failure and cooldown behavior

The first retryable target failure still switches the current request to the
next candidate, but under the default policy it only marks the target suspect.
A second independent failure inside the five-minute failure window opens the
circuit for five minutes. Duplicate observations from the same incident do not
inflate the count.

After cooldown, one half-open trial is allowed. Success closes the circuit and
clears the failure streak; failure opens it again. A longer valid
`Retry-After` value can extend cooldown. Invalid downstream requests and client
cancellations do not penalize an upstream.

Health is scoped to the published-model target. An unsupported model response
does not automatically disable every model on a Site. Credential runtime state
is separate, so one rejected or rate-limited key does not cool the whole target.

## Discovery

Discovery calls the selected endpoint's model-list API. It can use one chosen
key or the enabled key set and records per-key coverage rather than assuming all
keys are equivalent.

Results are applied only when the discovery operation succeeds against the
same endpoint revision used to start it. A failed or stale operation leaves the
last-known-good provider model catalog in place. Discovery does not issue a
billable chat probe.

## Monitoring

Monitoring selection is per published model; routing policy is global. A model
without an enabled monitor row is not scheduled. The default global interval
is five minutes.

One scheduled or manual model probe checks every configured route target for
that selected model. Target probes use bounded concurrency while credentials
inside a target retain deterministic order. Probe runs and target results are
stored with latency, first-output latency, outcome, failure class, permit
reason, and health-application result.

Probes and live requests feed the same target-health reducer. The monitor
matrix, cooldown state, and gateway eligibility therefore share one source of
truth. Manual probes do not silently enable monitoring for unselected models.

## Site account synchronization

The account scheduler is best effort and serialized per Site. Its defaults are:

| Operation | Default |
| --- | ---: |
| Balance refresh | Every 15 minutes |
| Raw usage synchronization | Every 5 minutes |
| Initial usage lookback | 24 hours |
| Maximum usage work per pass | 3 pages of 100 rows |
| Per-operation timeout | 30 seconds |

A malformed or unavailable management API affects only that Site's account
display. It cannot change inference routing or stop other Sites from syncing.

## Billing boundary

Downstream keys can be unlimited or carry a quota stored in nano-USD. Metered
calls use an immutable versioned official-price catalog:

- a published model maps to a confirmed official-price SKU;
- input, cache-read, output, and reasoning tokens are separate categories;
- prices published in another currency require a frozen, versioned FX snapshot
  before they can be normalized to USD;
- each charged request records catalog version, SKU, token categories, official
  cost, and the amount actually applied to quota; the immutable catalog version
  retains its own source digest;
- unknown models or unsupported token categories are not silently priced at
  zero.

The built-in catalog is imported idempotently for a fresh database. A newer
catalog can be previewed, imported, and activated through the pricing control
plane. Request handling never scrapes provider pricing pages or changes
historical charges.

A valid upstream response and its metering are separate outcomes. If a relay
returns usable content without a trustworthy usage object, JieShan delivers
that response once, records the target as healthy, releases the downstream
reservation, stores token fields as unknown, and marks metering as
`unavailable`. It never retries a completed response merely to obtain usage and
never represents unknown usage as a normal zero-cost call.

Upstream balance and raw upstream usage are reconciliation data only. JieShan
does not apply relay multipliers or derive downstream charge from them.

## Administration API

The panel uses administrator-session-protected `/api/vnext` endpoints. There
is no `/api/v2` or `/api/v3` compatibility namespace.

| Group | Responsibility |
| --- | --- |
| `/api/vnext/auth` | Administrator session lifecycle |
| `/api/vnext/inventory` | Sites, endpoints, credentials, discovery, provider models |
| `/api/vnext/downstream-keys` | Downstream key limits, profile binding, effective models |
| `/api/vnext/site-accounts` | Adapter capabilities, connection, balance, raw usage |
| `/api/vnext/pricing` | Catalog state, preview, import, and activation |
| `/api/vnext/request-logs` | Filtered logs, summary, stable cursor, attempt detail |
| `/api/vnext/monitor` | Selected-model matrix, settings, probe, target history |
| `/api/vnext/settings` | Revisioned global runtime settings |

Mutable records use revisions and ETags so stale panel writes fail explicitly
instead of overwriting newer state. Secrets are accepted on write operations
but omitted from read responses.

## Persistence, retention, and security

SQLite runs with foreign keys, WAL mode, a bounded busy timeout, a four-open /
two-idle default connection pool, bounded journal growth, and automatic WAL
checkpointing. Route
publication, quota reservation and settlement, health reduction, discovery
application, and account snapshot refresh use short transactions.

At request admission, the gateway freezes the effective routing profile,
route revision, ordered candidate identities, candidate credential state, and
initial eligibility. Each attempt then stores both the configured source model
and the model reported by the upstream response. Later route edits, target
deletions, or display-name changes therefore cannot rewrite historical routing
evidence.

The retention worker runs immediately at startup and then daily by default. It
removes expired request-attempt detail, probe history, raw upstream usage rows,
and old balance snapshots according to the global retention setting. Compact
request rows referenced by the quota ledger remain as accounting audit anchors.
The same pass refreshes SQLite planner statistics and requests a passive WAL
checkpoint without waiting for active readers.

The deployment secret protects upstream inference and management credentials.
Administrator sessions are HttpOnly, downstream keys are stored as secure
digests, and structured logs redact authentication material. Prompts and full
model responses are not persisted by the request log.

The production container runs as a non-root user with dropped capabilities and
a read-only root filesystem. Only `/data` and `/tmp` are writable.

## Package boundaries

Production composition begins in `internal/vnext/runtime` and imports only the
VNext modules under `internal/vnext`. The old top-level runtime packages were
deleted; there is no compatibility fallback to an old store, gateway, router,
billing engine, account synchronizer, or HTTP server.

`internal/vnextmigration` is intentionally independent from the production HTTP
runtime. It opens the legacy SQLite source in query-only mode and can either
produce a preview or atomically build a separate, initially empty VNext
database through `cmd/jieshan-migrate`. It never writes into the legacy file
and is not reachable through an administration endpoint.

## Removed product areas

VNext has no check-in automation, package or subscription inference, provider
OAuth management, weighted routing, recharge storefront, public registration,
upstream-balance-driven routing, or hidden legacy settings. These areas are
absent from the product rather than disabled features waiting to be restored.
