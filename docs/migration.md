# Legacy to V3 migration

Legacy data migration is separate from the automatic SQLite schema migration.
Starting a new binary may create V3 tables, but it must not silently reinterpret
legacy upstream rows, route priorities, account tokens, or request history.

The supported workflow is always:

```text
backup -> preview -> resolve conflicts -> apply -> verify -> remove legacy use
```

Never begin with Apply, and never delete legacy rows immediately after an
apparently successful migration.

## Before preview

1. Pin the currently running image tag and record the JieShan version.
2. Stop write traffic or schedule a short maintenance window.
3. Back up the complete SQLite volume as described in
   [Deployment](deployment.md).
4. Back up the unchanged `JIESHAN_SECRET_KEY` separately. Encrypted API keys
   and account credentials cannot be migrated or restored without it.
5. Confirm that the backup can be listed or extracted before continuing.

Preview is read-only. It must not create Sites, change route order, test keys,
refresh account sessions, run probes, or modify legacy data.

The Preview response includes `item.planFingerprint`, an opaque SHA-256 digest
of every legacy and existing V3 record that can affect the migration result,
plus a versioned normalized manifest of the calculated Preview, Site grouping,
upstream-to-group mapping, settings, and account migration units. Database
values retain their SQLite storage class in the digest. The digest includes
encrypted credential bytes without exposing them. Record the exact value from
the reviewed Preview; it is required by Apply.

## Mapping rules

The migration converts legacy concepts into the V3 hierarchy:

| Legacy data | V3 result |
| --- | --- |
| Upstream rows for the same website | One Site after explicit grouping review |
| Distinct inference base URL and protocol | Endpoint under that Site |
| Each inference key | Ordered API Key under that Site |
| Discovered upstream model | Site Model attached to its Endpoint |
| Public legacy route | Published Model |
| Legacy target order | Ordered Site route targets, preserving first appearance |
| Account configuration, snapshots, and usage | One Site Account when every merged configuration is provably identical |
| Request and attempt history | Preserved as `routingGeneration=legacy` |

Website grouping must use normalized, reviewable origins and never merge Sites
only because their display names match. When two legacy rows point at the same
website but have different inference addresses, they become separate Endpoints
under the proposed Site.

API keys remain separate and retain deterministic order. The migration does not
deduplicate secret values by displaying or exporting them. A key that cannot be
decrypted is reported as blocked rather than replaced with an empty credential.

Legacy route order is converted to Site order. If several legacy keys from one
website appeared as separate route targets, the Site occupies the position of
its first occurrence and those keys become its ordered local key pool. The
migration does not invent weights or sort by balance, latency, name, or current
health.

Check-in and provider OAuth management data are outside V3 and are not
migrated. Account authentication stays encrypted throughout migration; the
migration never decrypts and re-encrypts the credential envelope. Multiple
legacy accounts in one proposed Site are merged only when adapter, API origin,
encrypted authentication payload, enabled state, and capabilities are all
identical. Any difference is a hard conflict that `force` cannot bypass because
the authentication kind or credential equality cannot be inferred safely from
opaque ciphertext.

For an eligible account, all distinct snapshots and usage rows are copied.
Semantically equivalent JSON snapshots at the same capture time are deduplicated. Usage
rows with the same dedupe key are copied once only when every source-owned field
matches; conflicting rows block the migration rather than silently discarding
history. Repeating Apply compares the V3 account and history before inserting,
so it does not duplicate snapshots or usage.

Native Anthropic and Gemini Endpoints are retained for model discovery, but the
current OpenAI gateway cannot route requests to them without translation. Such
legacy targets are reported as skipped in Preview and are not written to
`route_site_targets`. Their Sites, Endpoints, keys, and model catalogs still
migrate normally.

## What preview must show

Review the preview totals and every conflict before applying:

- legacy upstream, model, route, target, account, and log counts;
- proposed Site groups and the legacy rows included in each group;
- Endpoint and API Key counts per Site;
- proposed Published Models and exact ordered Site targets;
- duplicate public names, duplicate endpoints, missing source models, disabled
  records, and invalid URLs;
- empty encrypted key or account credential fields;
- account connections whose adapter, origin, opaque authentication payload, or
  capabilities differ inside one proposed Site;
- records that will be preserved only as legacy history;
- rows that will be skipped, with a concrete reason.

Treat unexpected merges, target-count reductions, lost route positions, and
unexplained skipped rows as blockers. Correct the source data or the proposed
grouping and run preview again.

Apply accepts the reviewed fingerprint and optional force flag:

```json
{
  "planFingerprint": "sha256:<64 hexadecimal characters>",
  "force": false
}
```

A missing fingerprint is rejected with HTTP `400`. If any migration input
changes after Preview, Apply returns HTTP `409` with error code
`legacy_migration_plan_changed` and a new `preview`. Review that replacement
Preview and submit its fingerprint; `force` never bypasses this check.

## Apply behavior

Apply runs as one database transaction and records mappings in
`legacy_upstream_site_mappings` and `legacy_route_published_mappings`. Repeating
Apply after an interruption must be idempotent: mapped rows are verified or
resumed, not duplicated.

The apply operation must preserve legacy tables and logs. It creates V3 Sites,
Endpoints, API Keys, Site Models, Published Models, eligible route targets, and
unambiguous Site Account history; it does not delete or rewrite the legacy
source rows. Legacy failure thresholds below two are raised to two so one
isolated failure cannot immediately cool a Site.

Apply rebuilds and fingerprints the plan inside the same database transaction
that performs the migration. It compares that current fingerprint before any
write, so it cannot apply a reviewed plan to a different consistent snapshot.

Keep public V3 models disabled until the verification checks are complete when
the migration workflow offers that option. This prevents partially reviewed
routes from receiving downstream traffic.

## Verification

After Apply, compare the applied report with Preview and verify the product
behavior:

1. Every expected website appears once as a Site.
2. Endpoint URLs, protocols, and compatibility profiles are correct.
3. API Key counts and order match the preview; secrets remain masked.
4. Each Published Model has the expected official price SKU and exact Site
   order.
5. Run model discovery for one representative Site and review per-key coverage.
6. Enable monitoring for one model and run its all-sites manual probe.
7. Create or use a test downstream key and make one small request.
8. Confirm its V3 request log contains Site, Endpoint, API Key, switch reason,
   token categories, and price snapshot.
9. Confirm historical records still identify themselves as legacy rather than
   being relabeled as V3.
10. Refresh each migrated Site Account manually and confirm that balance,
    currency, subscription, and source usage retain the upstream's units.

Do not remove the pre-migration backup until these checks pass and the new
deployment has run through a normal traffic period.

## Rollback

If Apply or verification fails:

1. Stop JieShan to prevent new writes.
2. Preserve the failed post-migration volume for diagnosis.
3. Restore the complete pre-migration volume into a clean volume.
4. Restore the same `JIESHAN_SECRET_KEY` and pinned pre-migration image.
5. Start the service and verify `/healthz`, administrator login, and one legacy
   request.

Do not attempt rollback by manually deleting selected V3 rows. Foreign-key and
mapping relationships make a full volume restore safer and easier to verify.

## After migration

Keep legacy reads available only for the planned compatibility period. New
configuration changes should be made through V3 Sites and Published Models.
Once backups, monitoring, routing, account display, billing, and request logs
have all been verified, legacy UI paths can be retired without deleting the
historical records needed for audit and troubleshooting.
