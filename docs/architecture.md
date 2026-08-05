# Architecture contract

JieShan is a modular monolith. One Go process owns the public API, admin API,
background jobs, routing decisions, and SQLite database. The React application
is a static client served by the same process in production.

This document defines the boundaries that the implementation should preserve.

## Design goals

1. Keep a single-user installation operational on a two-core, one-gigabyte
   server shared with a lightweight proxy node.
2. Make routing behavior explainable: an administrator chooses the order and
   every switch has a recorded reason.
3. Contain failures at the smallest useful scope: credential, model target, or
   upstream site.
4. Keep downstream quota accounting independent from unreliable upstream
   balances and relay multipliers.
5. Prefer a small number of durable state machines over many loosely coupled
   scheduled tasks.

## Runtime modules

| Module | Responsibility |
| --- | --- |
| Auth | Administrator session and downstream API-key authentication |
| Upstream | Sites, credentials, protocol adapters, balance and usage adapters |
| Catalog | Discovered models, publication state, official price snapshots |
| Routing | Ordered route revisions, target selection, affinity, retry budget |
| Health | Failure classification, suspect state, cooldown, half-open recovery |
| Gateway | OpenAI-compatible request/response translation and streaming |
| Billing | Quota reservation, token settlement, immutable USD cost records |
| Monitor | Selected-model probes, jittered scheduling, result aggregation |
| Audit | Request records and per-attempt timelines with secret redaction |

Modules share one database but interact through explicit service interfaces.
HTTP handlers should validate and translate input; they should not contain
routing, health, or billing policy.

## Core identity

A routable target is the tuple:

```text
(published model, upstream site, credential, upstream model name)
```

Health state is attached to that tuple unless the failure classifier identifies
a narrower credential failure or a broader site failure. This prevents a 404
for one model from disabling every model on the same upstream.

## Request flow

1. Authenticate the downstream key and validate its model access.
2. Load the active official-price snapshot and reserve the maximum permitted
   quota transactionally.
3. Freeze the published model's current ordered route revision for this request.
4. Select the first eligible target, skipping disabled, cooling, and exhausted
   targets while respecting affinity.
5. Translate and send the request with a bounded per-attempt timeout.
6. Retry the next target only while the total request deadline and attempt limit
   allow it. A stream may switch only before its first semantic output.
7. Feed one classified outcome per incident into the shared health reducer.
8. Settle actual token usage in USD using the frozen price snapshot and release
   unused quota.
9. Persist the request summary and complete attempt timeline.

Keepalive frames, headers, and empty HTTP 200 responses are not semantic output.
Once content or a meaningful tool/reasoning event has been emitted, the gateway
must not replay the request on another upstream.

## Failure policy

- The first retryable failure switches the current request immediately and marks
  the target suspect; it does not enter cooldown by itself.
- A second independent failure within the configured window enters cooldown.
- Concurrent copies of the same incident count once.
- The default cooldown is five minutes and is administrator-configurable.
- Recovery grants one half-open lease. Success closes the circuit; failure
  returns it to cooldown.
- `401` isolates the shared credential. `403` penalizes only the current model
  target because relay stations often use it for model-specific access rules.
- Model-level `404` isolates only the model target.
- `429` follows `Retry-After` when present.
- Invalid downstream request `4xx` responses never penalize an upstream.

## Discovery and monitoring

Model discovery calls provider model-list endpoints only. Results are staged,
diffed, and applied in one transaction. A failed or partial synchronization
keeps the last-known-good snapshot and marks it stale. A model is removed only
after two complete successful synchronizations omit it.

Monitoring is opt-in per published model. The default interval is five minutes
with jitter. Probe outcomes use the same classifier and health reducer as real
traffic, so the dashboard and router cannot disagree about target state.

## Data and consistency

SQLite runs in WAL mode with foreign keys enabled and a bounded busy timeout.
Write transactions stay short. Quota reservation, settlement, route revision
publication, and discovery application are transactional boundaries.

Background jobs use database leases so a future multi-process deployment does
not duplicate probes or catalog updates. V1 still runs as one process and does
not require Redis.

## Security boundaries

- Administrator access uses an HttpOnly secure session and an Argon2id password
  verifier.
- Upstream credentials are encrypted with AES-GCM using the deployment secret.
- Downstream keys are displayed once and stored only as a secure digest.
- Authentication headers, cookies, passwords, prompts, and tokens are removed
  from structured logs and backups.
- The container runs as an unprivileged user with a read-only root filesystem;
  only `/data` and `/tmp` are writable.

## Resource budget

The production container is capped at 640 MiB by default and sets a 480 MiB Go
memory limit. Probe and adapter jobs must use bounded worker pools. Request and
attempt logs require retention limits and incremental cleanup; unbounded
in-memory queues are not allowed.
