import type {
  GatewayLogAttempt,
  ModelRoute,
  ModelTarget,
  MonitorProbePoint,
  MonitorTarget,
  PriceCatalog,
  RouteTarget,
  SiteAccountConnection,
  SiteUsageRecord,
} from '../lib/types';
import type {
  PrototypeRequestAttempt,
  PrototypeRequestLog,
  PrototypeRouteCandidate,
  PrototypeState,
} from './types';

const MINUTE = 60_000;

function target(
  input: Pick<ModelTarget, 'id' | 'siteId' | 'siteName' | 'endpointId' | 'endpointName' | 'sourceModel' | 'displayName' | 'wireProtocol' | 'surface' | 'authScheme'>,
  now: number,
): ModelTarget {
  const credentialByEndpoint: Record<number, { id: number; name: string }> = {
    11: { id: 111, name: 'Ciii Primary' },
    12: { id: 112, name: 'Ciii Claude' },
    21: { id: 211, name: 'Tokyo Primary' },
    31: { id: 311, name: 'Gemini Primary' },
  };
  const credential = credentialByEndpoint[input.endpointId];
  return {
    ...input,
    siteEnabled: true,
    endpointEnabled: true,
    baseUrl: `https://${input.siteName.toLowerCase().replaceAll(' ', '-')}.example.com/v1`,
    adapterKind: input.wireProtocol,
    boundCredentialCount: 1,
    usableCredentialCount: 1,
    unknownCredentialCount: 0,
    credentialIds: credential ? [credential.id] : [],
    credentialNames: credential ? [credential.name] : [],
    capabilities: {
      discovery: true,
      request: true,
      response: true,
      stream: true,
      usage: true,
      error: true,
    },
    routable: true,
    enabled: true,
    revision: 1,
    lastSeenAt: now - 2 * MINUTE,
    createdAt: now - 30 * 24 * 60 * MINUTE,
    updatedAt: now - 2 * MINUTE,
  };
}

function routeTarget(modelId: number, item: ModelTarget, position: number, now: number): RouteTarget {
  return {
    id: modelId * 100 + position,
    publishedModelId: modelId,
    siteId: item.siteId,
    siteName: item.siteName,
    endpointId: item.endpointId,
    endpointName: item.endpointName,
    providerModelTargetId: item.id,
    sourceModel: item.sourceModel,
    wireProtocol: item.wireProtocol,
    apiSurface: item.surface,
    position,
    revision: 1,
    createdAt: now - 20 * 24 * 60 * MINUTE,
    updatedAt: now - 30 * MINUTE,
  };
}

function probePoint(
  targetId: number,
  index: number,
  now: number,
  outcome: MonitorProbePoint['outcome'] = 'success',
): MonitorProbePoint {
  const finishedAt = now - (11 - index) * 5 * MINUTE;
  const failed = outcome === 'failure';
  return {
    id: targetId * 100 + index,
    runId: `probe_${targetId}_${index}`,
    providerModelTargetRevision: 1,
    outcome,
    permitMode: 'closed',
    permitReason: 'scheduled_probe',
    httpStatus: failed ? 503 : 200,
    failureKind: failed ? 'upstream_unavailable' : '',
    errorCode: failed ? 'upstream_503' : '',
    totalLatencyMs: failed ? 1_230 : 420 + ((targetId + index * 37) % 460),
    firstOutputMs: failed ? null : 180 + ((targetId + index * 19) % 260),
    startedAt: finishedAt - (failed ? 1_230 : 620),
    finishedAt,
    healthApplied: true,
    healthApplyReason: failed ? 'failure_recorded' : 'success_recorded',
    healthErrorCode: '',
  };
}

function monitorTarget(
  modelTarget: ModelTarget,
  publishedTargetId: number,
  position: number,
  now: number,
  outcomes: MonitorProbePoint['outcome'][] = Array(12).fill('success'),
): MonitorTarget {
  const points = outcomes.map((outcome, index) => probePoint(modelTarget.id, index, now, outcome));
  const successes = points.filter((point) => point.outcome === 'success').length;
  const failures = points.filter((point) => point.outcome === 'failure').length;
  const skipped = points.length - successes - failures;
  const attempted = successes + failures;
  const latest = points[points.length - 1] || null;
  const liveSamples = modelTarget.id === 1001 ? 184 : modelTarget.id === 2001 ? 96 : 74;
  const liveFailures = modelTarget.id === 1001 ? 3 : modelTarget.id === 2001 ? 1 : 0;
  return {
    publishedModelTargetId: publishedTargetId,
    publishedModelTargetRevision: 1,
    providerModelTargetId: modelTarget.id,
    providerModelTargetRevision: modelTarget.revision,
    position,
    siteId: modelTarget.siteId,
    siteName: modelTarget.siteName,
    endpointId: modelTarget.endpointId,
    endpointName: modelTarget.endpointName,
    sourceModel: modelTarget.sourceModel,
    wireProtocol: modelTarget.wireProtocol,
    apiSurface: modelTarget.surface,
    enabled: modelTarget.enabled,
    usableCredentialCount: modelTarget.usableCredentialCount,
    credentialId: modelTarget.credentialIds?.[0],
    credentialName: modelTarget.credentialNames?.[0],
    status: latest?.outcome === 'failure' ? 'degraded' : 'healthy',
    successes,
    failures,
    skipped,
    successBasisPoints: attempted ? Math.round((successes / attempted) * 10_000) : 0,
    latest,
    statusBar: points,
    evidence: {
      liveTraffic: {
        source: 'live_traffic',
        windowMs: 60 * MINUTE,
        samples: liveSamples,
        successes: liveSamples - liveFailures,
        failures: liveFailures,
        skipped: 0,
        successBasisPoints: Math.round(((liveSamples - liveFailures) / liveSamples) * 10_000),
        p50FirstOutputMs: modelTarget.id === 2001 ? 1_480 : 420,
        p95FirstOutputMs: modelTarget.id === 2001 ? 4_880 : 1_260,
        lastObservedAt: now - (modelTarget.id === 2001 ? 4 : 1) * MINUTE,
        lastFailureKind: liveFailures ? 'upstream_unavailable' : '',
      },
      probe: {
        source: 'probe',
        windowMs: 24 * 60 * MINUTE,
        samples: points.length,
        successes,
        failures,
        skipped,
        successBasisPoints: attempted ? Math.round((successes / attempted) * 10_000) : 0,
        p50FirstOutputMs: 360,
        p95FirstOutputMs: modelTarget.id === 2001 ? 16_400 : 610,
        lastObservedAt: latest?.finishedAt || null,
        lastFailureKind: latest?.outcome === 'failure' ? latest.failureKind : '',
      },
    },
    health: {
      phase: latest?.outcome === 'failure' ? 'suspect' : 'closed',
      capability: 'available',
      consecutiveFailures: latest?.outcome === 'failure' ? 1 : 0,
      failureWindowStartedAt: failures ? points.find((point) => point.outcome === 'failure')?.startedAt || null : null,
      lastFailureAt: [...points].reverse().find((point) => point.outcome === 'failure')?.finishedAt || null,
      lastSuccessAt: [...points].reverse().find((point) => point.outcome === 'success')?.finishedAt || null,
      cooldownUntil: null,
      halfOpenLeaseUntil: null,
      lastEventAt: latest?.finishedAt || null,
      lastFailureKind: latest?.outcome === 'failure' ? latest.failureKind : '',
      providerTargetRevision: modelTarget.revision,
      stateVersion: points.length,
      updatedAt: latest?.finishedAt || now,
    },
  };
}

function attempt(
  id: number,
  index: number,
  route: RouteTarget,
  credentialId: number,
  credentialName: string,
  now: number,
  status: 'success' | 'failed',
): PrototypeRequestAttempt {
  const failed = status === 'failed';
  const startedAt = now + index * 700;
  return {
    id,
    attemptIndex: index,
    publishedModelTargetId: route.id,
    publishedModelTargetRevision: route.revision,
    providerModelTargetId: route.providerModelTargetId,
    providerModelTargetRevision: 1,
    siteId: route.siteId,
    siteName: route.siteName,
    endpointId: route.endpointId,
    endpointName: route.endpointName,
    credentialId,
    credentialName,
    sourceModel: route.sourceModel,
    responseModel: failed ? '' : route.sourceModel,
    wireProtocol: route.wireProtocol,
    apiSurface: route.apiSurface,
    status,
    httpStatus: failed ? 503 : 200,
    failureKind: failed ? 'upstream_transient' : '',
    errorCode: failed ? 'upstream_503' : '',
    switchReason: failed ? 'next_target' : '',
    firstOutputMs: failed ? null : 386,
    durationMs: failed ? 642 : 1_284,
    startedAt,
    finishedAt: startedAt + (failed ? 642 : 1_284),
  };
}

function routeCandidate(route: RouteTarget, credentialId: number, credentialName: string): PrototypeRouteCandidate {
  return {
    position: route.position,
    publishedModelTargetId: route.id,
    publishedModelTargetRevision: route.revision,
    providerModelTargetId: route.providerModelTargetId,
    providerModelTargetRevision: 1,
    siteId: route.siteId,
    siteName: route.siteName,
    endpointId: route.endpointId,
    endpointName: route.endpointName,
    sourceModel: route.sourceModel,
    wireProtocol: route.wireProtocol,
    apiSurface: route.apiSurface,
    credentials: [{ id: credentialId, name: credentialName, position: 0, runtimeState: 'ready', coolingUntil: null }],
    initialEligibility: 'eligible',
    initialReason: 'ready',
    disposition: 'not_attempted',
    dispositionReason: '',
    attemptCount: 0,
    firstAttemptIndex: null,
    lastAttemptIndex: null,
  };
}

function siteUsage(now: number, siteId: number, prefix: string): SiteUsageRecord[] {
  return Array.from({ length: 12 }, (_, index) => {
    const model = index % 2 === 0 ? 'gpt-5.2' : 'claude-sonnet-4-5';
    const durationMs = 980 + index * 83;
    return ({
    id: siteId * 1000 + index,
    remoteId: `${prefix}-${8_000 + index}`,
    requestId: `req_demo_${siteId}_${index}`,
    upstreamRequestId: `up_${siteId}_${index}`,
    occurredAt: now - index * 17 * MINUTE,
    model,
    upstreamModel: index % 2 === 0 ? 'gpt-5.2-2026-07-15' : 'claude-sonnet-4-5',
    status: index === 5 ? 'failed' : 'success',
    httpStatus: index === 5 ? 429 : 200,
    inputTokens: 1_120 + index * 73,
    outputTokens: 360 + index * 31,
    cacheReadTokens: index % 3 === 0 ? 640 : 0,
    cacheWriteTokens: 0,
    reasoningTokens: index % 2 === 0 ? 120 + index * 4 : null,
    totalTokens: 1_480 + index * 104,
    chargeValue: (0.0034 + index * 0.0007).toFixed(6),
    chargeUnit: 'USD',
    durationMs,
    firstOutputMs: index === 5 ? null : Math.round(durationMs * 0.32),
    apiKeyName: index % 2 === 0 ? 'primary' : 'backup',
    reasoningEffort: index % 2 === 0 ? 'high' : 'default',
    requestPath: model.startsWith('claude') ? '/v1/messages' : '/v1/chat/completions',
    requestType: '流式',
    billingMode: '按量',
    requestIp: `210.16.177.${200 + index}`,
    region: '待获取地区',
    group: `${prefix.toUpperCase()} 默认分组`,
    sourceFetchedAt: now - 2 * MINUTE,
  });
  });
}

function catalog(now: number): PriceCatalog {
  const iso = new Date(now - 24 * 60 * MINUTE).toISOString();
  return {
    schema_version: 1,
    version: '2026-08-01',
    digest: 'sha256:prototype-official-pricing-20260801',
    settlement_currency: 'USD',
    source: 'official-provider-pages',
    source_digest: 'sha256:prototype-sources',
    fetched_at: iso,
    verified_at: iso,
    effective_at: iso,
    imported_at: iso,
    entries: [
      { sku: 'openai/gpt-5.2', provider: 'OpenAI', model_pattern: '^gpt-5\\.2$', native_currency: 'USD', rates: [
        { class: 'input', native_price_per_million: '1.75', nano_usd_per_million: 1_750_000_000 },
        { class: 'output', native_price_per_million: '14.00', nano_usd_per_million: 14_000_000_000 },
        { class: 'cache_read', native_price_per_million: '0.175', nano_usd_per_million: 175_000_000 },
      ] },
      { sku: 'anthropic/claude-sonnet-4-5', provider: 'Anthropic', model_pattern: '^claude-sonnet-4-5$', native_currency: 'USD', rates: [
        { class: 'input', native_price_per_million: '3.00', nano_usd_per_million: 3_000_000_000 },
        { class: 'output', native_price_per_million: '15.00', nano_usd_per_million: 15_000_000_000 },
        { class: 'cache_read', native_price_per_million: '0.30', nano_usd_per_million: 300_000_000 },
      ] },
      { sku: 'google/gemini-2.5-pro', provider: 'Google', model_pattern: '^gemini-2\\.5-pro$', native_currency: 'USD', rates: [
        { class: 'input', native_price_per_million: '1.25', nano_usd_per_million: 1_250_000_000 },
        { class: 'output', native_price_per_million: '10.00', nano_usd_per_million: 10_000_000_000 },
      ] },
      { sku: 'deepseek/deepseek-chat', provider: 'DeepSeek', model_pattern: '^deepseek-chat$', native_currency: 'USD', rates: [
        { class: 'input', native_price_per_million: '0.27', nano_usd_per_million: 270_000_000 },
        { class: 'output', native_price_per_million: '1.10', nano_usd_per_million: 1_100_000_000 },
      ] },
    ],
  };
}

export function createPrototypeState(now = Date.now()): PrototypeState {
  const modelTargets = [
    target({ id: 1001, siteId: 1, siteName: 'Ciii', endpointId: 11, endpointName: 'OpenAI 主线路', sourceModel: 'gpt-5.2', displayName: 'GPT-5.2', wireProtocol: 'openai', surface: 'openai.chat_completions', authScheme: 'bearer' }, now),
    target({ id: 1002, siteId: 1, siteName: 'Ciii', endpointId: 12, endpointName: 'Anthropic 线路', sourceModel: 'claude-sonnet-4-5', displayName: 'Claude Sonnet 4.5', wireProtocol: 'anthropic', surface: 'anthropic.messages', authScheme: 'x-api-key' }, now),
    target({ id: 2001, siteId: 2, siteName: 'Tokyo Relay', endpointId: 21, endpointName: 'OpenAI 备用线路', sourceModel: 'gpt-5.2', displayName: 'GPT-5.2 Tokyo', wireProtocol: 'openai', surface: 'openai.chat_completions', authScheme: 'bearer' }, now),
    target({ id: 3001, siteId: 3, siteName: 'Gemini Hub', endpointId: 31, endpointName: 'Gemini 原生线路', sourceModel: 'gemini-2.5-pro', displayName: 'Gemini 2.5 Pro', wireProtocol: 'gemini', surface: 'gemini.generate_content', authScheme: 'x-goog-api-key' }, now),
  ];
  const byTarget = new Map(modelTargets.map((item) => [item.id, item]));
  const gptTargets = [routeTarget(501, byTarget.get(1001)!, 0, now), routeTarget(501, byTarget.get(2001)!, 1, now)];
  const claudeTargets = [routeTarget(502, byTarget.get(1002)!, 0, now)];
  const geminiTargets = [routeTarget(503, byTarget.get(3001)!, 0, now)];
  const routes: ModelRoute[] = [
    { routingProfileId: 1, routingProfileName: '默认路由', sourceProfileId: 1, sourceProfileName: '默认路由', inherited: false, targetsOverridden: true, publishedModelId: 501, publicName: 'gpt-5.2', officialPriceSku: 'openai/gpt-5.2', enabled: true, publishedModelRevision: 1, revision: 3, targets: gptTargets, createdAt: now - 20 * 24 * 60 * MINUTE, updatedAt: now - 30 * MINUTE },
    { routingProfileId: 1, routingProfileName: '默认路由', sourceProfileId: 1, sourceProfileName: '默认路由', inherited: false, targetsOverridden: true, publishedModelId: 502, publicName: 'claude-sonnet-4-5', officialPriceSku: 'anthropic/claude-sonnet-4-5', enabled: true, publishedModelRevision: 1, revision: 1, targets: claudeTargets, createdAt: now - 18 * 24 * 60 * MINUTE, updatedAt: now - 45 * MINUTE },
    { routingProfileId: 1, routingProfileName: '默认路由', sourceProfileId: 1, sourceProfileName: '默认路由', inherited: false, targetsOverridden: true, publishedModelId: 503, publicName: 'gemini-2.5-pro', officialPriceSku: 'google/gemini-2.5-pro', enabled: true, publishedModelRevision: 1, revision: 1, targets: geminiTargets, createdAt: now - 16 * 24 * 60 * MINUTE, updatedAt: now - 50 * MINUTE },
    { routingProfileId: 2, routingProfileName: '代码优先', sourceProfileId: 2, sourceProfileName: '代码优先', inherited: false, targetsOverridden: true, publishedModelId: 501, publicName: 'gpt-5.2', officialPriceSku: 'openai/gpt-5.2', enabled: true, publishedModelRevision: 1, revision: 2, targets: [gptTargets[1], gptTargets[0]].map((item, index) => ({ ...item, position: index })), createdAt: now - 10 * 24 * 60 * MINUTE, updatedAt: now - 15 * MINUTE },
    { routingProfileId: 2, routingProfileName: '代码优先', sourceProfileId: 1, sourceProfileName: '默认路由', inherited: true, targetsOverridden: false, publishedModelId: 502, publicName: 'claude-sonnet-4-5', officialPriceSku: 'anthropic/claude-sonnet-4-5', enabled: true, publishedModelRevision: 1, revision: 1, targets: claudeTargets, createdAt: now - 10 * 24 * 60 * MINUTE, updatedAt: now - 45 * MINUTE },
  ];

  const gptMonitorTargets = [
    monitorTarget(byTarget.get(1001)!, gptTargets[0].id, 0, now, ['success','success','success','success','failure','success','success','success','success','success','success','success']),
    monitorTarget(byTarget.get(2001)!, gptTargets[1].id, 1, now),
  ];
  const slowFirstOutputTarget = gptMonitorTargets[1];
  const slowFirstOutputPoint = slowFirstOutputTarget.statusBar[slowFirstOutputTarget.statusBar.length - 1];
  if (slowFirstOutputPoint) {
    slowFirstOutputPoint.firstOutputMs = 16_400;
    slowFirstOutputPoint.totalLatencyMs = 17_180;
    slowFirstOutputPoint.startedAt = slowFirstOutputPoint.finishedAt - slowFirstOutputPoint.totalLatencyMs;
    slowFirstOutputTarget.latest = slowFirstOutputPoint;
  }
  const claudeMonitorTargets = [monitorTarget(byTarget.get(1002)!, claudeTargets[0].id, 0, now)];
  const monitorItems = [
    { publishedModelId: 501, publicModel: 'gpt-5.2', officialPriceSku: 'openai/gpt-5.2', publishedModelEnabled: true, publishedModelRevision: 1, status: 'healthy' as const, monitor: { enabled: true, intervalMs: 15 * MINUTE, historyLimit: 120, nextProbeAt: now + 3 * MINUTE, lastProbeStartedAt: now - 2 * MINUTE - 620, lastProbeFinishedAt: now - 2 * MINUTE, busy: false, revision: 2, createdAt: now - 10 * 24 * 60 * MINUTE, updatedAt: now - 2 * MINUTE }, targets: gptMonitorTargets, successes: gptMonitorTargets.reduce((sum, item) => sum + item.successes, 0), failures: gptMonitorTargets.reduce((sum, item) => sum + item.failures, 0), skipped: 0, successBasisPoints: 9_583 },
    { publishedModelId: 502, publicModel: 'claude-sonnet-4-5', officialPriceSku: 'anthropic/claude-sonnet-4-5', publishedModelEnabled: true, publishedModelRevision: 1, status: 'healthy' as const, monitor: { enabled: true, intervalMs: 15 * MINUTE, historyLimit: 120, nextProbeAt: now + 3 * MINUTE, lastProbeStartedAt: now - 2 * MINUTE - 620, lastProbeFinishedAt: now - 2 * MINUTE, busy: false, revision: 1, createdAt: now - 9 * 24 * 60 * MINUTE, updatedAt: now - 2 * MINUTE }, targets: claudeMonitorTargets, successes: 12, failures: 0, skipped: 0, successBasisPoints: 10_000 },
  ];

  const successStart = now - 7 * MINUTE;
  const firstFailed = attempt(1, 0, gptTargets[0], 111, 'Ciii Primary', successStart, 'failed');
  const secondSucceeded = attempt(2, 1, gptTargets[1], 211, 'Tokyo Primary', successStart, 'success');
  const candidates = [routeCandidate(gptTargets[0], 111, 'Ciii Primary'), routeCandidate(gptTargets[1], 211, 'Tokyo Primary')];
  candidates[0] = { ...candidates[0], disposition: 'attempted', dispositionReason: 'next_target', attemptCount: 1, firstAttemptIndex: 0, lastAttemptIndex: 0 };
  candidates[1] = { ...candidates[1], disposition: 'attempted', dispositionReason: 'success', attemptCount: 1, firstAttemptIndex: 1, lastAttemptIndex: 1 };
  const claudeStart = now - 22 * MINUTE;
  const claudeSucceeded = attempt(3, 0, claudeTargets[0], 112, 'Ciii Claude', claudeStart, 'success');
  const claudeCandidate = { ...routeCandidate(claudeTargets[0], 112, 'Ciii Claude'), disposition: 'attempted', dispositionReason: 'success', attemptCount: 1, firstAttemptIndex: 0, lastAttemptIndex: 0 };
  const errorStart = now - 47 * MINUTE;
  const errorAttempts = [
    attempt(4, 0, gptTargets[0], 111, 'Ciii Primary', errorStart, 'failed'),
    attempt(5, 1, gptTargets[1], 211, 'Tokyo Primary', errorStart, 'failed'),
  ];
  const errorCandidates = [routeCandidate(gptTargets[0], 111, 'Ciii Primary'), routeCandidate(gptTargets[1], 211, 'Tokyo Primary')]
    .map((item, index) => ({ ...item, disposition: 'attempted', dispositionReason: 'next_target', attemptCount: 1, firstAttemptIndex: index, lastAttemptIndex: index }));
  const requestLogs: PrototypeRequestLog[] = [
    {
      id: 'req_demo_failover', downstreamKeyId: 1, downstreamKeyName: '个人主密钥', publishedModelId: 501,
      publishedModelRevision: 1, effectiveRoutingProfileId: 1, effectiveRoutingProfileName: '默认路由',
      sourceRoutingProfileId: 1, sourceRoutingProfileName: '默认路由', routeRevision: 3,
      publicModel: 'gpt-5.2', apiSurface: 'openai.chat_completions', reasoningEffort: 'high',
      thinkingBudgetTokens: null, stream: true, priceCatalogVersion: '2026-08-01', priceSku: 'openai/gpt-5.2',
      reservationNanoUsd: 80_000_000, billingMultiplierBPS: 10_000, status: 'success', meteringStatus: 'metered', meteringErrorCode: '',
      finalAttemptIndex: 1, httpStatus: 200, firstOutputMs: 386, totalDurationMs: 2_626,
      inputTokens: 2_340, outputTokens: 812, cacheReadTokens: 1_024, cacheWriteTokens: 0,
      cacheWrite5mTokens: 0, cacheWrite1hTokens: 0, reasoningTokens: 226,
      officialCostNanoUsd: 15_212_000, chargedNanoUsd: 15_212_000, quotaCapped: false, errorCode: '',
      startedAt: successStart, finishedAt: secondSucceeded.finishedAt, finalAttempt: secondSucceeded,
      routeCandidates: candidates, attempts: [firstFailed, secondSucceeded], ledger: [
        { id: 1, eventType: 'reserve', reservedDeltaNanoUsd: 80_000_000, usedDeltaNanoUsd: 0, priceCatalogVersion: '2026-08-01', priceSku: 'openai/gpt-5.2', createdAt: successStart },
        { id: 2, eventType: 'settle', reservedDeltaNanoUsd: -80_000_000, usedDeltaNanoUsd: 15_212_000, priceCatalogVersion: '2026-08-01', priceSku: 'openai/gpt-5.2', createdAt: secondSucceeded.finishedAt },
      ],
    },
    {
      id: 'req_demo_claude', downstreamKeyId: 2, downstreamKeyName: '代码客户端', publishedModelId: 502,
      publishedModelRevision: 1, effectiveRoutingProfileId: 2, effectiveRoutingProfileName: '代码优先',
      sourceRoutingProfileId: 1, sourceRoutingProfileName: '默认路由', routeRevision: 1,
      publicModel: 'claude-sonnet-4-5', apiSurface: 'anthropic.messages', reasoningEffort: '',
      thinkingBudgetTokens: 4_096, stream: false, priceCatalogVersion: '2026-08-01', priceSku: 'anthropic/claude-sonnet-4-5',
      reservationNanoUsd: 120_000_000, billingMultiplierBPS: 10_000, status: 'success', meteringStatus: 'metered', meteringErrorCode: '',
      finalAttemptIndex: 0, httpStatus: 200, firstOutputMs: 448, totalDurationMs: 1_408,
      inputTokens: 1_540, outputTokens: 490, cacheReadTokens: 600, cacheWriteTokens: 0,
      cacheWrite5mTokens: 0, cacheWrite1hTokens: 0, reasoningTokens: 188,
      officialCostNanoUsd: 11_790_000, chargedNanoUsd: 11_790_000, quotaCapped: false, errorCode: '',
      startedAt: claudeStart, finishedAt: claudeSucceeded.finishedAt, finalAttempt: claudeSucceeded,
      routeCandidates: [claudeCandidate], attempts: [claudeSucceeded], ledger: [
        { id: 3, eventType: 'reserve', reservedDeltaNanoUsd: 120_000_000, usedDeltaNanoUsd: 0, priceCatalogVersion: '2026-08-01', priceSku: 'anthropic/claude-sonnet-4-5', createdAt: claudeStart },
        { id: 4, eventType: 'settle', reservedDeltaNanoUsd: -120_000_000, usedDeltaNanoUsd: 11_790_000, priceCatalogVersion: '2026-08-01', priceSku: 'anthropic/claude-sonnet-4-5', createdAt: claudeSucceeded.finishedAt },
      ],
    },
    {
      id: 'req_demo_error', downstreamKeyId: 1, downstreamKeyName: '个人主密钥', publishedModelId: 501,
      publishedModelRevision: 1, effectiveRoutingProfileId: 1, effectiveRoutingProfileName: '默认路由',
      sourceRoutingProfileId: 1, sourceRoutingProfileName: '默认路由', routeRevision: 3,
      publicModel: 'gpt-5.2', apiSurface: 'openai.chat_completions', reasoningEffort: 'medium',
      thinkingBudgetTokens: null, stream: false, priceCatalogVersion: '2026-08-01', priceSku: 'openai/gpt-5.2',
      reservationNanoUsd: 50_000_000, billingMultiplierBPS: 10_000, status: 'failed', meteringStatus: 'not_applicable', meteringErrorCode: '',
      finalAttemptIndex: 1, httpStatus: 503, firstOutputMs: null, totalDurationMs: 3_102,
      inputTokens: null, outputTokens: null, cacheReadTokens: null, cacheWriteTokens: null,
      cacheWrite5mTokens: null, cacheWrite1hTokens: null, reasoningTokens: null,
      officialCostNanoUsd: 0, chargedNanoUsd: 0, quotaCapped: false, errorCode: 'all_targets_unavailable',
      startedAt: errorStart, finishedAt: errorAttempts[1].finishedAt, finalAttempt: errorAttempts[1],
      routeCandidates: errorCandidates, attempts: errorAttempts, ledger: [
        { id: 5, eventType: 'reserve', reservedDeltaNanoUsd: 50_000_000, usedDeltaNanoUsd: 0, priceCatalogVersion: '2026-08-01', priceSku: 'openai/gpt-5.2', createdAt: errorStart },
        { id: 6, eventType: 'release', reservedDeltaNanoUsd: -50_000_000, usedDeltaNanoUsd: 0, priceCatalogVersion: '2026-08-01', priceSku: 'openai/gpt-5.2', createdAt: errorAttempts[1].finishedAt },
      ],
    },
  ];

  const priceCatalog = catalog(now);
  const account = (siteId: number, siteName: string, adapterKind: string, balance: string): SiteAccountConnection => ({
    id: siteId, siteId, siteName, adapterKind, origin: `https://${siteName.toLowerCase().replaceAll(' ', '-')}.example.com`,
    secretConfigured: true, enabled: true, capabilities: { sessionRefresh: true, balance: true, usage: true },
    adapterAvailable: true, lastSessionRefreshAt: now - 8 * MINUTE, lastBalanceRefreshAt: now - 6 * MINUTE,
    lastUsageRefreshAt: now - 4 * MINUTE, lastErrorOperation: '', lastErrorCode: '', lastErrorAt: null,
    latestBalance: { id: siteId, accountRemoteId: `remote-${siteId}`, accountName: 'JieShan', availableValue: balance, availableUnit: 'USD', usedValue: siteId === 1 ? '18.42' : '6.70', usedUnit: 'USD', capturedAt: now - 6 * MINUTE },
    revision: 2, createdAt: now - 14 * 24 * 60 * MINUTE, updatedAt: now - 4 * MINUTE,
  });

  return {
    authenticated: true,
    username: 'admin',
    sessionExpiresAt: now + 8 * 60 * MINUTE,
    csrfToken: 'prototype-csrf-token',
    sites: [
      { id: 1, name: 'Ciii', dashboardUrl: 'https://codex.ciii.club', enabled: true, maxConcurrency: 24, revision: 2, createdAt: now - 40 * 24 * 60 * MINUTE, updatedAt: now - 20 * MINUTE },
      { id: 2, name: 'Tokyo Relay', dashboardUrl: 'https://tokyo-relay.example.com', enabled: true, maxConcurrency: 12, revision: 1, createdAt: now - 28 * 24 * 60 * MINUTE, updatedAt: now - 30 * MINUTE },
      { id: 3, name: 'Gemini Hub', dashboardUrl: 'https://gemini-hub.example.com', enabled: true, maxConcurrency: 8, revision: 1, createdAt: now - 21 * 24 * 60 * MINUTE, updatedAt: now - 40 * MINUTE },
    ],
    endpoints: [
      { id: 11, siteId: 1, name: 'OpenAI 主线路', baseUrl: 'https://codex.ciii.club/v1', wireProtocol: 'openai', surface: 'openai.chat_completions', adapterKind: 'openai', authScheme: 'bearer', headers: {}, secretHeadersConfigured: false, position: 0, enabled: true, revision: 2, createdAt: now - 30 * 24 * 60 * MINUTE, updatedAt: now - 20 * MINUTE },
      { id: 12, siteId: 1, name: 'Anthropic 线路', baseUrl: 'https://codex.ciii.club', wireProtocol: 'anthropic', surface: 'anthropic.messages', adapterKind: 'anthropic', authScheme: 'x-api-key', headers: { 'anthropic-version': '2023-06-01' }, secretHeadersConfigured: false, position: 1, enabled: true, revision: 1, createdAt: now - 20 * 24 * 60 * MINUTE, updatedAt: now - 30 * MINUTE },
      { id: 21, siteId: 2, name: 'OpenAI 备用线路', baseUrl: 'https://tokyo-relay.example.com/v1', wireProtocol: 'openai', surface: 'openai.chat_completions', adapterKind: 'openai', authScheme: 'bearer', headers: {}, secretHeadersConfigured: false, position: 0, enabled: true, revision: 1, createdAt: now - 28 * 24 * 60 * MINUTE, updatedAt: now - 25 * MINUTE },
      { id: 31, siteId: 3, name: 'Gemini 原生线路', baseUrl: 'https://gemini-hub.example.com', wireProtocol: 'gemini', surface: 'gemini.generate_content', adapterKind: 'gemini', authScheme: 'x-goog-api-key', headers: {}, secretHeadersConfigured: false, position: 0, enabled: true, revision: 1, createdAt: now - 21 * 24 * 60 * MINUTE, updatedAt: now - 25 * MINUTE },
    ],
    credentials: [
      { id: 111, siteId: 1, name: 'Ciii Primary', secretConfigured: true, enabled: true, revision: 2, runtimeState: 'ready', coolingUntil: null, lastHttpStatus: 200, lastErrorCode: '', runtimeRevision: 8, runtimeUpdatedAt: now - 2 * MINUTE, createdAt: now - 30 * 24 * 60 * MINUTE, updatedAt: now - 20 * MINUTE },
      { id: 112, siteId: 1, name: 'Ciii Claude', secretConfigured: true, enabled: true, revision: 1, runtimeState: 'ready', coolingUntil: null, lastHttpStatus: 200, lastErrorCode: '', runtimeRevision: 6, runtimeUpdatedAt: now - 2 * MINUTE, createdAt: now - 20 * 24 * 60 * MINUTE, updatedAt: now - 30 * MINUTE },
      { id: 211, siteId: 2, name: 'Tokyo Primary', secretConfigured: true, enabled: true, revision: 1, runtimeState: 'ready', coolingUntil: null, lastHttpStatus: 200, lastErrorCode: '', runtimeRevision: 7, runtimeUpdatedAt: now - 2 * MINUTE, createdAt: now - 28 * 24 * 60 * MINUTE, updatedAt: now - 25 * MINUTE },
      { id: 311, siteId: 3, name: 'Gemini Primary', secretConfigured: true, enabled: true, revision: 1, runtimeState: 'ready', coolingUntil: null, lastHttpStatus: 200, lastErrorCode: '', runtimeRevision: 5, runtimeUpdatedAt: now - 2 * MINUTE, createdAt: now - 21 * 24 * 60 * MINUTE, updatedAt: now - 25 * MINUTE },
    ],
    endpointCredentialIds: { '11': [111], '12': [112], '21': [211], '31': [311] },
    modelTargets,
    discoveredModels: { '11': ['gpt-5.2', 'gpt-5.2-mini', 'o4-mini'], '12': ['claude-sonnet-4-5', 'claude-opus-4-1'], '21': ['gpt-5.2', 'gpt-5.2-mini'], '31': ['gemini-2.5-pro', 'gemini-2.5-flash'] },
    routingProfiles: [
      { id: 1, name: '默认路由', isDefault: true, revision: 4, modelCount: 3, localModelCount: 3, inheritedModelCount: 0, downstreamKeyCount: 1, createdAt: now - 20 * 24 * 60 * MINUTE, updatedAt: now - 30 * MINUTE },
      { id: 2, name: '代码优先', isDefault: false, revision: 2, modelCount: 2, localModelCount: 1, inheritedModelCount: 1, downstreamKeyCount: 1, createdAt: now - 10 * 24 * 60 * MINUTE, updatedAt: now - 15 * MINUTE },
    ],
    routes,
    downstreamKeys: [
      { id: 1, name: '个人主密钥', keyPrefix: 'js_live_demo1', enabled: true, revealable: true, quotaNanoUSD: 25_000_000_000, usedNanoUSD: 2_740_000_000, reservedNanoUSD: 0, hourlyQuotaNanoUSD: 2_000_000_000, usedThisHourNanoUSD: 184_000_000, reservedThisHourNanoUSD: 0, hourlyWindowStartedAt: now - 38 * MINUTE, billingMultiplier: 1, expires: null, lastUsedAt: now - 7 * MINUTE, revision: 2, routingProfileId: 1, routingProfileName: '默认路由', usesDefaultRoutingProfile: true, models: ['gpt-5.2','claude-sonnet-4-5','gemini-2.5-pro'], createdAt: now - 14 * 24 * 60 * MINUTE, updatedAt: now - 7 * MINUTE },
      { id: 2, name: '代码客户端', keyPrefix: 'js_live_demo2', enabled: true, revealable: true, quotaNanoUSD: 10_000_000_000, usedNanoUSD: 1_320_000_000, reservedNanoUSD: 0, hourlyQuotaNanoUSD: 800_000_000, usedThisHourNanoUSD: 96_000_000, reservedThisHourNanoUSD: 0, hourlyWindowStartedAt: now - 38 * MINUTE, billingMultiplier: 1, expires: null, lastUsedAt: now - 22 * MINUTE, revision: 1, routingProfileId: 2, routingProfileName: '代码优先', usesDefaultRoutingProfile: false, models: ['gpt-5.2','claude-sonnet-4-5'], createdAt: now - 9 * 24 * 60 * MINUTE, updatedAt: now - 22 * MINUTE },
    ],
    downstreamSecrets: { 1: 'js_prototype_personal_6fc2d9d8', 2: 'js_prototype_code_8a10e43b' },
    siteAccounts: { 1: account(1, 'Ciii', 'ciii', '86.58'), 2: account(2, 'Tokyo Relay', 'new_api', '42.20') },
    siteUsage: { 1: siteUsage(now, 1, 'ciii'), 2: siteUsage(now, 2, 'tokyo'), 3: [] },
    sitePlatformDetections: {
      1: {
        siteId: 1, state: 'detected', verdict: 'trusted', selectedPlatform: 'new_api', selectedPlatformLabel: 'New API', selectionLocked: false, confidence: 'high', score: 94,
        capabilities: { sessionRefresh: true, balance: true, usage: true }, checkedAt: now - 19 * MINUTE, detectedAt: now - 19 * MINUTE,
        candidates: [
          { platform: 'new_api', label: 'New API', confidence: 'high', score: 94, supported: true, capabilities: { sessionRefresh: true, balance: true, usage: true }, evidenceIds: ['header-server', 'api-shape', 'html-marker'] },
          { platform: 'one_api', label: 'One API', confidence: 'low', score: 28, supported: true, capabilities: { sessionRefresh: true, balance: true, usage: true }, evidenceIds: ['api-shape'] },
        ],
        evidence: [
          { id: 'header-server', source: 'response_header', signal: 'x-oneapi-version', observedValue: 'v0.6.8', matched: true, weight: 38, observedAt: now - 19 * MINUTE },
          { id: 'api-shape', source: 'api_shape', signal: '/api/status response', observedValue: 'data.version + data.system_name', matched: true, weight: 34, observedAt: now - 19 * MINUTE },
          { id: 'html-marker', source: 'html_marker', signal: 'frontend bundle marker', observedValue: '__NEW_API__', matched: true, weight: 22, observedAt: now - 19 * MINUTE },
        ],
        errors: [],
      },
      2: {
        siteId: 2, state: 'ambiguous', verdict: 'possible', selectedPlatform: 'one_api', selectedPlatformLabel: 'One API', selectionLocked: false, confidence: 'medium', score: 68,
        capabilities: { sessionRefresh: true, balance: true, usage: true }, checkedAt: now - 29 * MINUTE, detectedAt: now - 29 * MINUTE,
        candidates: [
          { platform: 'one_api', label: 'One API', confidence: 'medium', score: 68, supported: true, capabilities: { sessionRefresh: true, balance: true, usage: true }, evidenceIds: ['tokyo-api-shape', 'tokyo-html'] },
          { platform: 'new_api', label: 'New API', confidence: 'low', score: 51, supported: true, capabilities: { sessionRefresh: true, balance: true, usage: true }, evidenceIds: ['tokyo-api-shape'] },
        ],
        evidence: [
          { id: 'tokyo-api-shape', source: 'api_shape', signal: '/api/status response', observedValue: 'legacy status envelope', matched: true, weight: 48, observedAt: now - 29 * MINUTE },
          { id: 'tokyo-html', source: 'html_marker', signal: 'page title', observedValue: 'One API', matched: true, weight: 20, observedAt: now - 29 * MINUTE },
        ],
        errors: [],
      },
      3: {
        siteId: 3, state: 'unknown', verdict: 'unknown', selectedPlatform: '', selectedPlatformLabel: '', selectionLocked: false, confidence: 'unknown', score: 0,
        capabilities: { sessionRefresh: false, balance: false, usage: false }, checkedAt: now - 39 * MINUTE, detectedAt: null,
        candidates: [], evidence: [],
        errors: [
          { probeId: 'status', path: '/api/status', code: 'http_status', status: 404, message: 'platform detection probe returned HTTP 404', observedAt: now - 39 * MINUTE },
          { probeId: 'sub2api-public-settings', path: '/api/v1/settings/public', code: 'http_status', status: 404, message: 'platform detection probe returned HTTP 404', observedAt: now - 39 * MINUTE },
        ],
      },
    },
    siteRuntimeStatus: {
      1: { siteId: 1, inflightRequests: 7, maxConcurrency: 24, queuedRequests: 2, updatedAt: now - 8_000, throttledUntil: null },
      2: { siteId: 2, inflightRequests: 3, maxConcurrency: 12, queuedRequests: 0, updatedAt: now - 8_000, throttledUntil: null },
      3: { siteId: 3, inflightRequests: 1, maxConcurrency: 8, queuedRequests: 0, updatedAt: now - 8_000, throttledUntil: null },
    },
    tokenImportPreviews: {},
    priceCatalogs: [priceCatalog],
    catalogState: { active_version: priceCatalog.version, revision: 1, updated_at: new Date(now - 24 * 60 * MINUTE).toISOString() },
    monitorSnapshot: { items: monitorItems },
    monitorHistories: Object.fromEntries(monitorItems.flatMap((model) => model.targets.map((item) => [`${model.publishedModelId}:${item.providerModelTargetId}`, { publishedModelId: model.publishedModelId, publicModel: model.publicModel, target: { publishedModelTargetId: item.publishedModelTargetId, providerModelTargetId: item.providerModelTargetId, position: item.position, siteId: item.siteId, siteName: item.siteName, endpointId: item.endpointId, endpointName: item.endpointName, sourceModel: item.sourceModel, wireProtocol: item.wireProtocol, apiSurface: item.apiSurface }, status: item.status, successes: item.successes, failures: item.failures, skipped: item.skipped, total: item.statusBar.length, attempted: item.successes + item.failures, successBasisPoints: item.successBasisPoints, health: item.health, order: 'oldest_first' as const, items: item.statusBar, circuitTransitions: item.providerModelTargetId === 1001 ? [
      { id: 'circuit-1001-1', fromPhase: 'closed', toPhase: 'suspect', trigger: 'live_traffic' as const, reason: 'first_failure', failureKind: 'upstream_unavailable', requestId: 'req_01J7ERROR', occurredAt: now - 47 * MINUTE },
      { id: 'circuit-1001-2', fromPhase: 'suspect', toPhase: 'closed', trigger: 'probe' as const, reason: 'probe_recovered', failureKind: '', requestId: '', occurredAt: now - 42 * MINUTE },
    ] : [] }]))),
    routingHealth: Object.fromEntries(modelTargets.map((modelTarget) => {
      const monitored = monitorItems.flatMap((model) => model.targets).find((item) => item.providerModelTargetId === modelTarget.id);
      return [modelTarget.id, monitored?.health ? structuredClone(monitored.health) : { phase: 'closed', capability: 'available', consecutiveFailures: 0, failureWindowStartedAt: null, lastFailureAt: null, lastSuccessAt: null, cooldownUntil: null, halfOpenLeaseUntil: null, lastEventAt: null, lastFailureKind: '', providerTargetRevision: modelTarget.revision, stateVersion: 1, updatedAt: now }];
    })),
    requestLogs,
    settings: { failureThreshold: 2, failureWindowMs: 5 * MINUTE, cooldownMs: 15 * MINUTE, probeIntervalMs: 15 * MINUTE, firstOutputTimeoutMs: 15_000, streamIdleTimeoutMs: 45_000, requestTimeoutMs: 120_000, maxAttempts: 3, logRetentionDays: 30, revision: 3 },
    systemHealth: {
      runtime: { processStartedAt: now - 9 * 60 * MINUTE, snapshotAt: now - 8_000, configRevision: 3, configLoadedAt: now - 42 * MINUTE, activePriceCatalogVersion: priceCatalog.version, inflightRequests: 11, maxConcurrency: 64, queuedRequests: 2, meteringMode: 'degraded' },
      meteringWarnings: [{ code: 'usage_missing_cache_write', severity: 'warning', title: '部分上游未返回完整缓存计量', message: '最近 24 小时有 14 个请求缺少 cache_write 明细，账单仍按可用 Token 字段结算并标记为降级。', affectedRequests: 14, since: now - 6 * 60 * MINUTE, lastSeenAt: now - 8 * MINUTE }],
      backgroundTasks: [
        { id: 'monitor-probes', label: '模型自动探针', state: 'healthy', schedule: '每 5 分钟', lastStartedAt: now - 2 * MINUTE - 620, lastFinishedAt: now - 2 * MINUTE, nextRunAt: now + 3 * MINUTE, lastDurationMs: 620, lastErrorCode: '' },
        { id: 'usage-sync', label: '站点用量同步', state: 'delayed', schedule: '每 10 分钟', lastStartedAt: now - 16 * MINUTE, lastFinishedAt: now - 15 * MINUTE, nextRunAt: now - 5 * MINUTE, lastDurationMs: 48_200, lastErrorCode: 'upstream_rate_limited' },
        { id: 'log-retention', label: '日志保留清理', state: 'healthy', schedule: '每天 03:20', lastStartedAt: now - 11 * 60 * MINUTE, lastFinishedAt: now - 11 * 60 * MINUTE + 12_400, nextRunAt: now + 13 * 60 * MINUTE, lastDurationMs: 12_400, lastErrorCode: '' },
      ],
      configHistory: [
        { id: 'cfg-3', revision: 3, actor: 'admin', summary: '调整探针和请求超时策略', changedFields: ['probeIntervalMs', 'firstOutputTimeoutMs', 'requestTimeoutMs'], status: 'applied', createdAt: now - 42 * MINUTE },
        { id: 'cfg-2', revision: 2, actor: 'admin', summary: '更新故障冷却窗口', changedFields: ['failureWindowMs', 'cooldownMs'], status: 'superseded', createdAt: now - 2 * 24 * 60 * MINUTE },
        { id: 'cfg-1', revision: 1, actor: 'bootstrap', summary: '初始化网关运行配置', changedFields: ['*'], status: 'superseded', createdAt: now - 40 * 24 * 60 * MINUTE },
      ],
    },
    nextIds: { site: 4, endpoint: 41, credential: 411, providerModel: 4001, profile: 3, publishedModel: 504, downstreamKey: 3, balance: 10, usage: 20, attempt: 100, ledger: 100 },
    failNextTargetIds: [],
  };
}

// Compile-time guard: fixture attempts intentionally mirror the public detail contract.
const _attemptContract: GatewayLogAttempt | null = null;
void _attemptContract;
