import type {
  AuthScheme,
  InferenceSurface,
  GatewayLogSummary,
  ModelRoute,
  ModelTarget,
  MonitorProbePoint,
  PriceCatalog,
  PriceCatalogSummary,
  ProviderModel,
  RouteTarget,
  WireProtocol,
} from '../lib/types';
import { createPrototypeState } from './fixtures';
import type {
  PrototypeController,
  PrototypeRequestLog,
  PrototypeState,
} from './types';

const ADMIN_PREFIX = '/api/vnext/';
const DATA_PREFIX = '/v1/';
const CSRF_COOKIE = 'jieshan_admin_csrf';
const MINUTE = 60_000;

let activeController: PrototypeController | null = null;

function clone<T>(value: T): T {
  return structuredClone(value);
}

function headers(revision?: number, extra?: HeadersInit): Headers {
  const value = new Headers(extra);
  value.set('Content-Type', 'application/json; charset=utf-8');
  value.set('Cache-Control', 'no-store');
  value.set('X-Content-Type-Options', 'nosniff');
  if (revision !== undefined) value.set('ETag', `"${revision}"`);
  return value;
}

function json(value: unknown, status = 200, revision?: number, extra?: HeadersInit): Response {
  return new Response(JSON.stringify(value), { status, headers: headers(revision, extra) });
}

function noContent(): Response {
  return new Response(null, {
    status: 204,
    headers: { 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff' },
  });
}

function error(status: number, code: string, message: string): Response {
  return json({ error: { code, message } }, status);
}

async function requestBody<T>(request: Request): Promise<T | Response> {
  try {
    return await request.json() as T;
  } catch {
    return error(400, 'invalid_request', 'Request body must contain one valid JSON object.');
  }
}

function revisionError(request: Request, current: number): Response | null {
  const raw = request.headers.get('If-Match')?.trim();
  if (!raw) return error(428, 'precondition_required', 'If-Match is required.');
  if (!/^"[1-9]\d*"$/.test(raw)) return error(400, 'invalid_revision', 'If-Match must contain one strong numeric revision.');
  if (Number(raw.slice(1, -1)) !== current) return error(412, 'revision_conflict', 'Resource changed; refresh and try again.');
  return null;
}

function createRevisionError(request: Request): Response | null {
  return request.headers.get('If-Match')?.trim() === '*'
    ? null
    : error(428, 'precondition_required', 'If-Match: * is required when creating a routing profile.');
}

function csrfError(request: Request, state: PrototypeState): Response | null {
  return request.headers.get('X-CSRF-Token') === state.csrfToken
    ? null
    : error(403, 'csrf_rejected', 'Administrator CSRF validation failed.');
}

function nextID(state: PrototypeState, key: string): number {
  const value = state.nextIds[key] || 1;
  state.nextIds[key] = value + 1;
  return value;
}

function endpointDefaults(protocol: WireProtocol): { authScheme: AuthScheme; surface: InferenceSurface } {
  switch (protocol) {
    case 'anthropic': return { authScheme: 'x-api-key', surface: 'anthropic.messages' };
    case 'gemini': return { authScheme: 'x-goog-api-key', surface: 'gemini.generate_content' };
    default: return { authScheme: 'bearer', surface: 'openai.chat_completions' };
  }
}

function previewModels(protocol: WireProtocol): string[] {
  switch (protocol) {
    case 'anthropic': return ['claude-sonnet-4-5', 'claude-opus-4-1', 'claude-haiku-4-5'];
    case 'gemini': return ['gemini-2.5-pro', 'gemini-2.5-flash', 'gemini-2.0-flash'];
    default: return ['gpt-5.2', 'gpt-5.2-mini', 'o4-mini'];
  }
}

function profileModels(state: PrototypeState, profileId: number): string[] {
  return state.routes
    .filter((route) => route.routingProfileId === profileId && route.enabled)
    .map((route) => route.publicName);
}

function downstreamKeyResponse(item: PrototypeState['downstreamKeys'][number]): Record<string, unknown> {
  const { billingMultiplier, ...rest } = item;
  return { ...rest, billingMultiplierBPS: Math.round(billingMultiplier * 10_000) };
}

function refreshKeyProjection(state: PrototypeState, profileId?: number): void {
  for (const key of state.downstreamKeys) {
    if (profileId !== undefined && key.routingProfileId !== profileId) continue;
    const profile = state.routingProfiles.find((item) => item.id === key.routingProfileId);
    key.routingProfileName = profile?.name || key.routingProfileName;
    key.usesDefaultRoutingProfile = Boolean(profile?.isDefault);
    key.models = profileModels(state, key.routingProfileId);
    key.updatedAt = Date.now();
  }
}

function refreshEndpointModelUsability(state: PrototypeState, endpointId: number): void {
  const bound = state.endpointCredentialIds[String(endpointId)] || [];
  const boundCredentials = bound.flatMap((id) => {
    const credential = state.credentials.find((item) => item.id === id);
    return credential ? [credential] : [];
  });
  const usable = bound.filter((id) => state.credentials.some((credential) => credential.id === id && credential.enabled && credential.runtimeState !== 'invalid' && credential.runtimeState !== 'exhausted')).length;
  for (const target of state.modelTargets.filter((item) => item.endpointId === endpointId)) {
    target.boundCredentialCount = bound.length;
    target.usableCredentialCount = usable;
    target.credentialIds = boundCredentials.map((item) => item.id);
    target.credentialNames = boundCredentials.map((item) => item.name);
    target.routable = target.enabled && target.endpointEnabled && target.siteEnabled && usable > 0;
    target.updatedAt = Date.now();
  }
}

function makeRouteTargets(
  state: PrototypeState,
  publishedModelId: number,
  providerTargetIds: number[],
  now: number,
): RouteTarget[] | Response {
  const targets: RouteTarget[] = [];
  for (const [position, providerTargetId] of providerTargetIds.entries()) {
    const model = state.modelTargets.find((item) => item.id === providerTargetId);
    if (!model) return error(400, 'invalid_target', `Provider model target ${providerTargetId} was not found.`);
    if (!model.routable) return error(409, 'target_not_routable', `Provider model target ${providerTargetId} is not routable.`);
    targets.push({
      id: publishedModelId * 100_000 + providerTargetId,
      publishedModelId,
      siteId: model.siteId,
      siteName: model.siteName,
      endpointId: model.endpointId,
      endpointName: model.endpointName,
      providerModelTargetId: providerTargetId,
      sourceModel: model.sourceModel,
      wireProtocol: model.wireProtocol,
      apiSurface: model.surface,
      position,
      revision: 1,
      createdAt: now,
      updatedAt: now,
    });
  }
  return targets;
}

function clonedInheritedRoute(source: ModelRoute, profileId: number, profileName: string, now: number): ModelRoute {
  return {
    ...clone(source),
    routingProfileId: profileId,
    routingProfileName: profileName,
    sourceProfileId: source.routingProfileId,
    sourceProfileName: source.routingProfileName,
    inherited: true,
    targetsOverridden: false,
    createdAt: now,
    updatedAt: now,
  };
}

function monitorTargetFor(state: PrototypeState, providerTargetId: number) {
  for (const model of state.monitorSnapshot.items) {
    const target = model.targets.find((item) => item.providerModelTargetId === providerTargetId);
    if (target) return { model, target };
  }
  return null;
}

function recomputeMonitorModel(model: PrototypeState['monitorSnapshot']['items'][number]): void {
  model.successes = model.targets.reduce((sum, item) => sum + item.successes, 0);
  model.failures = model.targets.reduce((sum, item) => sum + item.failures, 0);
  model.skipped = model.targets.reduce((sum, item) => sum + item.skipped, 0);
  const attempted = model.successes + model.failures;
  model.successBasisPoints = attempted ? Math.round((model.successes / attempted) * 10_000) : 0;
  if (!model.monitor.enabled) model.status = 'disabled';
  else if (model.targets.some((item) => item.status === 'unavailable')) model.status = 'degraded';
  else if (model.targets.some((item) => item.status === 'cooling' || item.status === 'suspect')) model.status = 'degraded';
  else if (model.targets.some((item) => item.status === 'healthy')) model.status = 'healthy';
  else model.status = 'unprobed';
}

function syncMonitorRoute(state: PrototypeState, route: ModelRoute): void {
  const model = state.monitorSnapshot.items.find((item) => item.publishedModelId === route.publishedModelId);
  if (!model) return;
  model.publicModel = route.publicName;
  model.officialPriceSku = route.officialPriceSku;
  model.publishedModelEnabled = route.enabled;
  model.publishedModelRevision = route.publishedModelRevision;
  model.targets = [...route.targets].sort((a, b) => a.position - b.position).map((routeTarget) => {
    const provider = state.modelTargets.find((item) => item.id === routeTarget.providerModelTargetId);
    const existing = model.targets.find((item) => item.providerModelTargetId === routeTarget.providerModelTargetId);
    const health = state.routingHealth[routeTarget.providerModelTargetId] || null;
    const target = existing || {
      publishedModelTargetId: routeTarget.id, publishedModelTargetRevision: routeTarget.revision,
      providerModelTargetId: routeTarget.providerModelTargetId, providerModelTargetRevision: provider?.revision || 1,
      position: routeTarget.position, siteId: routeTarget.siteId, siteName: routeTarget.siteName,
      endpointId: routeTarget.endpointId, endpointName: routeTarget.endpointName, sourceModel: routeTarget.sourceModel,
      wireProtocol: routeTarget.wireProtocol, apiSurface: routeTarget.apiSurface, enabled: provider?.enabled ?? true,
      usableCredentialCount: provider?.usableCredentialCount || 0, status: 'unprobed' as const,
      successes: 0, failures: 0, skipped: 0, successBasisPoints: 0, latest: null, statusBar: [], health,
    };
    Object.assign(target, {
      publishedModelTargetId: routeTarget.id, publishedModelTargetRevision: routeTarget.revision,
      providerModelTargetRevision: provider?.revision || target.providerModelTargetRevision,
      position: routeTarget.position, siteId: routeTarget.siteId, siteName: routeTarget.siteName,
      endpointId: routeTarget.endpointId, endpointName: routeTarget.endpointName, sourceModel: routeTarget.sourceModel,
      wireProtocol: routeTarget.wireProtocol, apiSurface: routeTarget.apiSurface,
      enabled: provider?.enabled ?? target.enabled, usableCredentialCount: provider?.usableCredentialCount ?? target.usableCredentialCount,
      health: health ? clone(health) : target.health,
    });
    const historyKey = `${route.publishedModelId}:${routeTarget.providerModelTargetId}`;
    const history = state.monitorHistories[historyKey];
    const identity = { publishedModelTargetId: routeTarget.id, providerModelTargetId: routeTarget.providerModelTargetId, position: routeTarget.position, siteId: routeTarget.siteId, siteName: routeTarget.siteName, endpointId: routeTarget.endpointId, endpointName: routeTarget.endpointName, sourceModel: routeTarget.sourceModel, wireProtocol: routeTarget.wireProtocol, apiSurface: routeTarget.apiSurface };
    if (history) history.target = identity;
    else state.monitorHistories[historyKey] = { publishedModelId: route.publishedModelId, publicModel: route.publicName, target: identity, status: target.status, successes: 0, failures: 0, skipped: 0, total: 0, attempted: 0, successBasisPoints: 0, health: target.health, order: 'oldest_first', items: [] };
    return target;
  });
  recomputeMonitorModel(model);
}

function refreshRouteTargetMetadata(state: PrototypeState): void {
  for (const route of state.routes) {
    for (const target of route.targets) {
      const provider = state.modelTargets.find((item) => item.id === target.providerModelTargetId);
      if (!provider) continue;
      Object.assign(target, { siteId: provider.siteId, siteName: provider.siteName, endpointId: provider.endpointId, endpointName: provider.endpointName, sourceModel: provider.sourceModel, wireProtocol: provider.wireProtocol, apiSurface: provider.surface, updatedAt: Date.now() });
    }
    if (route.routingProfileId === 1) syncMonitorRoute(state, route);
  }
}

function appendProbe(
  state: PrototypeState,
  providerTargetId: number,
  outcome: MonitorProbePoint['outcome'],
  details: { httpStatus?: number | null; failureKind?: string; errorCode?: string; permitReason?: string } = {},
): void {
  const found = monitorTargetFor(state, providerTargetId);
  if (!found) return;
  const now = Date.now();
  const failed = outcome === 'failure';
  const skipped = outcome === 'skipped';
  const point: MonitorProbePoint = {
    id: nextID(state, 'attempt'),
    runId: `probe_manual_${providerTargetId}_${now}`,
    providerModelTargetRevision: found.target.providerModelTargetRevision,
    outcome,
    permitMode: found.target.health?.phase || 'closed',
    permitReason: details.permitReason || (skipped ? 'cooldown_active' : 'manual_probe'),
    httpStatus: details.httpStatus ?? (failed ? 503 : skipped ? null : 200),
    failureKind: details.failureKind || (failed ? 'upstream_unavailable' : ''),
    errorCode: details.errorCode || (failed ? 'upstream_503' : ''),
    totalLatencyMs: skipped ? 0 : failed ? 720 : 430,
    firstOutputMs: failed || skipped ? null : 210,
    startedAt: now - (skipped ? 0 : failed ? 720 : 430),
    finishedAt: now,
    healthApplied: !skipped,
    healthApplyReason: skipped ? 'not_applied' : failed ? 'failure_recorded' : 'success_recorded',
    healthErrorCode: '',
  };
  const limit = found.model.monitor.historyLimit;
  found.target.statusBar = [...found.target.statusBar, point].slice(-limit);
  found.target.latest = point;
  if (outcome === 'success') found.target.successes += 1;
  else if (outcome === 'failure') found.target.failures += 1;
  else found.target.skipped += 1;
  const attempted = found.target.successes + found.target.failures;
  found.target.successBasisPoints = attempted ? Math.round((found.target.successes / attempted) * 10_000) : 0;
  if (outcome === 'success') found.target.status = 'healthy';
  else if (outcome === 'failure') found.target.status = 'degraded';
  else found.target.status = 'cooling';
  const historyKey = `${found.model.publishedModelId}:${providerTargetId}`;
  const history = state.monitorHistories[historyKey];
  if (history) {
    history.items = [...history.items, point].slice(-limit);
    history.status = found.target.status;
    history.successes = found.target.successes;
    history.failures = found.target.failures;
    history.skipped = found.target.skipped;
    history.total = history.items.length;
    history.attempted = found.target.successes + found.target.failures;
    history.successBasisPoints = found.target.successBasisPoints;
    history.health = clone(found.target.health);
  }
  recomputeMonitorModel(found.model);
}

function updateRuntimeHealth(state: PrototypeState, providerTargetId: number, succeeded: boolean, failureKind = '', credentialId?: number): void {
  const now = Date.now();
  const modelTarget = state.modelTargets.find((item) => item.id === providerTargetId);
  const health = state.routingHealth[providerTargetId] ||= {
    phase: 'closed', capability: 'available', consecutiveFailures: 0, failureWindowStartedAt: null,
    lastFailureAt: null, lastSuccessAt: null, cooldownUntil: null, halfOpenLeaseUntil: null,
    lastEventAt: null, lastFailureKind: '', providerTargetRevision: modelTarget?.revision || 1,
    stateVersion: 1, updatedAt: now,
  };
  const credential = state.credentials.find((item) => item.id === credentialId);
  if (succeeded) {
    health.phase = 'closed';
    health.consecutiveFailures = 0;
    health.failureWindowStartedAt = null;
    health.lastSuccessAt = now;
    health.cooldownUntil = null;
    health.lastFailureKind = '';
  } else {
    if (!health.failureWindowStartedAt || now - health.failureWindowStartedAt > state.settings.failureWindowMs) {
      health.failureWindowStartedAt = now;
      health.consecutiveFailures = 0;
    }
    health.consecutiveFailures += 1;
    health.lastFailureAt = now;
    health.lastFailureKind = failureKind;
    if (health.consecutiveFailures >= state.settings.failureThreshold) {
      health.phase = 'cooling';
      health.cooldownUntil = now + state.settings.cooldownMs;
    } else {
      health.phase = 'suspect';
    }
  }
  health.lastEventAt = now;
  health.updatedAt = now;
  health.stateVersion += 1;
  for (const model of state.monitorSnapshot.items) {
    const target = model.targets.find((item) => item.providerModelTargetId === providerTargetId);
    if (!target) continue;
    target.health = clone(health);
    target.status = health.phase === 'cooling' ? 'cooling' : health.phase === 'suspect' ? 'suspect' : health.phase === 'recovering' ? 'recovering' : 'healthy';
    const history = state.monitorHistories[`${model.publishedModelId}:${providerTargetId}`];
    if (history) {
      history.health = clone(health);
      history.status = target.status;
    }
    recomputeMonitorModel(model);
  }
  if (credential) {
    credential.lastHttpStatus = succeeded ? 200 : 503;
    credential.lastErrorCode = succeeded ? '' : 'upstream_503';
    credential.runtimeRevision += 1;
    credential.runtimeUpdatedAt = now;
    credential.runtimeState = credential.enabled ? 'ready' : 'disabled';
    credential.coolingUntil = null;
  }
}

function setCSRFCookie(token: string): void {
  if (typeof document === 'undefined') return;
  document.cookie = `${CSRF_COOKIE}=${encodeURIComponent(token)}; Path=/; SameSite=Strict`;
}

function clearCSRFCookie(): void {
  if (typeof document === 'undefined') return;
  document.cookie = `${CSRF_COOKIE}=; Path=/; Max-Age=0; SameSite=Strict`;
}

function providerModel(item: PrototypeState['modelTargets'][number]): ProviderModel {
  return {
    id: item.id,
    siteId: item.siteId,
    endpointId: item.endpointId,
    sourceModel: item.sourceModel,
    displayName: item.displayName,
    enabled: item.enabled,
    revision: item.revision,
    lastSeenAt: item.lastSeenAt,
    createdAt: item.createdAt,
    updatedAt: item.updatedAt,
  };
}

function logListItem(item: PrototypeRequestLog): Omit<PrototypeRequestLog, 'routeCandidates' | 'attempts' | 'ledger'> {
  const { routeCandidates: _routeCandidates, attempts: _attempts, ledger: _ledger, ...result } = item;
  return result;
}

function percentile(values: number[], position: number): number | null {
  if (!values.length) return null;
  const sorted = [...values].sort((left, right) => left - right);
  return sorted[Math.min(sorted.length - 1, Math.max(0, Math.ceil(sorted.length * position) - 1))];
}

function logSummary(items: PrototypeRequestLog[]): GatewayLogSummary {
  const durations = items.flatMap((item) => item.totalDurationMs === null ? [] : [item.totalDurationMs]);
  const firstOutputs = items.flatMap((item) => item.firstOutputMs === null ? [] : [item.firstOutputMs]);
  const succeeded = items.filter((item) => item.status === 'success').length;
  return {
    requests: items.length,
    succeeded,
    failed: items.filter((item) => item.status === 'failed').length,
    cancelled: items.filter((item) => item.status === 'cancelled').length,
    running: items.filter((item) => item.status === 'running').length,
    successBasisPoints: items.length ? Math.round((succeeded / items.length) * 10_000) : 0,
    totalChargedNanoUsd: items.reduce((sum, item) => sum + item.chargedNanoUsd, 0),
    totalOfficialNanoUsd: items.reduce((sum, item) => sum + item.officialCostNanoUsd, 0),
    totalAttempts: items.reduce((sum, item) => sum + Math.max(item.attempts.length, (item.finalAttemptIndex ?? -1) + 1), 0),
    requestsWithSwitches: items.filter((item) => (item.finalAttemptIndex ?? 0) > 0 || item.attempts.length > 1).length,
    averageDurationMs: durations.length ? Math.round(durations.reduce((sum, value) => sum + value, 0) / durations.length) : null,
    p50DurationMs: percentile(durations, 0.5),
    p95DurationMs: percentile(durations, 0.95),
    p50FirstOutputMs: percentile(firstOutputs, 0.5),
    p95FirstOutputMs: percentile(firstOutputs, 0.95),
  };
}

function matchesLog(item: PrototypeRequestLog, query: URLSearchParams): boolean {
  const search = query.get('search')?.trim().toLowerCase();
  const status = query.get('status');
  const model = query.get('model');
  const surface = query.get('surface');
  const downstreamKeyId = Number(query.get('downstreamKeyId') || 0);
  const siteId = Number(query.get('siteId') || 0);
  const from = Number(query.get('from') || 0);
  const to = Number(query.get('to') || 0);
  if (search && ![item.id, item.downstreamKeyName, item.publicModel, item.errorCode]
    .some((value) => value.toLowerCase().includes(search))) return false;
  if (status && item.status !== status) return false;
  if (model && item.publicModel !== model) return false;
  if (surface && item.apiSurface !== surface) return false;
  if (downstreamKeyId && item.downstreamKeyId !== downstreamKeyId) return false;
  if (siteId && !item.routeCandidates.some((candidate) => candidate.siteId === siteId)) return false;
  if (from && item.startedAt < from) return false;
  if (to && item.startedAt > to) return false;
  return true;
}

function downstreamSecret(request: Request, state: PrototypeState): string | null {
  const authorization = request.headers.get('Authorization')?.trim() || '';
  const bearer = /^Bearer\s+(.+)$/i.exec(authorization)?.[1]?.trim();
  const apiKey = request.headers.get('x-api-key')?.trim()
    || request.headers.get('x-goog-api-key')?.trim()
    || new URL(request.url).searchParams.get('key')?.trim()
    || bearer;
  if (!apiKey) return null;
  return Object.values(state.downstreamSecrets).includes(apiKey) ? apiKey : null;
}

function publicModelsForSecret(secret: string, state: PrototypeState): string[] {
  const entry = Object.entries(state.downstreamSecrets).find(([, value]) => value === secret);
  const key = entry ? state.downstreamKeys.find((item) => item.id === Number(entry[0])) : undefined;
  if (!key || !key.enabled) return [];
  return key.models;
}

function priceSummary(item: PriceCatalog, state: PrototypeState): PriceCatalogSummary {
  return {
    version: item.version,
    digest: item.digest || '',
    settlement_currency: item.settlement_currency || 'USD',
    source: item.source,
    source_digest: item.source_digest,
    entry_count: item.entries.length,
    effective_at: item.effective_at,
    verified_at: item.verified_at || '',
    imported_at: item.imported_at || '',
    active: state.catalogState.active_version === item.version,
  };
}

function adminGET(path: string, url: URL, state: PrototypeState): Response | null {
  if (path === '/api/vnext/auth/status') {
    return json(state.authenticated
      ? { initialized: true, authenticated: true, username: state.username, expires_at: state.sessionExpiresAt }
      : { initialized: true, authenticated: false });
  }

  if (!state.authenticated) {
    return error(401, 'unauthenticated', 'Administrator session is not authenticated.');
  }

  if (path === '/api/vnext/inventory/sites') return json({ items: clone(state.sites) });
  const siteMatch = /^\/api\/vnext\/inventory\/sites\/(\d+)$/.exec(path);
  if (siteMatch) {
    const item = state.sites.find((site) => site.id === Number(siteMatch[1]));
    return item ? json({ item: clone(item) }, 200, item.revision) : error(404, 'not_found', 'Site was not found.');
  }

  const endpointsMatch = /^\/api\/vnext\/inventory\/sites\/(\d+)\/endpoints$/.exec(path);
  if (endpointsMatch) {
    const siteId = Number(endpointsMatch[1]);
    return json({ items: clone(state.endpoints.filter((item) => item.siteId === siteId).sort((a, b) => a.position - b.position)) });
  }
  const endpointMatch = /^\/api\/vnext\/inventory\/sites\/(\d+)\/endpoints\/(\d+)$/.exec(path);
  if (endpointMatch) {
    const item = state.endpoints.find((endpoint) => endpoint.siteId === Number(endpointMatch[1]) && endpoint.id === Number(endpointMatch[2]));
    return item ? json({ item: clone(item) }, 200, item.revision) : error(404, 'not_found', 'Endpoint was not found.');
  }

  const credentialsMatch = /^\/api\/vnext\/inventory\/sites\/(\d+)\/credentials$/.exec(path);
  if (credentialsMatch) {
    const siteId = Number(credentialsMatch[1]);
    return json({ items: clone(state.credentials.filter((item) => item.siteId === siteId)) });
  }
  const credentialMatch = /^\/api\/vnext\/inventory\/sites\/(\d+)\/credentials\/(\d+)$/.exec(path);
  if (credentialMatch) {
    const item = state.credentials.find((credential) => credential.siteId === Number(credentialMatch[1]) && credential.id === Number(credentialMatch[2]));
    return item ? json({ item: clone(item) }, 200, item.revision) : error(404, 'not_found', 'Credential was not found.');
  }

  const bindingsMatch = /^\/api\/vnext\/inventory\/sites\/(\d+)\/endpoints\/(\d+)\/credentials$/.exec(path);
  if (bindingsMatch) {
    const siteId = Number(bindingsMatch[1]);
    const credentialIds = state.endpointCredentialIds[bindingsMatch[2]] || [];
    const items = credentialIds.flatMap((credentialId, position) => {
      const credential = state.credentials.find((item) => item.siteId === siteId && item.id === credentialId);
      return credential ? [{ credentialId, credentialName: credential.name, position, enabled: credential.enabled, createdAt: credential.createdAt, updatedAt: credential.updatedAt }] : [];
    });
    return json({ items });
  }

  const modelsMatch = /^\/api\/vnext\/inventory\/sites\/(\d+)\/endpoints\/(\d+)\/models$/.exec(path);
  if (modelsMatch) {
    const siteId = Number(modelsMatch[1]);
    const endpointId = Number(modelsMatch[2]);
    return json({ items: state.modelTargets.filter((item) => item.siteId === siteId && item.endpointId === endpointId).map(providerModel) });
  }

  if (path === '/api/vnext/inventory/model-targets') {
    const q = url.searchParams.get('q')?.trim().toLowerCase();
    const protocol = url.searchParams.get('protocol');
    const surface = url.searchParams.get('surface');
    const siteId = Number(url.searchParams.get('siteId') || 0);
    const enabled = url.searchParams.get('enabled');
    const items = state.modelTargets.filter((item) => {
      if (q && ![item.sourceModel, item.displayName, item.siteName, item.endpointName].some((value) => value.toLowerCase().includes(q))) return false;
      if (protocol && item.wireProtocol !== protocol) return false;
      if (surface && item.surface !== surface) return false;
      if (siteId && item.siteId !== siteId) return false;
      if (enabled !== null && item.enabled !== (enabled === 'true')) return false;
      return true;
    });
    return json({ items: clone(items) });
  }

  if (path === '/api/vnext/downstream-keys') return json({ items: state.downstreamKeys.map(downstreamKeyResponse) });
  const keyMatch = /^\/api\/vnext\/downstream-keys\/(\d+)$/.exec(path);
  if (keyMatch) {
    const item = state.downstreamKeys.find((key) => key.id === Number(keyMatch[1]));
    return item ? json({ item: downstreamKeyResponse(item) }, 200, item.revision) : error(404, 'not_found', 'Downstream key was not found.');
  }
  const keyModelsMatch = /^\/api\/vnext\/downstream-keys\/(\d+)\/models$/.exec(path);
  if (keyModelsMatch) {
    const item = state.downstreamKeys.find((key) => key.id === Number(keyModelsMatch[1]));
    return item ? json({ items: clone(item.models) }) : error(404, 'not_found', 'Downstream key was not found.');
  }

  if (path === '/api/vnext/routing-profiles') return json({ items: clone(state.routingProfiles) });
  const profileMatch = /^\/api\/vnext\/routing-profiles\/(\d+)$/.exec(path);
  if (profileMatch) {
    const item = state.routingProfiles.find((profile) => profile.id === Number(profileMatch[1]));
    return item ? json({ item: clone(item) }, 200, item.revision) : error(404, 'not_found', 'Routing profile was not found.');
  }
  const routesMatch = /^\/api\/vnext\/routing-profiles\/(\d+)\/routes$/.exec(path);
  if (routesMatch) {
    const profileId = Number(routesMatch[1]);
    const profile = state.routingProfiles.find((item) => item.id === profileId);
    return profile
      ? json({ items: clone(state.routes.filter((item) => item.routingProfileId === profileId)) }, 200, profile.revision)
      : error(404, 'not_found', 'Routing profile was not found.');
  }
  const routeMatch = /^\/api\/vnext\/routing-profiles\/(\d+)\/routes\/(\d+)$/.exec(path);
  if (routeMatch) {
    const item = state.routes.find((route) => route.routingProfileId === Number(routeMatch[1]) && route.publishedModelId === Number(routeMatch[2]));
    return item ? json({ item: clone(item) }, 200, item.revision) : error(404, 'not_found', 'Published model route was not found.');
  }

  const accountMatch = /^\/api\/vnext\/site-accounts\/sites\/(\d+)$/.exec(path);
  if (accountMatch) {
    const item = state.siteAccounts[Number(accountMatch[1])];
    return item ? json(clone(item), 200, item.revision) : error(404, 'not_found', 'Site account is not configured.');
  }
  const usageMatch = /^\/api\/vnext\/site-accounts\/sites\/(\d+)\/usage$/.exec(path);
  if (usageMatch) {
    const siteId = Number(usageMatch[1]);
    let items = [...(state.siteUsage[siteId] || [])];
    const search = url.searchParams.get('search')?.trim().toLowerCase();
    const model = url.searchParams.get('model');
    const status = url.searchParams.get('status');
    const apiKey = url.searchParams.get('apiKey')?.trim().toLowerCase();
    const requestId = url.searchParams.get('requestId')?.trim().toLowerCase();
    const from = Number(url.searchParams.get('from') || 0);
    const to = Number(url.searchParams.get('to') || 0);
    items = items.filter((item) => {
      if (search && ![item.requestId, item.upstreamRequestId, item.model, item.upstreamModel, item.apiKeyName].some((value) => value.toLowerCase().includes(search))) return false;
      if (model && item.model !== model) return false;
      if (status && item.status !== status) return false;
      if (apiKey && !item.apiKeyName.toLowerCase().includes(apiKey)) return false;
      if (requestId && ![item.requestId, item.upstreamRequestId].some((value) => value.toLowerCase().includes(requestId))) return false;
      if (from && item.occurredAt < from) return false;
      if (to && item.occurredAt > to) return false;
      return true;
    });
    const offset = Math.max(0, Number(url.searchParams.get('cursor') || 0));
    const limit = Math.min(200, Math.max(1, Number(url.searchParams.get('limit') || 50)));
    const page = items.slice(offset, offset + limit);
    const hasMore = offset + limit < items.length;
    return json({ items: clone(page), hasMore, nextCursor: hasMore ? String(offset + limit) : '' });
  }

  if (path === '/api/vnext/pricing/state') return json({ state: clone(state.catalogState) }, 200, state.catalogState.revision);
  if (path === '/api/vnext/pricing/catalogs') {
    return json({ items: state.priceCatalogs.map((item) => priceSummary(item, state)), state: clone(state.catalogState) }, 200, state.catalogState.revision);
  }
  const catalogMatch = /^\/api\/vnext\/pricing\/catalogs\/([^/]+)$/.exec(path);
  if (catalogMatch) {
    const version = decodeURIComponent(catalogMatch[1]);
    const item = state.priceCatalogs.find((candidate) => candidate.version === version);
    return item ? json({ catalog: clone(item) }) : error(404, 'catalog_not_found', 'Price catalog was not found.');
  }

  if (path === '/api/vnext/monitor') return json(clone(state.monitorSnapshot));
  const historyMatch = /^\/api\/vnext\/monitor\/models\/(\d+)\/targets\/(\d+)\/history$/.exec(path);
  if (historyMatch) {
    const key = `${historyMatch[1]}:${historyMatch[2]}`;
    const item = state.monitorHistories[key];
    if (!item) return error(404, 'not_found', 'Monitor history was not found.');
    const limit = Math.min(500, Math.max(1, Number(url.searchParams.get('limit') || 200)));
    return json({ ...clone(item), items: clone(item.items.slice(-limit)) });
  }

  if (path === '/api/vnext/request-logs/summary') {
    return json(logSummary(state.requestLogs.filter((item) => matchesLog(item, url.searchParams))));
  }
  if (path === '/api/vnext/request-logs') {
    const matched = state.requestLogs.filter((item) => matchesLog(item, url.searchParams));
    const offset = Math.max(0, Number(url.searchParams.get('cursor') || 0));
    const limit = Math.min(200, Math.max(1, Number(url.searchParams.get('limit') || 50)));
    const page = matched.slice(offset, offset + limit);
    const hasMore = offset + limit < matched.length;
    return json({ items: page.map(logListItem), hasMore, nextCursor: hasMore ? String(offset + limit) : '' });
  }
  const logMatch = /^\/api\/vnext\/request-logs\/([^/]+)$/.exec(path);
  if (logMatch) {
    const item = state.requestLogs.find((log) => log.id === decodeURIComponent(logMatch[1]));
    return item
      ? json({ request: logListItem(item), routeCandidates: clone(item.routeCandidates), attempts: clone(item.attempts), ledger: clone(item.ledger) })
      : error(404, 'not_found', 'Request log was not found.');
  }

  if (path === '/api/vnext/settings') return json(clone(state.settings), 200, state.settings.revision);
  return null;
}

async function handleAdminMutation(request: Request, path: string, state: PrototypeState): Promise<Response | null> {
  const method = request.method.toUpperCase();
  const now = Date.now();

  if (method === 'POST' && path === '/api/vnext/inventory/sites') {
    const body = await requestBody<{ name?: string; dashboardUrl?: string; enabled?: boolean }>(request);
    if (body instanceof Response) return body;
    const name = body.name?.trim();
    if (!name) return error(400, 'invalid_request', 'Site name is required.');
    const item = {
      id: nextID(state, 'site'), name, dashboardUrl: body.dashboardUrl?.trim() || '', enabled: body.enabled ?? true,
      revision: 1, createdAt: now, updatedAt: now,
    };
    state.sites.push(item);
    return json({ item: clone(item) }, 201, item.revision);
  }

  const siteMatch = /^\/api\/vnext\/inventory\/sites\/(\d+)$/.exec(path);
  if (method === 'PATCH' && siteMatch) {
    const item = state.sites.find((site) => site.id === Number(siteMatch[1]));
    if (!item) return error(404, 'not_found', 'Site was not found.');
    const conflict = revisionError(request, item.revision);
    if (conflict) return conflict;
    const body = await requestBody<{ name?: string; dashboardUrl?: string | null; enabled?: boolean }>(request);
    if (body instanceof Response) return body;
    if (body.name !== undefined) {
      const name = body.name.trim();
      if (!name) return error(400, 'invalid_request', 'Site name cannot be empty.');
      item.name = name;
    }
    if (body.dashboardUrl !== undefined) item.dashboardUrl = body.dashboardUrl?.trim() || '';
    if (body.enabled !== undefined) item.enabled = body.enabled;
    item.revision += 1;
    item.updatedAt = now;
    for (const target of state.modelTargets.filter((candidate) => candidate.siteId === item.id)) {
      target.siteName = item.name;
      target.siteEnabled = item.enabled;
      target.routable = target.enabled && target.endpointEnabled && target.siteEnabled && target.usableCredentialCount > 0;
    }
    refreshRouteTargetMetadata(state);
    return json({ item: clone(item) }, 200, item.revision);
  }

  const endpointsMatch = /^\/api\/vnext\/inventory\/sites\/(\d+)\/endpoints$/.exec(path);
  if (method === 'POST' && endpointsMatch) {
    const siteId = Number(endpointsMatch[1]);
    if (!state.sites.some((site) => site.id === siteId)) return error(404, 'not_found', 'Site was not found.');
    const body = await requestBody<{ name?: string; baseUrl?: string; wireProtocol?: WireProtocol; surface?: InferenceSurface; adapterKind?: string; authScheme?: AuthScheme; headers?: Record<string, string>; enabled?: boolean }>(request);
    if (body instanceof Response) return body;
    if (!body.name?.trim() || !body.baseUrl?.trim() || !body.wireProtocol) return error(400, 'invalid_request', 'Endpoint name, baseUrl, and wireProtocol are required.');
    const defaults = endpointDefaults(body.wireProtocol);
    const position = state.endpoints.filter((item) => item.siteId === siteId).reduce((max, item) => Math.max(max, item.position), -1) + 1;
    const item = {
      id: nextID(state, 'endpoint'), siteId, name: body.name.trim(), baseUrl: body.baseUrl.trim().replace(/\/$/, ''),
      wireProtocol: body.wireProtocol, surface: body.surface || defaults.surface, adapterKind: body.adapterKind?.trim() || body.wireProtocol,
      authScheme: body.authScheme || defaults.authScheme, headers: body.headers || {}, secretHeadersConfigured: false,
      position, enabled: body.enabled ?? true, revision: 1, createdAt: now, updatedAt: now,
    };
    state.endpoints.push(item);
    state.endpointCredentialIds[String(item.id)] = [];
    state.discoveredModels[String(item.id)] = [];
    return json({ item: clone(item) }, 201, item.revision);
  }

  const endpointMatch = /^\/api\/vnext\/inventory\/sites\/(\d+)\/endpoints\/(\d+)$/.exec(path);
  if (method === 'PATCH' && endpointMatch) {
    const item = state.endpoints.find((endpoint) => endpoint.siteId === Number(endpointMatch[1]) && endpoint.id === Number(endpointMatch[2]));
    if (!item) return error(404, 'not_found', 'Endpoint was not found.');
    const conflict = revisionError(request, item.revision);
    if (conflict) return conflict;
    const body = await requestBody<Partial<{ name: string; baseUrl: string; wireProtocol: WireProtocol; surface: InferenceSurface; adapterKind: string; authScheme: AuthScheme; headers: Record<string, string>; enabled: boolean }>>(request);
    if (body instanceof Response) return body;
    if (body.name !== undefined) item.name = body.name.trim();
    if (body.baseUrl !== undefined) item.baseUrl = body.baseUrl.trim().replace(/\/$/, '');
    if (body.wireProtocol !== undefined) item.wireProtocol = body.wireProtocol;
    if (body.surface !== undefined) item.surface = body.surface;
    if (body.adapterKind !== undefined) item.adapterKind = body.adapterKind.trim();
    if (body.authScheme !== undefined) item.authScheme = body.authScheme;
    if (body.headers !== undefined) item.headers = body.headers;
    if (body.enabled !== undefined) item.enabled = body.enabled;
    item.revision += 1;
    item.updatedAt = now;
    for (const target of state.modelTargets.filter((candidate) => candidate.endpointId === item.id)) {
      target.endpointName = item.name;
      target.endpointEnabled = item.enabled;
      target.baseUrl = item.baseUrl;
      target.wireProtocol = item.wireProtocol;
      target.surface = item.surface;
      target.adapterKind = item.adapterKind;
      target.authScheme = item.authScheme;
      target.routable = target.enabled && target.endpointEnabled && target.siteEnabled && target.usableCredentialCount > 0;
    }
    refreshRouteTargetMetadata(state);
    return json({ item: clone(item) }, 200, item.revision);
  }

  const credentialsMatch = /^\/api\/vnext\/inventory\/sites\/(\d+)\/credentials$/.exec(path);
  if (method === 'POST' && credentialsMatch) {
    const siteId = Number(credentialsMatch[1]);
    if (!state.sites.some((site) => site.id === siteId)) return error(404, 'not_found', 'Site was not found.');
    const body = await requestBody<{ name?: string; secret?: string; enabled?: boolean }>(request);
    if (body instanceof Response) return body;
    if (!body.name?.trim() || !body.secret?.trim()) return error(400, 'invalid_request', 'Credential name and secret are required.');
    const item = {
      id: nextID(state, 'credential'), siteId, name: body.name.trim(), secretConfigured: true, enabled: body.enabled ?? true,
      revision: 1, runtimeState: 'ready', coolingUntil: null, lastHttpStatus: null, lastErrorCode: '',
      runtimeRevision: 1, runtimeUpdatedAt: now, createdAt: now, updatedAt: now,
    };
    state.credentials.push(item);
    return json({ item: clone(item) }, 201, item.revision);
  }

  const credentialMatch = /^\/api\/vnext\/inventory\/sites\/(\d+)\/credentials\/(\d+)$/.exec(path);
  if (method === 'PATCH' && credentialMatch) {
    const item = state.credentials.find((credential) => credential.siteId === Number(credentialMatch[1]) && credential.id === Number(credentialMatch[2]));
    if (!item) return error(404, 'not_found', 'Credential was not found.');
    const conflict = revisionError(request, item.revision);
    if (conflict) return conflict;
    const body = await requestBody<{ name?: string; enabled?: boolean }>(request);
    if (body instanceof Response) return body;
    if (body.name !== undefined) item.name = body.name.trim();
    if (body.enabled !== undefined) item.enabled = body.enabled;
    item.revision += 1;
    item.updatedAt = now;
    for (const [endpointId, ids] of Object.entries(state.endpointCredentialIds)) {
      if (ids.includes(item.id)) refreshEndpointModelUsability(state, Number(endpointId));
    }
    refreshRouteTargetMetadata(state);
    return json({ item: clone(item) }, 200, item.revision);
  }

  const secretMatch = /^\/api\/vnext\/inventory\/sites\/(\d+)\/credentials\/(\d+)\/secret$/.exec(path);
  if (method === 'PUT' && secretMatch) {
    const item = state.credentials.find((credential) => credential.siteId === Number(secretMatch[1]) && credential.id === Number(secretMatch[2]));
    if (!item) return error(404, 'not_found', 'Credential was not found.');
    const conflict = revisionError(request, item.revision);
    if (conflict) return conflict;
    const body = await requestBody<{ secret?: string }>(request);
    if (body instanceof Response) return body;
    if (!body.secret?.trim()) return error(400, 'invalid_request', 'Credential secret is required.');
    item.secretConfigured = true;
    item.revision += 1;
    item.updatedAt = now;
    return json({ item: clone(item) }, 200, item.revision);
  }

  const bindingsMatch = /^\/api\/vnext\/inventory\/sites\/(\d+)\/endpoints\/(\d+)\/credentials$/.exec(path);
  if (method === 'PUT' && bindingsMatch) {
    const siteId = Number(bindingsMatch[1]);
    const endpoint = state.endpoints.find((item) => item.siteId === siteId && item.id === Number(bindingsMatch[2]));
    if (!endpoint) return error(404, 'not_found', 'Endpoint was not found.');
    const conflict = revisionError(request, endpoint.revision);
    if (conflict) return conflict;
    const body = await requestBody<{ credentialIds?: number[] }>(request);
    if (body instanceof Response) return body;
    const credentialIds = [...new Set(body.credentialIds || [])];
    if (credentialIds.some((id) => !state.credentials.some((credential) => credential.id === id && credential.siteId === siteId))) {
      return error(400, 'invalid_credential', 'Every bound credential must belong to this site.');
    }
    state.endpointCredentialIds[String(endpoint.id)] = credentialIds;
    endpoint.revision += 1;
    endpoint.updatedAt = now;
    refreshEndpointModelUsability(state, endpoint.id);
    refreshRouteTargetMetadata(state);
    const items = credentialIds.map((credentialId, position) => {
      const credential = state.credentials.find((item) => item.id === credentialId)!;
      return { credentialId, credentialName: credential.name, position, enabled: credential.enabled, createdAt: now, updatedAt: now };
    });
    return json({ endpoint: clone(endpoint), items }, 200, endpoint.revision);
  }

  if (method === 'POST' && path === '/api/vnext/inventory/model-discovery/preview') {
    const body = await requestBody<{ baseUrl?: string; wireProtocol?: WireProtocol; surface?: InferenceSurface; authScheme?: AuthScheme; apiKey?: string }>(request);
    if (body instanceof Response) return body;
    if (!body.baseUrl?.trim() || !body.wireProtocol || !body.surface || !body.authScheme || !body.apiKey?.trim()) {
      return error(400, 'invalid_request', 'API address, type, authentication, and key are required.');
    }
    return json({ models: previewModels(body.wireProtocol), complete: true });
  }

  const discoverMatch = /^\/api\/vnext\/inventory\/sites\/(\d+)\/endpoints\/(\d+)\/models\/discover$/.exec(path);
  if (method === 'POST' && discoverMatch) {
    const siteId = Number(discoverMatch[1]);
    const endpointId = Number(discoverMatch[2]);
    const body = await requestBody<{ credentialId?: number }>(request);
    if (body instanceof Response) return body;
    if (!state.credentials.some((item) => item.id === body.credentialId && item.siteId === siteId && item.enabled)) {
      return error(400, 'invalid_credential', 'An enabled credential from this site is required.');
    }
    if (!state.endpoints.some((item) => item.id === endpointId && item.siteId === siteId)) return error(404, 'not_found', 'Endpoint was not found.');
    const items = (state.discoveredModels[String(endpointId)] || []).map((sourceModel) => {
      const imported = state.modelTargets.find((item) => item.endpointId === endpointId && item.sourceModel === sourceModel);
      return { sourceModel, imported: Boolean(imported), targetId: imported?.id || null, enabled: imported?.enabled ?? null, revision: imported?.revision ?? null };
    });
    return json({ items });
  }

  const importMatch = /^\/api\/vnext\/inventory\/sites\/(\d+)\/endpoints\/(\d+)\/models\/import$/.exec(path);
  if (method === 'POST' && importMatch) {
    const siteId = Number(importMatch[1]);
    const endpointId = Number(importMatch[2]);
    const site = state.sites.find((item) => item.id === siteId);
    const endpoint = state.endpoints.find((item) => item.id === endpointId && item.siteId === siteId);
    if (!site || !endpoint) return error(404, 'not_found', 'Endpoint was not found.');
    const body = await requestBody<{ credentialId?: number; models?: string[] }>(request);
    if (body instanceof Response) return body;
    if (!state.credentials.some((item) => item.id === body.credentialId && item.siteId === siteId && item.enabled)) return error(400, 'invalid_credential', 'An enabled credential from this site is required.');
    const imported: ProviderModel[] = [];
    for (const sourceModel of [...new Set((body.models || []).map((item) => item.trim()).filter(Boolean))]) {
      let model = state.modelTargets.find((item) => item.endpointId === endpointId && item.sourceModel === sourceModel);
      if (!model) {
        const bound = state.endpointCredentialIds[String(endpointId)] || [];
        model = {
          id: nextID(state, 'providerModel'), siteId, endpointId, sourceModel, displayName: sourceModel, enabled: true,
          revision: 1, lastSeenAt: now, createdAt: now, updatedAt: now, siteName: site.name, siteEnabled: site.enabled,
          endpointName: endpoint.name, endpointEnabled: endpoint.enabled, baseUrl: endpoint.baseUrl, wireProtocol: endpoint.wireProtocol,
          surface: endpoint.surface, adapterKind: endpoint.adapterKind, authScheme: endpoint.authScheme,
          boundCredentialCount: bound.length, usableCredentialCount: bound.filter((id) => state.credentials.some((item) => item.id === id && item.enabled)).length,
          credentialIds: [...bound], credentialNames: bound.flatMap((id) => {
            const credential = state.credentials.find((item) => item.id === id);
            return credential ? [credential.name] : [];
          }),
          unknownCredentialCount: 0, capabilities: { discovery: true, request: true, response: true, stream: true, usage: true, error: true },
          routable: endpoint.enabled && site.enabled && bound.some((id) => state.credentials.some((item) => item.id === id && item.enabled)),
        };
        state.modelTargets.push(model);
      } else {
        model.lastSeenAt = now;
        model.updatedAt = now;
      }
      imported.push(providerModel(model));
    }
    return json({ items: imported });
  }

  if (method === 'POST' && path === '/api/vnext/downstream-keys') {
    const body = await requestBody<{ name?: string; quotaNanoUSD?: number | null; hourlyQuotaNanoUSD?: number | null; billingMultiplierBPS?: number; rpm?: unknown; expires?: number | null; routingProfileId?: number | null; enabled?: boolean }>(request);
    if (body instanceof Response) return body;
    if (body.rpm !== undefined) return error(400, 'invalid_request', 'rpm is no longer supported.');
    if (!body.name?.trim()) return error(400, 'invalid_request', 'Downstream key name is required.');
    if (body.billingMultiplierBPS !== undefined && (!Number.isInteger(body.billingMultiplierBPS) || body.billingMultiplierBPS < 0 || body.billingMultiplierBPS > 10_000_000)) return error(400, 'invalid_request', 'billingMultiplierBPS must be an integer between 0 and 10000000.');
    const profile = body.routingProfileId
      ? state.routingProfiles.find((item) => item.id === body.routingProfileId)
      : state.routingProfiles.find((item) => item.isDefault);
    if (!profile) return error(400, 'invalid_routing_profile', 'Routing profile was not found.');
    const id = nextID(state, 'downstreamKey');
    const secret = `js_prototype_${id}_${now.toString(36)}`;
    const item = {
      id, name: body.name.trim(), keyPrefix: secret.slice(0, 14), enabled: body.enabled ?? true, revealable: true,
      quotaNanoUSD: body.quotaNanoUSD ?? null, usedNanoUSD: 0, reservedNanoUSD: 0,
      hourlyQuotaNanoUSD: body.hourlyQuotaNanoUSD ?? null, usedThisHourNanoUSD: 0,
      reservedThisHourNanoUSD: 0, hourlyWindowStartedAt: now,
      billingMultiplier: (body.billingMultiplierBPS ?? 10_000) / 10_000,
      expires: body.expires ?? null, lastUsedAt: null, revision: 1, routingProfileId: profile.id,
      routingProfileName: profile.name, usesDefaultRoutingProfile: profile.isDefault, models: profileModels(state, profile.id),
      createdAt: now, updatedAt: now,
    };
    state.downstreamKeys.push(item);
    state.downstreamSecrets[id] = secret;
    profile.downstreamKeyCount += 1;
    return json({ item: downstreamKeyResponse(item), secret }, 201, item.revision);
  }

  const keyMatch = /^\/api\/vnext\/downstream-keys\/(\d+)$/.exec(path);
  if (method === 'PATCH' && keyMatch) {
    const item = state.downstreamKeys.find((key) => key.id === Number(keyMatch[1]));
    if (!item) return error(404, 'not_found', 'Downstream key was not found.');
    const conflict = revisionError(request, item.revision);
    if (conflict) return conflict;
    const body = await requestBody<{ name?: string; quotaNanoUSD?: number | null; hourlyQuotaNanoUSD?: number | null; billingMultiplierBPS?: number; rpm?: unknown; expires?: number | null; routingProfileId?: number | null; enabled?: boolean }>(request);
    if (body instanceof Response) return body;
    if (body.rpm !== undefined) return error(400, 'invalid_request', 'rpm is no longer supported.');
    if (body.billingMultiplierBPS !== undefined && (!Number.isInteger(body.billingMultiplierBPS) || body.billingMultiplierBPS < 0 || body.billingMultiplierBPS > 10_000_000)) return error(400, 'invalid_request', 'billingMultiplierBPS must be an integer between 0 and 10000000.');
    if (body.name !== undefined) item.name = body.name.trim();
    if (body.quotaNanoUSD !== undefined) item.quotaNanoUSD = body.quotaNanoUSD;
    if (body.hourlyQuotaNanoUSD !== undefined) item.hourlyQuotaNanoUSD = body.hourlyQuotaNanoUSD;
    if (body.billingMultiplierBPS !== undefined) item.billingMultiplier = body.billingMultiplierBPS / 10_000;
    if (body.expires !== undefined) item.expires = body.expires;
    if (body.enabled !== undefined) item.enabled = body.enabled;
    if (body.routingProfileId !== undefined) {
      const nextProfile = body.routingProfileId
        ? state.routingProfiles.find((profile) => profile.id === body.routingProfileId)
        : state.routingProfiles.find((profile) => profile.isDefault);
      if (!nextProfile) return error(400, 'invalid_routing_profile', 'Routing profile was not found.');
      const previous = state.routingProfiles.find((profile) => profile.id === item.routingProfileId);
      if (previous && previous.id !== nextProfile.id) previous.downstreamKeyCount = Math.max(0, previous.downstreamKeyCount - 1);
      if (previous?.id !== nextProfile.id) nextProfile.downstreamKeyCount += 1;
      item.routingProfileId = nextProfile.id;
    }
    item.revision += 1;
    item.updatedAt = now;
    refreshKeyProjection(state);
    return json({ item: downstreamKeyResponse(item) }, 200, item.revision);
  }

  const revealMatch = /^\/api\/vnext\/downstream-keys\/(\d+)\/reveal$/.exec(path);
  if (method === 'POST' && revealMatch) {
    const id = Number(revealMatch[1]);
    const item = state.downstreamKeys.find((key) => key.id === id);
    if (!item) return error(404, 'not_found', 'Downstream key was not found.');
    const secret = state.downstreamSecrets[id];
    return secret && item.revealable ? json({ secret }) : error(409, 'key_not_revealable', 'This key cannot be revealed.');
  }

  const rotateMatch = /^\/api\/vnext\/downstream-keys\/(\d+)\/rotate$/.exec(path);
  if (method === 'POST' && rotateMatch) {
    const id = Number(rotateMatch[1]);
    const item = state.downstreamKeys.find((key) => key.id === id);
    if (!item) return error(404, 'not_found', 'Downstream key was not found.');
    const conflict = revisionError(request, item.revision);
    if (conflict) return conflict;
    const secret = `js_prototype_${id}_${(now + item.revision).toString(36)}`;
    state.downstreamSecrets[id] = secret;
    item.keyPrefix = secret.slice(0, 14);
    item.revision += 1;
    item.updatedAt = now;
    return json({ item: downstreamKeyResponse(item), secret }, 200, item.revision);
  }

  if (method === 'POST' && path === '/api/vnext/routing-profiles') {
    const precondition = createRevisionError(request);
    if (precondition) return precondition;
    const body = await requestBody<{ name?: string }>(request);
    if (body instanceof Response) return body;
    const name = body.name?.trim();
    if (!name) return error(400, 'invalid_request', 'Routing profile name is required.');
    if (state.routingProfiles.some((item) => item.name.toLowerCase() === name.toLowerCase())) return error(409, 'name_conflict', 'Routing profile name already exists.');
    const id = nextID(state, 'profile');
    const defaultProfile = state.routingProfiles.find((item) => item.isDefault)!;
    const inherited = state.routes.filter((item) => item.routingProfileId === defaultProfile.id).map((item) => clonedInheritedRoute(item, id, name, now));
    const item = { id, name, isDefault: false, revision: 1, modelCount: inherited.length, localModelCount: 0, inheritedModelCount: inherited.length, downstreamKeyCount: 0, createdAt: now, updatedAt: now };
    state.routingProfiles.push(item);
    state.routes.push(...inherited);
    return json({ item: clone(item) }, 201, item.revision);
  }

  const profileMatch = /^\/api\/vnext\/routing-profiles\/(\d+)$/.exec(path);
  if (method === 'PATCH' && profileMatch) {
    const item = state.routingProfiles.find((profile) => profile.id === Number(profileMatch[1]));
    if (!item) return error(404, 'not_found', 'Routing profile was not found.');
    const conflict = revisionError(request, item.revision);
    if (conflict) return conflict;
    const body = await requestBody<{ name?: string }>(request);
    if (body instanceof Response) return body;
    if (body.name !== undefined) {
      const name = body.name.trim();
      if (!name) return error(400, 'invalid_request', 'Routing profile name cannot be empty.');
      item.name = name;
      for (const route of state.routes.filter((candidate) => candidate.routingProfileId === item.id)) route.routingProfileName = name;
    }
    item.revision += 1;
    item.updatedAt = now;
    refreshKeyProjection(state, item.id);
    return json({ item: clone(item) }, 200, item.revision);
  }

  if (method === 'DELETE' && profileMatch) {
    const item = state.routingProfiles.find((profile) => profile.id === Number(profileMatch[1]));
    if (!item) return error(404, 'not_found', 'Routing profile was not found.');
    const conflict = revisionError(request, item.revision);
    if (conflict) return conflict;
    if (item.isDefault) return error(409, 'default_profile_required', 'The default routing profile cannot be deleted.');
    if (state.downstreamKeys.some((key) => key.routingProfileId === item.id)) return error(409, 'profile_in_use', 'Routing profile is assigned to downstream keys.');
    state.routingProfiles = state.routingProfiles.filter((profile) => profile.id !== item.id);
    state.routes = state.routes.filter((route) => route.routingProfileId !== item.id);
    return noContent();
  }

  const routesMatch = /^\/api\/vnext\/routing-profiles\/(\d+)\/routes$/.exec(path);
  if (method === 'POST' && routesMatch) {
    const profile = state.routingProfiles.find((item) => item.id === Number(routesMatch[1]));
    if (!profile) return error(404, 'not_found', 'Routing profile was not found.');
    const conflict = revisionError(request, profile.revision);
    if (conflict) return conflict;
    const body = await requestBody<{ publicName?: string; officialPriceSku?: string; enabled?: boolean; providerTargetIds?: number[]; publishedModelId?: number }>(request);
    if (body instanceof Response) return body;
    let item: ModelRoute;
    if (profile.isDefault) {
      if (!body.publicName?.trim() || !body.officialPriceSku?.trim() || !body.providerTargetIds?.length) return error(400, 'invalid_request', 'Published model name, price SKU, and ordered targets are required.');
      if (state.routes.some((route) => route.routingProfileId === profile.id && route.publicName === body.publicName?.trim())) return error(409, 'name_conflict', 'Published model name already exists.');
      const publishedModelId = nextID(state, 'publishedModel');
      const targets = makeRouteTargets(state, publishedModelId, body.providerTargetIds, now);
      if (targets instanceof Response) return targets;
      item = { routingProfileId: profile.id, routingProfileName: profile.name, sourceProfileId: profile.id, sourceProfileName: profile.name, inherited: false, targetsOverridden: true, publishedModelId, publicName: body.publicName.trim(), officialPriceSku: body.officialPriceSku.trim(), enabled: body.enabled ?? true, publishedModelRevision: 1, revision: 1, targets, createdAt: now, updatedAt: now };
      state.routes.push(item);
      for (const custom of state.routingProfiles.filter((candidate) => !candidate.isDefault)) {
        state.routes.push(clonedInheritedRoute(item, custom.id, custom.name, now));
        custom.modelCount += 1;
        custom.inheritedModelCount += 1;
      }
    } else {
      const publishedModelId = body.publishedModelId || 0;
      const source = state.routes.find((route) => route.routingProfileId === 1 && route.publishedModelId === publishedModelId);
      if (!source) return error(400, 'invalid_published_model', 'Published model was not found in the default profile.');
      const existingIndex = state.routes.findIndex((route) => route.routingProfileId === profile.id && route.publishedModelId === publishedModelId);
      const wasInherited = existingIndex >= 0 && state.routes[existingIndex].inherited;
      const targets = body.providerTargetIds
        ? makeRouteTargets(state, publishedModelId, body.providerTargetIds, now)
        : clone(source.targets);
      if (targets instanceof Response) return targets;
      item = { ...clone(source), routingProfileId: profile.id, routingProfileName: profile.name, sourceProfileId: profile.id, sourceProfileName: profile.name, inherited: false, targetsOverridden: body.providerTargetIds !== undefined, enabled: body.enabled ?? source.enabled, revision: 1, targets, createdAt: existingIndex >= 0 ? state.routes[existingIndex].createdAt : now, updatedAt: now };
      if (existingIndex >= 0) state.routes[existingIndex] = item;
      else state.routes.push(item);
      if (existingIndex < 0 || wasInherited) profile.localModelCount += 1;
      profile.inheritedModelCount = Math.max(0, profile.modelCount - profile.localModelCount);
    }
    profile.revision += 1;
    profile.modelCount = state.routes.filter((route) => route.routingProfileId === profile.id).length;
    profile.updatedAt = now;
    refreshKeyProjection(state, profile.id);
    return json({ item: clone(item) }, 201, item.revision);
  }

  const routeMatch = /^\/api\/vnext\/routing-profiles\/(\d+)\/routes\/(\d+)$/.exec(path);
  if ((method === 'PATCH' || method === 'DELETE') && routeMatch) {
    const profileId = Number(routeMatch[1]);
    const publishedModelId = Number(routeMatch[2]);
    const profile = state.routingProfiles.find((item) => item.id === profileId);
    const item = state.routes.find((route) => route.routingProfileId === profileId && route.publishedModelId === publishedModelId);
    if (!profile || !item) return error(404, 'not_found', 'Published model route was not found.');
    const conflict = revisionError(request, item.revision);
    if (conflict) return conflict;
    if (method === 'DELETE') {
      if (profile.isDefault) {
        state.routes = state.routes.filter((route) => route.publishedModelId !== publishedModelId);
        state.monitorSnapshot.items = state.monitorSnapshot.items.filter((model) => model.publishedModelId !== publishedModelId);
        for (const custom of state.routingProfiles.filter((candidate) => !candidate.isDefault)) {
          custom.modelCount = state.routes.filter((route) => route.routingProfileId === custom.id).length;
          custom.localModelCount = state.routes.filter((route) => route.routingProfileId === custom.id && !route.inherited).length;
          custom.inheritedModelCount = custom.modelCount - custom.localModelCount;
          custom.revision += 1;
          custom.updatedAt = now;
        }
      } else {
        const source = state.routes.find((route) => route.routingProfileId === 1 && route.publishedModelId === publishedModelId);
        if (!source) return error(404, 'not_found', 'Default published model was not found.');
        const index = state.routes.indexOf(item);
        state.routes[index] = clonedInheritedRoute(source, profile.id, profile.name, now);
      }
      profile.revision += 1;
      profile.localModelCount = state.routes.filter((route) => route.routingProfileId === profile.id && !route.inherited).length;
      profile.modelCount = state.routes.filter((route) => route.routingProfileId === profile.id).length;
      profile.inheritedModelCount = profile.modelCount - profile.localModelCount;
      refreshKeyProjection(state);
      return noContent();
    }
    const body = await requestBody<{ publicName?: string; officialPriceSku?: string; enabled?: boolean }>(request);
    if (body instanceof Response) return body;
    if (profile.isDefault) {
      const publicName = body.publicName?.trim();
      const officialPriceSku = body.officialPriceSku?.trim();
      for (const route of state.routes.filter((candidate) => candidate.publishedModelId === publishedModelId)) {
        if (publicName) route.publicName = publicName;
        if (officialPriceSku) route.officialPriceSku = officialPriceSku;
        if (body.enabled !== undefined && (route.routingProfileId === profile.id || route.inherited)) route.enabled = body.enabled;
        route.publishedModelRevision += 1;
        route.revision += 1;
        route.updatedAt = now;
      }
      const defaultRoute = state.routes.find((route) => route.routingProfileId === 1 && route.publishedModelId === publishedModelId);
      if (defaultRoute) syncMonitorRoute(state, defaultRoute);
    } else {
      if (body.enabled !== undefined) item.enabled = body.enabled;
      item.inherited = false;
      item.sourceProfileId = profile.id;
      item.sourceProfileName = profile.name;
      item.revision += 1;
      item.updatedAt = now;
    }
    refreshKeyProjection(state);
    return json({ item: clone(state.routes.find((route) => route.routingProfileId === profileId && route.publishedModelId === publishedModelId)!) }, 200, item.revision);
  }

  const targetsMatch = /^\/api\/vnext\/routing-profiles\/(\d+)\/routes\/(\d+)\/targets$/.exec(path);
  if (method === 'PUT' && targetsMatch) {
    const profileId = Number(targetsMatch[1]);
    const publishedModelId = Number(targetsMatch[2]);
    const item = state.routes.find((route) => route.routingProfileId === profileId && route.publishedModelId === publishedModelId);
    if (!item) return error(404, 'not_found', 'Published model route was not found.');
    const conflict = revisionError(request, item.revision);
    if (conflict) return conflict;
    const body = await requestBody<{ providerTargetIds?: number[] }>(request);
    if (body instanceof Response) return body;
    if (!body.providerTargetIds?.length) return error(400, 'invalid_request', 'At least one ordered provider target is required.');
    const targets = makeRouteTargets(state, publishedModelId, body.providerTargetIds, now);
    if (targets instanceof Response) return targets;
    item.targets = targets;
    item.targetsOverridden = true;
    item.inherited = false;
    item.sourceProfileId = profileId;
    item.sourceProfileName = item.routingProfileName;
    item.revision += 1;
    item.updatedAt = now;
    if (profileId === 1) {
      for (const inherited of state.routes.filter((route) => route.publishedModelId === publishedModelId && route.routingProfileId !== 1 && route.inherited)) {
        inherited.targets = clone(targets);
        inherited.revision += 1;
        inherited.updatedAt = now;
      }
      syncMonitorRoute(state, item);
    }
    return json({ item: clone(item) }, 200, item.revision);
  }

  const accountMatch = /^\/api\/vnext\/site-accounts\/sites\/(\d+)$/.exec(path);
  if (method === 'PUT' && accountMatch) {
    const siteId = Number(accountMatch[1]);
    const site = state.sites.find((item) => item.id === siteId);
    if (!site) return error(404, 'not_found', 'Site was not found.');
    const body = await requestBody<{ adapterKind?: string; origin?: string; enabled?: boolean; secrets?: Record<string, unknown> }>(request);
    if (body instanceof Response) return body;
    if (!body.adapterKind?.trim() || !body.origin?.trim()) return error(400, 'invalid_request', 'Adapter kind and origin are required.');
    const existing = state.siteAccounts[siteId];
    const item = {
      id: existing?.id || siteId, siteId, siteName: site.name, adapterKind: body.adapterKind.trim(),
      origin: body.origin.trim().replace(/\/$/, ''), secretConfigured: Boolean(body.secrets && Object.values(body.secrets).some(Boolean)),
      enabled: body.enabled ?? true, capabilities: { sessionRefresh: true, balance: true, usage: true }, adapterAvailable: true,
      lastSessionRefreshAt: now, lastBalanceRefreshAt: existing?.lastBalanceRefreshAt || null,
      lastUsageRefreshAt: existing?.lastUsageRefreshAt || null, lastErrorOperation: '', lastErrorCode: '', lastErrorAt: null,
      latestBalance: existing?.latestBalance || null, revision: (existing?.revision || 0) + 1,
      createdAt: existing?.createdAt || now, updatedAt: now,
    };
    state.siteAccounts[siteId] = item;
    return json(clone(item), existing ? 200 : 201, item.revision);
  }

  if ((method === 'PATCH' || method === 'DELETE') && accountMatch) {
    const siteId = Number(accountMatch[1]);
    const item = state.siteAccounts[siteId];
    if (!item) return error(404, 'not_found', 'Site account is not configured.');
    const conflict = revisionError(request, item.revision);
    if (conflict) return conflict;
    if (method === 'DELETE') {
      delete state.siteAccounts[siteId];
      return noContent();
    }
    const body = await requestBody<{ adapterKind?: string; origin?: string; enabled?: boolean }>(request);
    if (body instanceof Response) return body;
    if (body.adapterKind !== undefined) item.adapterKind = body.adapterKind.trim();
    if (body.origin !== undefined) item.origin = body.origin.trim().replace(/\/$/, '');
    if (body.enabled !== undefined) item.enabled = body.enabled;
    item.revision += 1;
    item.updatedAt = now;
    return json(clone(item), 200, item.revision);
  }

  const accountSecretMatch = /^\/api\/vnext\/site-accounts\/sites\/(\d+)\/secret$/.exec(path);
  if (method === 'PUT' && accountSecretMatch) {
    const item = state.siteAccounts[Number(accountSecretMatch[1])];
    if (!item) return error(404, 'not_found', 'Site account is not configured.');
    const conflict = revisionError(request, item.revision);
    if (conflict) return conflict;
    const body = await requestBody<Record<string, unknown>>(request);
    if (body instanceof Response) return body;
    if (!Object.values(body).some(Boolean)) return error(400, 'invalid_request', 'At least one account secret is required.');
    item.secretConfigured = true;
    item.lastSessionRefreshAt = now;
    item.revision += 1;
    item.updatedAt = now;
    return json(clone(item), 200, item.revision);
  }

  const balanceMatch = /^\/api\/vnext\/site-accounts\/sites\/(\d+)\/balance\/refresh$/.exec(path);
  if (method === 'POST' && balanceMatch) {
    const siteId = Number(balanceMatch[1]);
    const item = state.siteAccounts[siteId];
    if (!item) return error(404, 'not_found', 'Site account is not configured.');
    const previous = item.latestBalance;
    const balance = {
      id: nextID(state, 'balance'), accountRemoteId: previous?.accountRemoteId || `remote-${siteId}`,
      accountName: previous?.accountName || 'JieShan', availableValue: previous?.availableValue || '100.00',
      availableUnit: previous?.availableUnit || 'USD', usedValue: previous?.usedValue || '0.00',
      usedUnit: previous?.usedUnit || 'USD', capturedAt: now,
    };
    item.latestBalance = balance;
    item.lastBalanceRefreshAt = now;
    item.lastErrorOperation = '';
    item.lastErrorCode = '';
    item.lastErrorAt = null;
    item.updatedAt = now;
    return json({ balance: clone(balance) });
  }

  const usageSyncMatch = /^\/api\/vnext\/site-accounts\/sites\/(\d+)\/usage\/sync$/.exec(path);
  if (method === 'POST' && usageSyncMatch) {
    const siteId = Number(usageSyncMatch[1]);
    const account = state.siteAccounts[siteId];
    if (!account) return error(404, 'not_found', 'Site account is not configured.');
    const body = await requestBody<Record<string, unknown>>(request);
    if (body instanceof Response) return body;
    const list = state.siteUsage[siteId] || [];
    list.unshift({
      id: nextID(state, 'usage'), remoteId: `sync-${now}`, requestId: `req_sync_${now}`, upstreamRequestId: `up_sync_${now}`,
      occurredAt: now - 5_000, model: 'gpt-5.2', upstreamModel: 'gpt-5.2', status: 'success', httpStatus: 200,
      inputTokens: 960, outputTokens: 280, cacheReadTokens: 0, cacheWriteTokens: 0, reasoningTokens: 64,
      totalTokens: 1_240, chargeValue: '0.006400', chargeUnit: 'USD', durationMs: 860,
      apiKeyName: 'primary', sourceFetchedAt: now,
    });
    state.siteUsage[siteId] = list;
    account.lastUsageRefreshAt = now;
    account.updatedAt = now;
    return noContent();
  }

  if (method === 'POST' && path === '/api/vnext/pricing/catalogs/preview') {
    const body = await requestBody<{ catalog?: PriceCatalog }>(request);
    if (body instanceof Response) return body;
    if (!body.catalog?.version || !Array.isArray(body.catalog.entries)) return error(400, 'invalid_catalog', 'Catalog version and entries are required.');
    const candidate = clone(body.catalog);
    candidate.digest ||= `sha256:prototype-${candidate.version}-${candidate.entries.length}`;
    const active = state.priceCatalogs.find((item) => item.version === state.catalogState.active_version);
    const activeSkus = new Set(active?.entries.map((item) => item.sku) || []);
    const candidateSkus = new Set(candidate.entries.map((item) => item.sku));
    const added = [...candidateSkus].filter((sku) => !activeSkus.has(sku));
    const removed = [...activeSkus].filter((sku) => !candidateSkus.has(sku));
    const changed = candidate.entries.filter((entry) => activeSkus.has(entry.sku) && JSON.stringify(active?.entries.find((item) => item.sku === entry.sku)) !== JSON.stringify(entry)).map((entry) => entry.sku);
    return json({
      candidate,
      state: clone(state.catalogState),
      diff: {
        active_version: state.catalogState.active_version,
        active_digest: active?.digest,
        candidate_version: candidate.version,
        candidate_digest: candidate.digest,
        summary: { added_entries: added.length, removed_entries: removed.length, changed_entries: changed.length, unchanged_entries: Math.max(0, candidate.entries.length - added.length - changed.length) },
        entries: [
          ...added.map((sku) => ({ sku, kind: 'added' as const })),
          ...removed.map((sku) => ({ sku, kind: 'removed' as const })),
          ...changed.map((sku) => ({ sku, kind: 'changed' as const, metadata_changed: true })),
        ],
      },
      can_activate: new Date(candidate.effective_at).getTime() <= now,
    }, 200, state.catalogState.revision);
  }

  if (method === 'POST' && path === '/api/vnext/pricing/catalogs') {
    const body = await requestBody<{ catalog?: PriceCatalog; expected_digest?: string }>(request);
    if (body instanceof Response) return body;
    if (!body.catalog?.version) return error(400, 'invalid_catalog', 'Catalog version is required.');
    const candidate = clone(body.catalog);
    candidate.digest ||= `sha256:prototype-${candidate.version}-${candidate.entries.length}`;
    if (body.expected_digest !== candidate.digest) return error(409, 'digest_confirmation_failed', 'Expected digest does not match the candidate catalog.');
    const existing = state.priceCatalogs.find((item) => item.version === candidate.version);
    if (existing && existing.digest !== candidate.digest) return error(409, 'immutable_version_conflict', 'Catalog versions are immutable.');
    const imported = !existing;
    candidate.imported_at ||= new Date(now).toISOString();
    if (imported) state.priceCatalogs.push(candidate);
    return json({ catalog: clone(existing || candidate), imported, state: clone(state.catalogState) }, imported ? 201 : 200, state.catalogState.revision);
  }

  const activateMatch = /^\/api\/vnext\/pricing\/catalogs\/([^/]+)\/activate$/.exec(path);
  if (method === 'POST' && activateMatch) {
    const conflict = revisionError(request, state.catalogState.revision);
    if (conflict) return conflict;
    const version = decodeURIComponent(activateMatch[1]);
    const candidate = state.priceCatalogs.find((item) => item.version === version);
    if (!candidate) return error(404, 'catalog_not_found', 'Price catalog was not found.');
    if (new Date(candidate.effective_at).getTime() > now) return error(409, 'catalog_not_effective', 'Price catalog is not effective yet.');
    state.catalogState.active_version = version;
    state.catalogState.revision += 1;
    state.catalogState.updated_at = new Date(now).toISOString();
    return json({ state: clone(state.catalogState) }, 200, state.catalogState.revision);
  }

  const monitorModelMatch = /^\/api\/vnext\/monitor\/models\/(\d+)$/.exec(path);
  if (method === 'POST' && monitorModelMatch) {
    const publishedModelId = Number(monitorModelMatch[1]);
    if (state.monitorSnapshot.items.some((item) => item.publishedModelId === publishedModelId)) return error(409, 'already_exists', 'Monitor configuration already exists.');
    const route = state.routes.find((item) => item.routingProfileId === 1 && item.publishedModelId === publishedModelId);
    if (!route) return error(404, 'not_found', 'Published model was not found.');
    const body = await requestBody<{ enabled?: boolean; historyLimit?: number }>(request);
    if (body instanceof Response) return body;
    const monitor = { enabled: body.enabled ?? true, intervalMs: state.settings.probeIntervalMs, historyLimit: body.historyLimit || 120, nextProbeAt: now + state.settings.probeIntervalMs, lastProbeStartedAt: null, lastProbeFinishedAt: null, busy: false, revision: 1, createdAt: now, updatedAt: now };
    const targets = route.targets.map((target) => {
      const provider = state.modelTargets.find((item) => item.id === target.providerModelTargetId);
      const health = state.routingHealth[target.providerModelTargetId] || null;
      const item = { publishedModelTargetId: target.id, publishedModelTargetRevision: target.revision, providerModelTargetId: target.providerModelTargetId, providerModelTargetRevision: provider?.revision || 1, position: target.position, siteId: target.siteId, siteName: target.siteName, endpointId: target.endpointId, endpointName: target.endpointName, sourceModel: target.sourceModel, wireProtocol: target.wireProtocol, apiSurface: target.apiSurface, enabled: provider?.enabled ?? true, usableCredentialCount: provider?.usableCredentialCount || 0, status: 'unprobed' as const, successes: 0, failures: 0, skipped: 0, successBasisPoints: 0, latest: null, statusBar: [], health };
      state.monitorHistories[`${publishedModelId}:${target.providerModelTargetId}`] = { publishedModelId, publicModel: route.publicName, target: { publishedModelTargetId: target.id, providerModelTargetId: target.providerModelTargetId, position: target.position, siteId: target.siteId, siteName: target.siteName, endpointId: target.endpointId, endpointName: target.endpointName, sourceModel: target.sourceModel, wireProtocol: target.wireProtocol, apiSurface: target.apiSurface }, status: 'unprobed', successes: 0, failures: 0, skipped: 0, total: 0, attempted: 0, successBasisPoints: 0, health: clone(item.health), order: 'oldest_first', items: [] };
      return item;
    });
    state.monitorSnapshot.items.push({ publishedModelId, publicModel: route.publicName, officialPriceSku: route.officialPriceSku, publishedModelEnabled: route.enabled, publishedModelRevision: route.publishedModelRevision, status: monitor.enabled ? 'unprobed' : 'disabled', monitor, targets, successes: 0, failures: 0, skipped: 0, successBasisPoints: 0 });
    return json({ item: clone(monitor) }, 201, monitor.revision);
  }

  if (method === 'PATCH' && monitorModelMatch) {
    const model = state.monitorSnapshot.items.find((item) => item.publishedModelId === Number(monitorModelMatch[1]));
    if (!model) return error(404, 'not_found', 'Monitor configuration was not found.');
    const conflict = revisionError(request, model.monitor.revision);
    if (conflict) return conflict;
    const body = await requestBody<{ enabled?: boolean; historyLimit?: number }>(request);
    if (body instanceof Response) return body;
    if (body.enabled !== undefined) model.monitor.enabled = body.enabled;
    if (body.historyLimit !== undefined) {
      model.monitor.historyLimit = Math.max(10, body.historyLimit);
      for (const target of model.targets) {
        target.statusBar = target.statusBar.slice(-model.monitor.historyLimit);
        const history = state.monitorHistories[`${model.publishedModelId}:${target.providerModelTargetId}`];
        if (history) {
          history.items = history.items.slice(-model.monitor.historyLimit);
          history.total = history.items.length;
        }
      }
    }
    model.monitor.revision += 1;
    model.monitor.updatedAt = now;
    model.monitor.nextProbeAt = now + model.monitor.intervalMs;
    recomputeMonitorModel(model);
    return json({ item: clone(model.monitor) }, 200, model.monitor.revision);
  }

  const probeMatch = /^\/api\/vnext\/monitor\/models\/(\d+)\/probe$/.exec(path);
  if (method === 'POST' && probeMatch) {
    const model = state.monitorSnapshot.items.find((item) => item.publishedModelId === Number(probeMatch[1]));
    if (!model) return error(404, 'not_found', 'Monitor configuration was not found.');
    model.monitor.busy = true;
    model.monitor.lastProbeStartedAt = now;
    for (const target of model.targets) {
      const coolingUntil = target.health?.cooldownUntil || 0;
      if (coolingUntil > now) appendProbe(state, target.providerModelTargetId, 'skipped', { permitReason: 'cooldown_active' });
      else {
        updateRuntimeHealth(state, target.providerModelTargetId, true);
        appendProbe(state, target.providerModelTargetId, 'success');
      }
    }
    model.monitor.busy = false;
    model.monitor.lastProbeFinishedAt = Date.now();
    model.monitor.nextProbeAt = Date.now() + model.monitor.intervalMs;
    model.monitor.updatedAt = Date.now();
    return noContent();
  }

  if (method === 'PATCH' && path === '/api/vnext/settings') {
    const conflict = revisionError(request, state.settings.revision);
    if (conflict) return conflict;
    const body = await requestBody<Omit<PrototypeState['settings'], 'revision'>>(request);
    if (body instanceof Response) return body;
    if (Object.values(body).some((value) => typeof value !== 'number' || value <= 0)) return error(400, 'invalid_request', 'Every runtime setting must be a positive number.');
    state.settings = { ...body, revision: state.settings.revision + 1 };
    for (const model of state.monitorSnapshot.items) {
      model.monitor.intervalMs = state.settings.probeIntervalMs;
      model.monitor.nextProbeAt = now + state.settings.probeIntervalMs;
    }
    return json(clone(state.settings), 200, state.settings.revision);
  }

  return null;
}

function boundCredentials(state: PrototypeState, target: RouteTarget) {
  const ids = state.endpointCredentialIds[String(target.endpointId)] || [];
  return ids.flatMap((id) => {
    const item = state.credentials.find((credential) => credential.id === id);
    return item ? [item] : [];
  });
}

function routeCredential(state: PrototypeState, target: RouteTarget) {
  const now = Date.now();
  for (const credential of boundCredentials(state, target)) {
    if (credential.coolingUntil && credential.coolingUntil <= now) {
      credential.coolingUntil = null;
      credential.runtimeState = 'ready';
    }
    if (credential.enabled && credential.runtimeState !== 'invalid' && credential.runtimeState !== 'exhausted' && !credential.coolingUntil) return credential;
  }
  return undefined;
}

function recoverExpiredCooldown(state: PrototypeState, providerTargetId: number): void {
  const health = state.routingHealth[providerTargetId];
  if (!health?.cooldownUntil || health.cooldownUntil > Date.now()) return;
  health.phase = 'recovering';
  health.cooldownUntil = null;
  health.updatedAt = Date.now();
  for (const model of state.monitorSnapshot.items) {
    const target = model.targets.find((item) => item.providerModelTargetId === providerTargetId);
    if (!target) continue;
    target.health = clone(health);
    target.status = 'recovering';
    recomputeMonitorModel(model);
  }
}

function officialCost(state: PrototypeState, sku: string, inputTokens: number, outputTokens: number, cacheReadTokens: number): number {
  const catalog = state.priceCatalogs.find((item) => item.version === state.catalogState.active_version);
  const entry = catalog?.entries.find((item) => item.sku === sku);
  if (!entry) return 0;
  const rate = (tokenClass: string) => entry.rates.find((item) => item.class === tokenClass)?.nano_usd_per_million || 0;
  return Math.round((inputTokens * rate('input') + outputTokens * rate('output') + cacheReadTokens * rate('cache_read')) / 1_000_000);
}

function dataPlaneError(status: number, code: string, message: string, requestId?: string): Response {
  return json(
    { error: { code, message, type: code } },
    status,
    undefined,
    requestId ? { 'X-JieShan-Request-Id': requestId } : undefined,
  );
}

async function handleDataPlane(request: Request, path: string, state: PrototypeState): Promise<Response | null> {
  if (!['/v1/chat/completions', '/v1/responses', '/v1/messages'].includes(path)) return null;
  if (request.method.toUpperCase() !== 'POST') return dataPlaneError(405, 'method_not_allowed', 'Method is not allowed.');
  const secret = downstreamSecret(request, state);
  if (!secret) return dataPlaneError(401, 'invalid_api_key', 'A single valid JieShan API key is required.');
  const secretEntry = Object.entries(state.downstreamSecrets).find(([, value]) => value === secret);
  const key = secretEntry ? state.downstreamKeys.find((item) => item.id === Number(secretEntry[0])) : undefined;
  if (!key || !key.enabled) return dataPlaneError(401, 'invalid_api_key', 'A single valid JieShan API key is required.');
  const now = Date.now();
  if (now - key.hourlyWindowStartedAt >= 60 * MINUTE) {
    key.hourlyWindowStartedAt = now;
    key.usedThisHourNanoUSD = 0;
  }
  if (key.expires !== null && key.expires <= now) return dataPlaneError(401, 'api_key_expired', 'The downstream API key has expired.');
  if (key.quotaNanoUSD !== null && key.usedNanoUSD + key.reservedNanoUSD >= key.quotaNanoUSD) {
    return dataPlaneError(429, 'quota_exhausted', 'The downstream API key quota is exhausted.');
  }
  if (key.hourlyQuotaNanoUSD !== null && key.usedThisHourNanoUSD >= key.hourlyQuotaNanoUSD) {
    return dataPlaneError(429, 'hourly_quota_exhausted', 'The downstream API key hourly spending limit is exhausted.');
  }
  const body = await requestBody<Record<string, unknown>>(request);
  if (body instanceof Response) return body;
  const publicModel = typeof body.model === 'string' ? body.model.trim() : '';
  if (!publicModel) return dataPlaneError(400, 'invalid_request', 'Request must contain a model.');
  const expectedSurface: InferenceSurface = path === '/v1/messages'
    ? 'anthropic.messages'
    : path === '/v1/responses'
      ? 'openai.responses'
      : 'openai.chat_completions';
  const route = state.routes.find((item) => item.routingProfileId === key.routingProfileId && item.publicName === publicModel && item.enabled);
  if (!route || !key.models.includes(publicModel)) return dataPlaneError(404, 'model_not_found', `Model ${publicModel} is not available for this key.`);
  const orderedTargets = [...route.targets].sort((left, right) => left.position - right.position);
  if (!orderedTargets.some((target) => target.apiSurface === expectedSurface)) {
    return dataPlaneError(400, 'surface_not_supported', `Model ${publicModel} is not published on this API surface.`);
  }

  const requestId = request.headers.get('X-Request-Id')?.trim() || `req_proto_${now.toString(36)}_${state.requestLogs.length + 1}`;
  const reservationNanoUsd = 100_000_000;
  const attempts: PrototypeRequestLog['attempts'] = [];
  const candidates: PrototypeRequestLog['routeCandidates'] = [];
  let succeededTarget: RouteTarget | null = null;
  let attemptIndex = 0;

  for (const target of orderedTargets) {
    recoverExpiredCooldown(state, target.providerModelTargetId);
    const modelTarget = state.modelTargets.find((item) => item.id === target.providerModelTargetId);
    const credentials = boundCredentials(state, target);
    const credential = routeCredential(state, target);
    const coolingUntil = state.routingHealth[target.providerModelTargetId]?.cooldownUntil || null;
    const eligible = Boolean(
      target.apiSurface === expectedSurface
      && modelTarget?.routable
      && credential
      && (!coolingUntil || coolingUntil <= now),
    );
    const candidate: PrototypeRequestLog['routeCandidates'][number] = {
      position: target.position,
      publishedModelTargetId: target.id,
      publishedModelTargetRevision: target.revision,
      providerModelTargetId: target.providerModelTargetId,
      providerModelTargetRevision: modelTarget?.revision || 1,
      siteId: target.siteId,
      siteName: target.siteName,
      endpointId: target.endpointId,
      endpointName: target.endpointName,
      sourceModel: target.sourceModel,
      wireProtocol: target.wireProtocol,
      apiSurface: target.apiSurface,
      credentials: credentials.map((item, position) => ({ id: item.id, name: item.name, position, runtimeState: item.runtimeState, coolingUntil: item.coolingUntil })),
      initialEligibility: eligible ? 'eligible' : 'skipped',
      initialReason: eligible ? 'ready' : coolingUntil && coolingUntil > now ? 'cooling' : target.apiSurface !== expectedSurface ? 'unsupported' : credential ? 'target_disabled' : 'no_eligible_credentials',
      disposition: 'not_attempted',
      dispositionReason: '',
      attemptCount: 0,
      firstAttemptIndex: null,
      lastAttemptIndex: null,
    };
    candidates.push(candidate);
    if (succeededTarget) {
      candidate.disposition = eligible ? 'not_attempted' : 'skipped';
      candidate.dispositionReason = eligible ? 'request_succeeded' : candidate.initialReason;
      continue;
    }
    if (!eligible) {
      candidate.disposition = 'skipped';
      candidate.dispositionReason = candidate.initialReason;
      continue;
    }
    if (attemptIndex >= state.settings.maxAttempts) {
      candidate.disposition = 'not_attempted';
      candidate.dispositionReason = 'candidates_exhausted';
      continue;
    }

    const plannedFailureAt = state.failNextTargetIds.indexOf(target.providerModelTargetId);
    const plannedFailure = plannedFailureAt >= 0;
    if (plannedFailure) state.failNextTargetIds.splice(plannedFailureAt, 1);
    const failed = plannedFailure;
    const startedAt = Date.now();
    const durationMs = failed ? 680 : 1_160;
    const attempt = {
      id: nextID(state, 'attempt'), attemptIndex, publishedModelTargetId: target.id,
      publishedModelTargetRevision: target.revision, providerModelTargetId: target.providerModelTargetId,
      providerModelTargetRevision: modelTarget?.revision || 1, siteId: target.siteId, siteName: target.siteName,
      endpointId: target.endpointId, endpointName: target.endpointName, credentialId: credential!.id,
      credentialName: credential!.name, sourceModel: target.sourceModel, responseModel: failed ? '' : target.sourceModel,
      wireProtocol: target.wireProtocol, apiSurface: target.apiSurface, status: failed ? 'failed' : 'success',
      httpStatus: failed ? 503 : 200, failureKind: failed ? 'upstream_transient' : '',
      errorCode: failed ? 'upstream_503' : '', switchReason: failed ? 'next_target' : '',
      firstOutputMs: failed ? null : 310, durationMs, startedAt, finishedAt: startedAt + durationMs,
    } satisfies PrototypeRequestLog['attempts'][number];
    attempts.push(attempt);
    candidate.disposition = 'attempted';
    candidate.dispositionReason = failed ? 'next_target' : 'success';
    candidate.attemptCount = 1;
    candidate.firstAttemptIndex = attemptIndex;
    candidate.lastAttemptIndex = attemptIndex;
    attemptIndex += 1;
    updateRuntimeHealth(state, target.providerModelTargetId, !failed, failed ? 'upstream_transient' : '', credential!.id);
    if (!failed) {
      succeededTarget = target;
    }
  }

  const finishedAt = Date.now() + (attempts.reduce((sum, item) => sum + item.durationMs, 0) || 80);
  const stream = body.stream === true;
  const rawBodyLength = JSON.stringify(body).length;
  const inputTokens = Math.max(32, Math.ceil(rawBodyLength / 4));
  const outputTokens = succeededTarget ? 96 : 0;
  const cacheReadTokens = typeof body.prompt_cache_key === 'string' ? Math.min(inputTokens, 64) : 0;
  const official = succeededTarget ? officialCost(state, route.officialPriceSku, inputTokens, outputTokens, cacheReadTokens) : 0;
  const chargeBeforeQuota = Math.round(official * key.billingMultiplier);
  const totalRemaining = key.quotaNanoUSD === null ? Number.MAX_SAFE_INTEGER : Math.max(0, key.quotaNanoUSD - key.usedNanoUSD);
  const hourlyRemaining = key.hourlyQuotaNanoUSD === null ? Number.MAX_SAFE_INTEGER : Math.max(0, key.hourlyQuotaNanoUSD - key.usedThisHourNanoUSD);
  const remaining = Math.min(totalRemaining, hourlyRemaining);
  const chargedNanoUsd = Math.min(chargeBeforeQuota, remaining);
  const quotaCapped = chargedNanoUsd < chargeBeforeQuota;
  const finalAttempt = attempts[attempts.length - 1] || null;
  const log: PrototypeRequestLog = {
    id: requestId,
    downstreamKeyId: key.id,
    downstreamKeyName: key.name,
    publishedModelId: route.publishedModelId,
    publishedModelRevision: route.publishedModelRevision,
    effectiveRoutingProfileId: key.routingProfileId,
    effectiveRoutingProfileName: key.routingProfileName,
    sourceRoutingProfileId: route.sourceProfileId,
    sourceRoutingProfileName: route.sourceProfileName,
    routeRevision: route.revision,
    publicModel,
    apiSurface: expectedSurface,
    reasoningEffort: typeof body.reasoning_effort === 'string'
      ? body.reasoning_effort
      : typeof (body.reasoning as { effort?: unknown } | undefined)?.effort === 'string'
        ? String((body.reasoning as { effort: string }).effort)
        : '',
    thinkingBudgetTokens: typeof (body.thinking as { budget_tokens?: unknown } | undefined)?.budget_tokens === 'number'
      ? Number((body.thinking as { budget_tokens: number }).budget_tokens)
      : null,
    stream,
    priceCatalogVersion: state.catalogState.active_version || '',
    priceSku: route.officialPriceSku,
    reservationNanoUsd,
    billingMultiplierBPS: Math.round(key.billingMultiplier * 10_000),
    status: succeededTarget ? 'success' : 'failed',
    meteringStatus: succeededTarget ? 'metered' : 'not_applicable',
    meteringErrorCode: '',
    finalAttemptIndex: finalAttempt?.attemptIndex ?? null,
    httpStatus: succeededTarget ? 200 : 503,
    firstOutputMs: succeededTarget ? finalAttempt?.firstOutputMs ?? 310 : null,
    totalDurationMs: Math.max(1, finishedAt - now),
    inputTokens: succeededTarget ? inputTokens : null,
    outputTokens: succeededTarget ? outputTokens : null,
    cacheReadTokens: succeededTarget ? cacheReadTokens : null,
    cacheWriteTokens: succeededTarget ? 0 : null,
    cacheWrite5mTokens: succeededTarget ? 0 : null,
    cacheWrite1hTokens: succeededTarget ? 0 : null,
    reasoningTokens: succeededTarget ? 24 : null,
    officialCostNanoUsd: official,
    chargedNanoUsd,
    quotaCapped,
    errorCode: succeededTarget ? '' : 'all_targets_unavailable',
    startedAt: now,
    finishedAt,
    finalAttempt,
    routeCandidates: candidates,
    attempts,
    ledger: succeededTarget ? [
      { id: nextID(state, 'ledger'), eventType: 'reserve', reservedDeltaNanoUsd: reservationNanoUsd, usedDeltaNanoUsd: 0, priceCatalogVersion: state.catalogState.active_version || '', priceSku: route.officialPriceSku, createdAt: now },
      { id: nextID(state, 'ledger'), eventType: 'settle', reservedDeltaNanoUsd: -reservationNanoUsd, usedDeltaNanoUsd: chargedNanoUsd, priceCatalogVersion: state.catalogState.active_version || '', priceSku: route.officialPriceSku, createdAt: finishedAt },
    ] : [],
  };
  state.requestLogs.unshift(log);
  key.lastUsedAt = now;
  key.usedNanoUSD += chargedNanoUsd;
  key.usedThisHourNanoUSD += chargedNanoUsd;
  key.updatedAt = now;

  if (!succeededTarget) return dataPlaneError(503, 'all_targets_unavailable', 'Every ordered upstream target failed or was cooling.', requestId);
  const responseHeaders = new Headers({ 'X-JieShan-Request-Id': requestId });
  if (stream) {
    responseHeaders.set('Content-Type', 'text/event-stream; charset=utf-8');
    responseHeaders.set('Cache-Control', 'no-store');
    const payload = path === '/v1/messages'
      ? `event: message_start\ndata: ${JSON.stringify({ type: 'message_start', message: { id: requestId, type: 'message', role: 'assistant', model: succeededTarget.sourceModel, content: [] } })}\n\nevent: content_block_delta\ndata: ${JSON.stringify({ type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'Prototype route succeeded.' } })}\n\nevent: message_stop\ndata: ${JSON.stringify({ type: 'message_stop' })}\n\n`
      : `data: ${JSON.stringify({ id: requestId, object: 'chat.completion.chunk', model: succeededTarget.sourceModel, choices: [{ index: 0, delta: { role: 'assistant', content: 'Prototype route succeeded.' }, finish_reason: null }] })}\n\ndata: [DONE]\n\n`;
    return new Response(payload, { status: 200, headers: responseHeaders });
  }
  if (path === '/v1/messages') {
    return json({ id: requestId, type: 'message', role: 'assistant', model: succeededTarget.sourceModel, content: [{ type: 'text', text: 'Prototype route succeeded.' }], stop_reason: 'end_turn', stop_sequence: null, usage: { input_tokens: inputTokens, output_tokens: outputTokens } }, 200, undefined, responseHeaders);
  }
  if (path === '/v1/responses') {
    return json({ id: requestId, object: 'response', created_at: Math.floor(now / 1000), status: 'completed', model: succeededTarget.sourceModel, output: [{ id: `msg_${requestId}`, type: 'message', role: 'assistant', status: 'completed', content: [{ type: 'output_text', text: 'Prototype route succeeded.', annotations: [] }] }], usage: { input_tokens: inputTokens, output_tokens: outputTokens, total_tokens: inputTokens + outputTokens } }, 200, undefined, responseHeaders);
  }
  return json({ id: requestId, object: 'chat.completion', created: Math.floor(now / 1000), model: succeededTarget.sourceModel, choices: [{ index: 0, message: { role: 'assistant', content: 'Prototype route succeeded.' }, finish_reason: 'stop' }], usage: { prompt_tokens: inputTokens, completion_tokens: outputTokens, total_tokens: inputTokens + outputTokens } }, 200, undefined, responseHeaders);
}

async function handleRequest(request: Request, state: PrototypeState): Promise<Response> {
  const url = new URL(request.url);
  const path = url.pathname.replace(/\/$/, '') || '/';
  const method = request.method.toUpperCase();

  if (method === 'POST' && path === '/api/vnext/auth/login') {
    const body = await requestBody<{ username?: string; password?: string }>(request);
    if (body instanceof Response) return body;
    if (body.username !== 'admin' || !body.password?.trim()) {
      return error(401, 'invalid_credentials', 'Administrator credentials are invalid.');
    }
    state.authenticated = true;
    state.sessionExpiresAt = Date.now() + 8 * 60 * 60_000;
    setCSRFCookie(state.csrfToken);
    return json({ initialized: true, authenticated: true, username: state.username, expires_at: state.sessionExpiresAt });
  }

  if (method === 'POST' && path === '/api/vnext/auth/logout') {
    if (!state.authenticated) return error(401, 'unauthenticated', 'Administrator session is not authenticated.');
    const rejected = csrfError(request, state);
    if (rejected) return rejected;
    state.authenticated = false;
    clearCSRFCookie();
    return noContent();
  }

  if (path.startsWith(ADMIN_PREFIX)) {
    if (method === 'GET') {
      const response = adminGET(path, url, state);
      return response || error(404, 'not_found', 'Prototype endpoint was not found.');
    }
    if (!state.authenticated) return error(401, 'unauthenticated', 'Administrator session is not authenticated.');
    const rejected = csrfError(request, state);
    if (rejected) return rejected;
    const response = await handleAdminMutation(request, path, state);
    return response || error(404, 'not_found', 'Prototype endpoint was not found.');
  }

  if (path === '/v1/models') {
    if (method !== 'GET') return dataPlaneError(405, 'method_not_allowed', 'Method is not allowed.');
    const secret = downstreamSecret(request, state);
    if (!secret) return dataPlaneError(401, 'invalid_api_key', 'A single valid JieShan API key is required.');
    const secretEntry = Object.entries(state.downstreamSecrets).find(([, value]) => value === secret);
    const key = secretEntry ? state.downstreamKeys.find((item) => item.id === Number(secretEntry[0])) : undefined;
    if (!key?.enabled) return dataPlaneError(401, 'invalid_api_key', 'A single valid JieShan API key is required.');
    const anthropic = Boolean(request.headers.get('x-api-key') || request.headers.get('anthropic-version'));
    const wire: WireProtocol = anthropic ? 'anthropic' : 'openai';
    const models = state.routes
      .filter((route) => route.routingProfileId === key.routingProfileId && route.enabled && key.models.includes(route.publicName))
      .filter((route) => route.targets.some((target) => target.wireProtocol === wire))
      .map((route) => route.publicName);
    if (anthropic) {
      return json({ data: models.map((id) => ({ id, type: 'model', display_name: id })), has_more: false, first_id: models[0] || '', last_id: models[models.length - 1] || '' });
    }
    return json({ object: 'list', data: models.map((id) => ({ id, object: 'model', owned_by: 'jieshan' })) });
  }

  if (path.startsWith(DATA_PREFIX)) {
    const response = await handleDataPlane(request, path, state);
    return response || dataPlaneError(404, 'not_found', 'Prototype data-plane endpoint was not found.');
  }
  return error(404, 'not_found', 'Prototype endpoint was not found.');
}

export function installPrototypeFetch(): PrototypeController {
  if (activeController) return activeController;
  const originalFetch = globalThis.fetch.bind(globalThis);
  let state = createPrototypeState();
  let installedFetch: typeof fetch;

  const controller: PrototypeController = {
    uninstall() {
      if (globalThis.fetch === installedFetch) globalThis.fetch = originalFetch;
      clearCSRFCookie();
      if (activeController === controller) activeController = null;
    },
    reset() {
      state = createPrototypeState();
      setCSRFCookie(state.csrfToken);
    },
    failNextRequest(options = {}) {
      const normalized = typeof options === 'number' ? { targetId: options } : options;
      const targetId = normalized.targetId || state.routes.find((item) => item.routingProfileId === 1)?.targets[0]?.providerModelTargetId;
      if (!targetId) return;
      for (let index = 0; index < Math.max(1, normalized.times || 1); index += 1) state.failNextTargetIds.push(targetId);
    },
    recoverTarget(targetId: number) {
      state.failNextTargetIds = state.failNextTargetIds.filter((id) => id !== targetId);
      const health = state.routingHealth[targetId];
      if (health) {
        health.phase = 'closed';
        health.consecutiveFailures = 0;
        health.failureWindowStartedAt = null;
        health.cooldownUntil = null;
        health.lastSuccessAt = Date.now();
        health.updatedAt = Date.now();
      }
      for (const model of state.monitorSnapshot.items) {
        const target = model.targets.find((item) => item.providerModelTargetId === targetId);
        if (!target) continue;
        target.status = 'healthy';
        target.health = health ? clone(health) : target.health;
        const history = state.monitorHistories[`${model.publishedModelId}:${targetId}`];
        if (history) {
          history.status = 'healthy';
          history.health = clone(target.health);
        }
        recomputeMonitorModel(model);
      }
      const credential = state.credentials.find((item) => {
        const endpoint = state.modelTargets.find((target) => target.id === targetId)?.endpointId;
        return endpoint ? state.endpointCredentialIds[String(endpoint)]?.includes(item.id) : false;
      });
      if (credential) {
        credential.runtimeState = 'ready';
        credential.coolingUntil = null;
        credential.lastErrorCode = '';
        credential.lastHttpStatus = 200;
        credential.runtimeUpdatedAt = Date.now();
      }
    },
    getState() {
      return clone(state);
    },
  };

  installedFetch = (async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const raw = input instanceof Request ? input.url : String(input);
    const base = new URL(typeof location === 'undefined' ? 'http://localhost/' : location.href);
    const url = new URL(raw, base);
    const intercepted = url.origin === base.origin
      && (url.pathname.startsWith(ADMIN_PREFIX) || url.pathname.startsWith(DATA_PREFIX));
    if (!intercepted) return originalFetch(input, init);
    const request = input instanceof Request
      ? new Request(input, init)
      : new Request(url, init);
    return handleRequest(request, state);
  }) as typeof fetch;

  globalThis.fetch = installedFetch;
  if (state.authenticated) setCSRFCookie(state.csrfToken);
  activeController = controller;
  return controller;
}

export type { PrototypeController, PrototypeFailureOptions, PrototypeState } from './types';
