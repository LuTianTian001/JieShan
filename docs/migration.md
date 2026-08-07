# Data migration and upgrades

JieShan has two different migration concerns:

1. **VNext schema upgrades** update an existing VNext SQLite database when a
   newer JieShan image starts.
2. **Legacy application conversion** moves useful configuration from an older
   Metapi or prototype database into the clean VNext model.

Only the first path is automatic. The production runtime does not silently
reinterpret an older application's tables, credentials, routes, balances, or
request history.

## VNext schema upgrades

`internal/vnext/store` owns the production schema and applies ordered SQLite
migrations when a VNext database opens. Schema migration is forward-only:

```text
verified backup -> start newer image -> automatic schema migration -> verify
```

Before an image upgrade:

1. Pin and record the currently running image tag.
2. Stop JieShan for the simplest consistent backup.
3. Archive the complete data volume, including `jieshan.sqlite`, any WAL files,
   and `jieshan-secret.key`.
4. Verify that the archive can be listed or extracted.
5. Keep the old image and backup until the upgraded product path passes.

The deployment secret is part of the data set. Restoring the database without
the same key makes stored inference and management credentials unreadable.

After upgrade, verify:

- `/healthz` succeeds;
- administrator login succeeds;
- `/api/vnext/settings` returns the previously saved revisioned global policy;
- Site, Endpoint, inference key, published model, and route order are intact;
- selected monitor rows and recent health history are readable;
- downstream quota and the active official-price catalog are unchanged;
- one small inference request succeeds and its attempt log is complete;
- configured Site accounts still show exact balance and raw upstream usage.

Do not run an older image against a database already migrated by a newer
release. Rollback means restoring the matching pre-upgrade volume and secret,
then starting the older image.

## Legacy conversion boundary

The VNext runtime is a clean implementation. The former top-level legacy
runtime packages have been deleted, and production startup has no fallback to
their store, gateway, routing, billing, account, or HTTP behavior. Legacy
conversion is deliberately offline:

- startup never guesses that a database belongs to an older application;
- there is no `/api/v2` compatibility API or browser-accessible migration
  endpoint;
- the legacy SQLite source is opened in query-only mode;
- the destination path must not already contain a database;
- conversion is built in a temporary staging file and installed atomically;
- a repeated run refuses to overwrite the installed destination.

`cmd/jieshan-migrate` converts supported legacy configuration, encrypted
inference credentials, downstream key digests and limits, account connections,
exact balance snapshots, and raw upstream usage into a new VNext database. It
requires the existing 32-byte `JIESHAN_SECRET_KEY`, because that key is needed
to decrypt legacy secrets and seal them with the new record-bound format.

## Supported legacy workflow

```text
stop old instance
-> archive old database and secret
-> copy the database to a read-only migration workspace
-> run offline conversion into a new path
-> start VNext on a different loopback port
-> verify routes, keys, monitoring, accounting, balance, and logs
-> switch the public endpoint
-> retain the old image and backup until accepted
```

Never point both processes at the same SQLite file or mount one writable data
volume into both containers.

Build the migration binary on a machine with Go 1.25 or newer, then run it in a
maintenance window. `--openai-surface` is mandatory because an older generic
OpenAI endpoint does not prove whether it should become Chat Completions,
Responses, or both. Models whose names are not exact built-in official pricing
SKUs require one or more explicit `--price MODEL=SKU` mappings.

```bash
read -rsp 'Existing JIESHAN_SECRET_KEY: ' JIESHAN_SECRET_KEY
echo
export JIESHAN_SECRET_KEY
./jieshan-migrate \
  --source /migration/legacy.db \
  --destination /migration/jieshan.sqlite \
  --openai-surface chat \
  --price old-public-alias=gpt-4o
```

The command prints a JSON result containing migrated row counts, non-revealable
downstream key count, selected OpenAI policy, catalog version, and warnings.
Review that output before the new database is installed into the production
volume.

## Converted resources

The converter maps only resources used by the VNext product:

| Old concept | VNext resource |
| --- | --- |
| One relay website or operator | Site |
| One inference API base URL and native surface | Endpoint |
| One upstream inference secret | Site inference API key |
| Model available from an Endpoint | Provider Model Target |
| Downstream-visible model name | Published Model |
| Normal target priority | Default routing profile order |
| Alternate ordered route for selected clients | Sparse custom routing profile |
| Distributed client credential | New downstream key |
| Optional management connection | Site Account Connection |

After conversion, inspect every Site/Endpoint boundary, the exact native
surface, API-key binding order, published-model alias, and route order. Older
generic OpenAI metadata and duplicated upstream rows can require manual review;
the converter will not infer priority from balance, latency, historical
success, multiplier, package size, or display name.

Old weighted routes must be converted to explicit drag order. Do not derive
priority from balance, latency, historical success, multiplier, package size,
or display name.

## Data intentionally not converted

The following old product areas have no VNext destination and should remain
only in the archived legacy database:

- check-in automation and check-in history;
- provider OAuth configuration;
- subscription, package, plan, or quota-to-money inference;
- storefront, recharge, payment, or public-registration state;
- weighted or random route configuration;
- upstream balance-driven routing decisions;
- obsolete application settings and UI preferences.

A Site account connection stores only exact balance snapshots and raw upstream
usage records. It does not import or display a subscription or package model.
Management-session refresh, when required by an adapter such as Ciii, is only a
means to refresh those two data sets.

Old gateway request logs are not recalculated using today's official prices.
Historical request records and relay multipliers have different meanings, so
the archived legacy database remains the source of truth for that history.
Imported raw Site account usage stays informational and separate from VNext
downstream billing.

## Secrets and downstream keys

Do not export plaintext secrets into notes, shell history, screenshots, issue
attachments, or source control. Re-enter upstream API keys through the VNext
panel so they are encrypted by the current deployment secret.

Legacy downstream key digests are preserved, so a client that still has its raw
key can continue authenticating after cutover. The panel cannot reveal a key
whose legacy database stored only the digest. Rotate any lost, shared, or
uncertain key and verify clients one at a time.

Site management authentication can include an authorization token, access and
refresh token pair, or cookie depending on the adapter. These values remain
encrypted at rest. Their presence does not reintroduce OAuth management as a
product area.

## Cutover verification

Before moving all clients, test at least one published model with two ordered
targets when available:

1. Confirm the downstream key lists only the effective published models.
2. Call the model through its native protocol surface.
3. Confirm the first healthy target is selected.
4. Create a controlled retryable failure and confirm the same request advances
   to the second target.
5. Confirm one independent failure marks the first target suspect but does not
   cool it under the default policy.
6. Confirm the second independent failure inside five minutes opens a
   five-minute cooldown.
7. Confirm the target returns through a half-open trial after cooldown.
8. Confirm the request log records effective and source routing profiles,
   ordered attempts, timings, switch reason, token usage, and price snapshot.
9. Confirm monitoring covers only selected models and a manual model probe
   checks all configured targets.
10. Confirm downstream quota changes by official USD pricing only.
11. Confirm upstream balance and raw usage remain separate informational data.

Perform smoke tests for each protocol actually used by clients:

- OpenAI `/v1/chat/completions` and `/v1/responses`;
- Anthropic `/v1/messages`;
- Gemini `/v1beta/models/{model}:generateContent` or
  `:streamGenerateContent`.

## Rollback during legacy cutover

Keep the old instance and its data read-only but available until acceptance.
If VNext verification fails:

1. Stop sending new clients to VNext.
2. Restore the previous client endpoint or DNS record.
3. Leave the VNext database intact for diagnosis; do not manually delete
   selected rows to simulate rollback.
4. Correct configuration or restore the fresh VNext backup, then repeat the
   full verification checklist.

Retire the old instance only after routes, monitoring, downstream quotas,
request logs, and every client protocol in use have been verified. Archive the
old database and its matching secret according to the required retention
period; do not copy its obsolete runtime packages back into VNext.
