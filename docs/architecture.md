# V3 architecture

JieShan V3 is a modular monolith. One Go process owns the public inference API,
administrator API, routing decisions, health state, background jobs, billing,
and SQLite database. The React panel is built as static assets and served by
the same process in production.

The architecture is intentionally small enough for a two-core, one-gigabyte
server while keeping the failure and accounting boundaries explicit.

## Design invariants

1. The administrator's drag order is the route order. Health may temporarily
   skip a target, but it never silently reorders the configured list.
2. A website, an API address, an API key, an account login, and a published
   model are different resources with different lifecycles.
3. Failover happens at the narrowest correct scope: key-local failures rotate a
   key; site-local failures advance to the next site.
4. The first independent site failure is suspicious, not sufficient by itself
   to open the circuit under the default policy.
5. Only explicitly selected models are probed. Monitoring does not crawl every
   discovered model.
6. Downstream quota is official-price USD accounting. Upstream balance and
   subscription values stay exactly as the source site reports them.
7. Every switch is explainable from a persisted request-attempt timeline.

## Domain model

```text
Site
|- Endpoint 1..n
|- API Key 1..n
|- Site Account 0..1
`- Site Model 0..n

Published Model
`- Ordered Route Target 0..n
   `- Site + Endpoint + Site Model
```

### Site

A `Site` represents one upstream website or operator. It owns the display name,
dashboard URL, enabled state, and child resources. Balance is not a Site field
because not every inference endpoint exposes account information.

### Endpoint

An `Endpoint` is an inference API address under a Site. It records the base URL,
wire protocol, compatibility profile, authentication scheme, optional custom
headers, enabled state, and stable position. Multiple endpoints can represent
different compatible API surfaces offered by the same website.

### API Key

An inference `API Key` belongs to a Site, not directly to an Endpoint or route.
Keys have an explicit order and independent runtime states such as active,
invalid, exhausted, or temporarily rate limited. Secret material is encrypted
at rest and is never returned by an administration read API.

### Site Account

A `Site Account` is an optional adapter-backed connection used to display the
upstream's balance, subscriptions, and source usage rows. It is isolated from
inference keys, model health, route order, and downstream billing. JieShan does
not provide provider OAuth management; account adapters use only the explicit
authentication material configured for that site.

### Site Model and Published Model

Discovery records models offered by a Site and Endpoint. Per-key coverage is
stored separately because relay sites can expose different models to different
keys.

A `Published Model` is the stable public name exposed to downstream clients. It
also owns the monitoring flag and its runtime policy: probe interval, failure
window, cooldown, first-output timeout, stream idle timeout, total deadline,
and maximum site attempts.

Each ordered route target connects a Published Model to one Site, Endpoint, and
source model. API keys are resolved inside that Site at request time, so key
availability cannot change the administrator's cross-site order.

## Live request flow

1. Authenticate the downstream API key and validate access to the requested
   public model.
2. Select the Published Model's `officialPriceSKU`, falling back to its public
   name, and reserve quota against the active immutable price snapshot.
3. Resolve the enabled route targets once in stored position order, together
   with each site's eligible API keys in key order.
4. Acquire a health permit for the first eligible site target. Open circuits
   are skipped; an expired circuit permits one half-open trial.
5. Try keys inside the site in order. Authentication, payment, permission, and
   rate-limit failures are key-local and can advance to the next key without
   changing site priority.
6. Network, DNS, TLS, response-read, first-output timeout, stream interruption,
   invalid-success, and retryable server failures advance immediately to the
   next configured site, subject to the total deadline and attempt limit.
7. Stop retrying after semantic streaming output has reached the downstream
   client. Keepalive frames, headers, and an empty successful response are not
   semantic output.
8. Persist the selected Site, Endpoint, API Key, source model, timings, status,
   error class, and switch reason for every attempt.
9. Settle actual token usage against the same price snapshot reserved at the
   start of the request and release unused quota.

The gateway supports `/v1/chat/completions` and `/v1/responses`. Both surfaces
use the same V3 route, health, accounting, and audit state.

## Failure and cooldown policy

The default Published Model policy is:

| Setting | Default |
| --- | ---: |
| Failure threshold | 2 independent failures |
| Failure window | 300 seconds |
| Cooldown | 300 seconds |
| First semantic output timeout | 30 seconds |
| Stream idle timeout | 60 seconds |
| Total request deadline | 120 seconds |
| Maximum site attempts | 3 |

The first retryable site failure still switches the current request to the next
site, but only marks the target suspect. A second independent failure inside
the window opens the circuit and starts cooldown. Duplicate observations from
the same request or probe incident do not inflate the counter.

After cooldown, one half-open request is allowed. Success closes the circuit
and clears the failure streak; failure opens it again. A `Retry-After` value is
honored where applicable. Key-local states remain separate, so one rejected or
rate-limited key does not cool the entire website.

Invalid downstream requests and client cancellations do not penalize an
upstream. Model-specific unsupported responses affect only that route target,
not every model on the Site.

## Discovery

Model discovery calls the configured model-list endpoint rather than issuing a
chat request. It can check one selected key or all enabled keys, follows common
pagination formats, and records an attempt per key.

Results are applied only when the discovery run is complete and its Site and
Endpoint revisions still match the configuration used to start the run. A
failed, partial, or stale run leaves the last-known-good catalog in place.
Coverage records let the router skip keys known not to provide the selected
source model.

## Monitoring

Monitoring is enabled per Published Model through `monitorEnabled`. Disabled or
unpublished models are not scheduled. The default interval is 300 seconds and
can be changed per model.

One scheduled or manual model probe checks every configured route target for
that model. Sites are probed with bounded concurrency; keys within each Site
retain their configured order. Probe runs and individual attempts are stored,
and their outcomes feed the same health reducer as live traffic. This keeps the
monitor matrix and route eligibility consistent.

A manual probe may target one route target or all sites for the model. It does
not enable monitoring for a model that the administrator has not selected.

## Billing boundary

Downstream keys can be unlimited or carry a quota in micro-USD. Metered calls
use a versioned price catalog whose base currency is USD:

- `officialPriceSKU` maps a public model to a confirmed official price row.
- Input, cache-read, output, and reasoning tokens are non-overlapping billing
  categories.
- Models officially priced in another currency use the catalog's frozen FX
  snapshot and are settled in USD.
- Every charged request stores the catalog version, digest, checked date, rate
  band, source currency, FX snapshot, and component cost breakdown.
- A metered key cannot use a model or token category without a confirmed price;
  JieShan does not guess a price or relay multiplier.

This accounting never reads an upstream balance or upstream usage multiplier.
The optional Site Account stores source values as decimal strings with source
currency or raw quota metadata. Those values are for display and reconciliation
only.

## Administration API V2

The V3 panel uses administrator-session-protected `/api/v2` endpoints. The
principal groups are:

| Group | Responsibilities |
| --- | --- |
| `/api/v2/sites` | Site list, create, update, delete |
| `/api/v2/sites/{id}/endpoints` | Endpoint inventory and order |
| `/api/v2/sites/{id}/credentials` | Inference API keys and key order |
| `/api/v2/sites/{id}/account` | Optional account connection, refresh, usage |
| `/api/v2/sites/{id}/model-discoveries` | Persistent discovery runs |
| `/api/v2/sites/{id}/models` | Discovered Site models and key coverage |
| `/api/v2/published-models` | Public model settings and ordered targets |
| `/api/v2/published-models/{id}/probe` | One-model manual probe |
| `/api/v2/monitor/matrix` | Selected models and per-site health |
| `/api/v2/request-logs` | Filtered logs, attempt timelines, stable cursor |
| `/api/v2/request-logs/summary` | Aggregate counts and latency percentiles |
| `/api/v2/migrations/legacy` | Preview fingerprint and guarded legacy migration |

Mutable resources carry revisions. Update and reorder operations use those
revisions to reject stale writes instead of silently overwriting a newer panel
change. Legacy migration Preview returns a content fingerprint; Apply must echo
that fingerprint and rejects a changed plan before writing. Secrets are
accepted on writes but omitted from reads.

## Persistence and security

SQLite runs with foreign keys, WAL mode, and a bounded busy timeout. Route
publication, health reduction, quota reservation and settlement, discovery
application, and account snapshot refresh use short transactional boundaries.

The deployment secret protects upstream inference and account credentials.
Administrator sessions are HttpOnly, downstream keys are stored as secure
digests, and structured logs redact authentication material. Prompts and full
model responses are not stored by default.

The production container runs without root capabilities, uses a read-only root
filesystem, and writes only to `/data` and `/tmp`.

## Removed product areas

V3 has no check-in automation, check-in records, provider OAuth management,
weighted routing, recharge storefront, upstream balance-driven routing, or
hidden legacy settings. These are intentionally absent rather than disabled
features waiting to be re-enabled.
