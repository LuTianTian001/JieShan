import { demo } from './demo';
import type {
  AccountAdapter,
  AccountUsageRange,
  ConfigureUpstreamAccountInput,
  CreateKeyInput,
  CreateRouteInput,
  CreateUpstreamInput,
  CreateV2PublishedModelInput,
  CreateV2RouteTargetInput,
  CreateV2CredentialInput,
  CreateV2EndpointInput,
  CreateV2SiteInput,
  DashboardSummary,
  DownstreamKey,
  GatewaySettings,
  ModelDiscovery,
  MonitorMatrix,
  RequestLog,
  RequestLogDetail,
  RequestLogCursor,
  RequestLogFilter,
  RequestLogListItem,
  RequestLogPage,
  RequestLogSummary,
  Route,
  RoutingProfile,
  RoutingProfileModelRoute,
  Upstream,
  UpstreamAccount,
  UpstreamUsagePage,
  UpdateKeyInput,
  UpdateRouteInput,
  UpdateUpstreamInput,
  UpdateV2CredentialInput,
  UpdateV2EndpointInput,
  UpdateV2PublishedModelInput,
  UpdateV2RouteTargetInput,
  UpdateV2SiteInput,
  User,
  V2Credential,
  V2DiscoveryStrategy,
  V2Endpoint,
  V2ModelDiscovery,
  V2MonitorMatrix,
  V2PublishedModel,
  V2ProbeAttempt,
  V2ProbeRun,
  V2RouteSiteTarget,
  V2Site,
  V2SiteDetail,
  V2SiteModel,
  V2SiteSummary,
} from './types';

const API_PREFIX = import.meta.env.VITE_API_PREFIX || '/api/v1';
const API_V2_PREFIX = import.meta.env.VITE_API_V2_PREFIX || '/api/v2';
const DEMO_KEY = 'jieshan.demo.enabled';
export const AUTH_EXPIRED_EVENT = 'jieshan:auth-expired';

export class ApiError extends Error {
  constructor(message: string, readonly status: number, readonly body?: unknown) {
    super(message);
  }
}

export function isDemoMode(): boolean {
  return localStorage.getItem(DEMO_KEY) === '1';
}

export function canUseDemoMode(): boolean {
  return import.meta.env.DEV || import.meta.env.VITE_ENABLE_DEMO === 'true';
}

export function enableDemoMode(): void {
  if (!canUseDemoMode()) throw new Error('当前环境未启用预览模式');
  localStorage.setItem(DEMO_KEY, '1');
}

export function disableDemoMode(): void {
  localStorage.removeItem(DEMO_KEY);
}

async function requestFrom<T>(prefix: string, path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${prefix}${path}`, {
    ...init,
    credentials: 'include',
    headers: {
      Accept: 'application/json',
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
  });

  if (!response.ok) {
    let message = `请求失败 (${response.status})`;
    let body: unknown;
    try {
      body = await response.json();
      if (body && typeof body === 'object') {
        const detail = body as { message?: string; error?: string };
        message = detail.message || detail.error || message;
      }
    } catch {
      // Keep the status-based message when the server did not return JSON.
    }
    if (response.status === 401 && !isDemoMode()) {
      window.dispatchEvent(new Event(AUTH_EXPIRED_EVENT));
    }
    throw new ApiError(message, response.status, body);
  }

  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return requestFrom<T>(API_PREFIX, path, init);
}

function requestV2<T>(path: string, init?: RequestInit): Promise<T> {
  return requestFrom<T>(API_V2_PREFIX, path, init);
}

function appendRequestLogFilters(query: URLSearchParams, filters: RequestLogFilter): void {
  if (filters.status) query.set('status', filters.status);
  if (filters.model) query.set('model', filters.model);
  if (filters.siteId) query.set('siteId', String(filters.siteId));
  if (filters.upstreamId) query.set('upstream', String(filters.upstreamId));
  if (filters.downstreamKeyId) query.set('downstreamKey', String(filters.downstreamKeyId));
  if (filters.stream !== undefined) query.set('stream', String(filters.stream));
  if (filters.switched !== undefined) query.set('switched', String(filters.switched));
}

function demoLogMatches(log: RequestLog, filters: RequestLogFilter): boolean {
  const model = filters.model?.trim().toLowerCase();
  if (filters.status && log.status !== filters.status) return false;
  if (model && log.requestedModel.toLowerCase() !== model && log.actualModel.toLowerCase() !== model) return false;
  if (filters.downstreamKeyId && log.downstreamKeyId !== filters.downstreamKeyId) return false;
  if (filters.stream !== undefined && Boolean(log.stream) !== filters.stream) return false;
  if (filters.switched !== undefined && (log.switchCount > 0) !== filters.switched) return false;
  if (filters.siteId && !log.attempts?.some((attempt) => attempt.siteId === filters.siteId)) return false;
  if (filters.upstreamId && !log.attempts?.some((attempt) => attempt.upstreamId === filters.upstreamId)) return false;
  return true;
}

function demoLogListItem(log: RequestLog): RequestLogListItem {
  const selectedAttempt = [...(log.attempts ?? [])].sort((left, right) => {
    const leftRank = left.state === 'success' ? 0 : 1;
    const rightRank = right.state === 'success' ? 0 : 1;
    return leftRank - rightRank || right.sequence - left.sequence;
  })[0];
  return {
    id: log.id,
    routingGeneration: 'legacy',
    surface: 'chat_completions',
    downstreamKeyId: log.downstreamKeyId,
    keyName: log.keyName,
    routeId: log.routeId,
    routeRevision: log.routeRevision,
    routingProfileName: '默认路由',
    actualUpstreamId: selectedAttempt?.upstreamId,
    actualUpstreamName: selectedAttempt?.upstreamName,
    actualSiteId: selectedAttempt?.siteId,
    actualSiteName: selectedAttempt?.siteName,
    actualEndpointId: selectedAttempt?.endpointId,
    actualEndpointName: selectedAttempt?.endpointName,
    actualCredentialId: selectedAttempt?.credentialId,
    actualCredentialName: selectedAttempt?.credentialName,
    requestedModel: log.requestedModel,
    actualModel: log.actualModel,
    reasoningEffort: log.reasoningEffort,
    thinkingBudget: log.thinkingBudget,
    status: log.status,
    httpStatus: log.httpStatus,
    stream: Boolean(log.stream),
    firstTokenMs: log.ttftMs,
    durationMs: log.durationMs,
    inputTokens: log.inputTokens,
    cacheReadTokens: log.cacheTokens,
    outputTokens: log.outputTokens,
    reasoningTokens: log.reasoningTokens,
    costMicroUsd: Math.round(log.costUsd * 1_000_000),
    priceSnapshot: log.priceSnapshot ?? undefined,
    switchCount: log.switchCount,
    errorMessage: log.errorMessage ?? undefined,
    startedAt: new Date(log.startedAt).getTime(),
    finishedAt: log.finishedAt
      ? new Date(log.finishedAt).getTime()
      : log.durationMs == null ? undefined : new Date(log.startedAt).getTime() + log.durationMs,
  };
}

function demoLogDetail(log: RequestLog): RequestLogDetail {
  return {
    ...demoLogListItem(log),
    attempts: (log.attempts ?? []).map((attempt) => ({
      id: attempt.id,
      requestId: log.id,
      attemptIndex: Math.max(0, attempt.sequence - 1),
      routingGeneration: attempt.routingGeneration ?? 'legacy',
      targetId: attempt.targetId,
      routeSiteTargetId: attempt.routeSiteTargetId,
      upstreamId: attempt.upstreamId,
      upstreamName: attempt.upstreamName,
      siteId: attempt.siteId,
      siteName: attempt.siteName,
      endpointId: attempt.endpointId,
      endpointName: attempt.endpointName,
      inferenceCredentialId: attempt.credentialId,
      credentialName: attempt.credentialName,
      siteModelId: attempt.siteModelId,
      upstreamModel: attempt.model,
      status: attempt.state,
      httpStatus: attempt.statusCode,
      switchReason: attempt.switchReason,
      errorClass: attempt.errorClass,
      errorMessage: attempt.error,
      latencyMs: attempt.durationMs,
      firstTokenMs: attempt.ttftMs,
      createdAt: new Date(attempt.startedAt).getTime(),
    })),
  };
}

function demoFilteredLogs(filters: RequestLogFilter): RequestLogListItem[] {
  return demo.logs()
    .filter((log) => demoLogMatches(log, filters))
    .map(demoLogListItem)
    .sort((left, right) => right.startedAt - left.startedAt || right.id.localeCompare(left.id));
}

function nearestRank(values: number[], percentile: number): number | null {
  if (values.length === 0) return null;
  const ordered = [...values].sort((left, right) => left - right);
  return ordered[Math.ceil(ordered.length * percentile) - 1] ?? null;
}

export const api = {
  async me(): Promise<User> {
    if (isDemoMode()) return demo.user();
    const result = await request<{ user: User } | User>('/me');
    return 'user' in result ? result.user : result;
  },
  async login(password: string): Promise<User> {
    if (isDemoMode()) return demo.user();
    const result = await request<{ user: User }>('/auth/login', { method: 'POST', body: JSON.stringify({ password }) });
    return result.user;
  },
  async logout(): Promise<void> {
    if (isDemoMode()) {
      disableDemoMode();
      return;
    }
    await request<void>('/auth/logout', { method: 'POST' });
  },
  async dashboard(): Promise<DashboardSummary> {
    if (isDemoMode()) return demo.dashboard();
    return request<DashboardSummary>('/dashboard');
  },
  async monitor(): Promise<MonitorMatrix> {
    if (isDemoMode()) return demo.monitor();
    return request<MonitorMatrix>('/monitor/matrix');
  },
  async upstreams(): Promise<Upstream[]> {
    if (isDemoMode()) return demo.upstreams();
    const result = await request<{ items: Upstream[] }>('/upstreams');
    return result.items;
  },
  async upstreamInventorySource(): Promise<unknown[]> {
    if (isDemoMode()) return demo.upstreams();
    const result = await request<{ items?: unknown[]; sites?: unknown[] }>('/upstreams');
    return result.sites ?? result.items ?? [];
  },
  async v2Sites(): Promise<V2SiteSummary[]> {
    if (isDemoMode()) return [];
    const result = await requestV2<{ items: V2SiteSummary[] }>('/sites');
    return result.items ?? [];
  },
  async v2Site(id: number): Promise<V2SiteDetail> {
    const result = await requestV2<{ item: V2SiteDetail }>(`/sites/${id}`);
    return result.item;
  },
  async createV2Site(input: CreateV2SiteInput): Promise<V2Site> {
    const result = await requestV2<{ item: V2Site }>('/sites', { method: 'POST', body: JSON.stringify(input) });
    return result.item;
  },
  async updateV2Site(id: number, patch: UpdateV2SiteInput): Promise<V2Site> {
    const result = await requestV2<{ item: V2Site }>(`/sites/${id}`, { method: 'PATCH', body: JSON.stringify(patch) });
    return result.item;
  },
  async deleteV2Site(id: number, revision?: number): Promise<void> {
    const query = revision ? `?revision=${revision}` : '';
    await requestV2<void>(`/sites/${id}${query}`, { method: 'DELETE' });
  },
  async siteAccount(id: number): Promise<UpstreamAccount> {
    const result = await requestV2<{ account: UpstreamAccount }>(`/sites/${id}/account`);
    return result.account;
  },
  async configureSiteAccount(id: number, input: ConfigureUpstreamAccountInput): Promise<UpstreamAccount> {
    const result = await requestV2<{ account: UpstreamAccount }>(`/sites/${id}/account`, { method: 'PUT', body: JSON.stringify(input) });
    return result.account;
  },
  async deleteSiteAccount(id: number): Promise<void> {
    await requestV2<void>(`/sites/${id}/account`, { method: 'DELETE' });
  },
  async refreshSiteAccount(id: number): Promise<UpstreamAccount> {
    const result = await requestV2<{ account: UpstreamAccount }>(`/sites/${id}/account/refresh`, { method: 'POST' });
    return result.account;
  },
  async siteAccountUsage(id: number, range: AccountUsageRange, limit = 50, beforeId?: string): Promise<UpstreamUsagePage> {
    const query = new URLSearchParams({ range, limit: String(limit) });
    if (beforeId) query.set('beforeId', beforeId);
    return requestV2<UpstreamUsagePage>(`/sites/${id}/account/usage?${query.toString()}`);
  },
  async v2Endpoints(siteId: number): Promise<V2Endpoint[]> {
    const result = await requestV2<{ items: V2Endpoint[] }>(`/sites/${siteId}/endpoints`);
    return result.items ?? [];
  },
  async createV2Endpoint(siteId: number, input: CreateV2EndpointInput): Promise<V2Endpoint> {
    const result = await requestV2<{ item: V2Endpoint }>(`/sites/${siteId}/endpoints`, { method: 'POST', body: JSON.stringify(input) });
    return result.item;
  },
  async updateV2Endpoint(id: number, patch: UpdateV2EndpointInput): Promise<V2Endpoint> {
    const result = await requestV2<{ item: V2Endpoint }>(`/endpoints/${id}`, { method: 'PATCH', body: JSON.stringify(patch) });
    return result.item;
  },
  async deleteV2Endpoint(id: number, revision?: number): Promise<void> {
    const query = revision ? `?revision=${revision}` : '';
    await requestV2<void>(`/endpoints/${id}${query}`, { method: 'DELETE' });
  },
  async v2Credentials(siteId: number): Promise<V2Credential[]> {
    const result = await requestV2<{ items: V2Credential[] }>(`/sites/${siteId}/credentials`);
    return result.items ?? [];
  },
  async createV2Credential(siteId: number, input: CreateV2CredentialInput): Promise<V2Credential> {
    const result = await requestV2<{ item: V2Credential }>(`/sites/${siteId}/credentials`, { method: 'POST', body: JSON.stringify(input) });
    return result.item;
  },
  async updateV2Credential(id: number, patch: UpdateV2CredentialInput): Promise<V2Credential> {
    const result = await requestV2<{ item: V2Credential }>(`/credentials/${id}`, { method: 'PATCH', body: JSON.stringify(patch) });
    return result.item;
  },
  async deleteV2Credential(id: number, revision?: number): Promise<void> {
    const query = revision ? `?revision=${revision}` : '';
    await requestV2<void>(`/credentials/${id}${query}`, { method: 'DELETE' });
  },
  async v2SiteModels(siteId: number): Promise<V2SiteModel[]> {
    const result = await requestV2<{ items: V2SiteModel[] }>(`/sites/${siteId}/models`);
    return result.items ?? [];
  },
  async discoverV2Models(siteId: number, input: { endpointId: number; credentialId?: number; strategy: V2DiscoveryStrategy }): Promise<V2ModelDiscovery> {
    return requestV2<V2ModelDiscovery>(`/sites/${siteId}/model-discoveries`, { method: 'POST', body: JSON.stringify(input) });
  },
  async v2PublishedModels(): Promise<V2PublishedModel[]> {
    if (isDemoMode()) return [];
    const result = await requestV2<{ items: V2PublishedModel[] }>('/published-models');
    return result.items ?? [];
  },
  async v2PublishedModel(id: number): Promise<V2PublishedModel> {
    const result = await requestV2<{ item: V2PublishedModel }>(`/published-models/${id}`);
    return result.item;
  },
  async createV2PublishedModel(input: CreateV2PublishedModelInput): Promise<V2PublishedModel> {
    const result = await requestV2<{ item: V2PublishedModel }>('/published-models', { method: 'POST', body: JSON.stringify(input) });
    return result.item;
  },
  async updateV2PublishedModel(id: number, patch: UpdateV2PublishedModelInput): Promise<V2PublishedModel> {
    const result = await requestV2<{ item: V2PublishedModel }>(`/published-models/${id}`, { method: 'PATCH', body: JSON.stringify(patch) });
    return result.item;
  },
  async deleteV2PublishedModel(id: number, revision?: number): Promise<void> {
    const query = revision ? `?revision=${revision}` : '';
    await requestV2<void>(`/published-models/${id}${query}`, { method: 'DELETE' });
  },
  async createV2RouteTarget(publishedModelId: number, input: CreateV2RouteTargetInput): Promise<V2RouteSiteTarget> {
    const result = await requestV2<{ item: V2RouteSiteTarget }>(`/published-models/${publishedModelId}/targets`, { method: 'POST', body: JSON.stringify(input) });
    return result.item;
  },
  async updateV2RouteTarget(id: number, patch: UpdateV2RouteTargetInput): Promise<V2RouteSiteTarget> {
    const result = await requestV2<{ item: V2RouteSiteTarget }>(`/route-targets/${id}`, { method: 'PATCH', body: JSON.stringify(patch) });
    return result.item;
  },
  async deleteV2RouteTarget(id: number, targetRevision?: number, publishedRevision?: number): Promise<void> {
    const query = new URLSearchParams();
    if (targetRevision) query.set('targetRevision', String(targetRevision));
    if (publishedRevision) query.set('publishedRevision', String(publishedRevision));
    const suffix = query.size ? `?${query.toString()}` : '';
    await requestV2<void>(`/route-targets/${id}${suffix}`, { method: 'DELETE' });
  },
  async reorderV2RouteTargets(publishedModelId: number, ids: number[], revision?: number): Promise<V2PublishedModel> {
    const result = await requestV2<{ item: V2PublishedModel }>(`/published-models/${publishedModelId}/targets/order`, { method: 'PUT', body: JSON.stringify({ ids, revision }) });
    return result.item;
  },
  async routingProfiles(): Promise<RoutingProfile[]> {
    if (isDemoMode()) return [];
    const result = await requestV2<{ items: RoutingProfile[] }>('/routing-profiles');
    return result.items ?? [];
  },
  async createRoutingProfile(name: string): Promise<RoutingProfile> {
    const result = await requestV2<{ item: RoutingProfile }>('/routing-profiles', { method: 'POST', body: JSON.stringify({ name }) });
    return result.item;
  },
  async updateRoutingProfile(id: number, name: string, revision: number): Promise<RoutingProfile> {
    const result = await requestV2<{ item: RoutingProfile }>(`/routing-profiles/${id}`, { method: 'PATCH', body: JSON.stringify({ name, revision }) });
    return result.item;
  },
  async deleteRoutingProfile(id: number, revision: number): Promise<void> {
    await requestV2<void>(`/routing-profiles/${id}?revision=${revision}`, { method: 'DELETE' });
  },
  async routingProfileModel(profileId: number, publishedModelId: number): Promise<RoutingProfileModelRoute> {
    const result = await requestV2<{ item: RoutingProfileModelRoute }>(`/routing-profiles/${profileId}/models/${publishedModelId}`);
    return result.item;
  },
  async setRoutingProfileModel(profileId: number, publishedModelId: number, targetIds: number[], revision: number): Promise<RoutingProfileModelRoute> {
    const result = await requestV2<{ item: RoutingProfileModelRoute }>(`/routing-profiles/${profileId}/models/${publishedModelId}`, { method: 'PUT', body: JSON.stringify({ targetIds, revision }) });
    return result.item;
  },
  async clearRoutingProfileModel(profileId: number, publishedModelId: number, revision: number): Promise<RoutingProfileModelRoute> {
    const result = await requestV2<{ item: RoutingProfileModelRoute }>(`/routing-profiles/${profileId}/models/${publishedModelId}?revision=${revision}`, { method: 'DELETE' });
    return result.item;
  },
  async probeV2PublishedModel(publishedModelId: number, targetId?: number): Promise<{ run: V2ProbeRun; attempts: V2ProbeAttempt[] }> {
    return requestV2<{ run: V2ProbeRun; attempts: V2ProbeAttempt[] }>(`/published-models/${publishedModelId}/probe`, { method: 'POST', body: JSON.stringify(targetId ? { targetId } : {}) });
  },
  async v2ProbeRuns(publishedModelId: number, limit = 50): Promise<V2ProbeRun[]> {
    const result = await requestV2<{ items: V2ProbeRun[] }>(`/published-models/${publishedModelId}/probe-runs?limit=${limit}`);
    return result.items ?? [];
  },
  async v2ProbeRun(id: string): Promise<V2ProbeRun> {
    const result = await requestV2<{ item: V2ProbeRun }>(`/probe-runs/${encodeURIComponent(id)}`);
    return result.item;
  },
  async v2ProbeAttempts(id: string): Promise<V2ProbeAttempt[]> {
    const result = await requestV2<{ items: V2ProbeAttempt[] }>(`/probe-runs/${encodeURIComponent(id)}/attempts`);
    return result.items ?? [];
  },
  async v2MonitorMatrix(): Promise<V2MonitorMatrix> {
    if (isDemoMode()) return { generatedAt: Date.now(), models: [] };
    return requestV2<V2MonitorMatrix>('/monitor/matrix');
  },
  async accountAdapters(): Promise<AccountAdapter[]> {
    if (isDemoMode()) return demo.accountAdapters();
    const result = await request<{ items: AccountAdapter[] }>('/account-adapters');
    return result.items;
  },
  async upstreamAccount(id: number): Promise<UpstreamAccount> {
    if (isDemoMode()) return demo.upstreamAccount(id);
    const result = await request<{ account: UpstreamAccount }>(`/upstreams/${id}/account`);
    return result.account;
  },
  async configureUpstreamAccount(id: number, input: ConfigureUpstreamAccountInput): Promise<UpstreamAccount> {
    if (isDemoMode()) return demo.configureUpstreamAccount(id, input);
    const result = await request<{ account: UpstreamAccount }>(`/upstreams/${id}/account`, { method: 'PUT', body: JSON.stringify(input) });
    return result.account;
  },
  async deleteUpstreamAccount(id: number): Promise<void> {
    if (isDemoMode()) return demo.deleteUpstreamAccount(id);
    await request<void>(`/upstreams/${id}/account`, { method: 'DELETE' });
  },
  async refreshUpstreamAccount(id: number): Promise<UpstreamAccount> {
    if (isDemoMode()) return demo.refreshUpstreamAccount(id);
    const result = await request<{ account: UpstreamAccount }>(`/upstreams/${id}/account/refresh`, { method: 'POST' });
    return result.account;
  },
  async upstreamAccountUsage(id: number, range: AccountUsageRange, limit = 50, beforeId?: string): Promise<UpstreamUsagePage> {
    if (isDemoMode()) return demo.upstreamAccountUsage(id, range, limit);
    const query = new URLSearchParams({ range, limit: String(limit) });
    if (beforeId) query.set('beforeId', beforeId);
    return request<UpstreamUsagePage>(`/upstreams/${id}/account/usage?${query.toString()}`);
  },
  async createUpstream(input: CreateUpstreamInput): Promise<Upstream> {
    if (isDemoMode()) return demo.createUpstream(input);
    const result = await request<{ item: Upstream }>('/upstreams', { method: 'POST', body: JSON.stringify(input) });
    return result.item;
  },
  async updateUpstream(id: number, patch: UpdateUpstreamInput): Promise<Upstream> {
    if (isDemoMode()) return demo.updateUpstream(id, patch);
    const result = await request<{ item: Upstream }>(`/upstreams/${id}`, { method: 'PATCH', body: JSON.stringify(patch) });
    return result.item;
  },
  async deleteUpstream(id: number): Promise<void> {
    if (isDemoMode()) return demo.deleteUpstream(id);
    await request<void>(`/upstreams/${id}`, { method: 'DELETE' });
  },
  async testUpstream(id: number): Promise<Upstream> {
    if (isDemoMode()) return demo.testUpstream(id);
    const result = await request<{ item: Upstream }>(`/upstreams/${id}/test`, { method: 'POST' });
    return result.item;
  },
  async discoverModels(id: number): Promise<ModelDiscovery> {
    if (isDemoMode()) return demo.discoverModels(id);
    return request<ModelDiscovery>(`/upstreams/${id}/models/discover`, { method: 'POST' });
  },
  async applyModels(id: number, discovery: ModelDiscovery): Promise<Upstream> {
    if (isDemoMode()) return demo.applyModels(id, discovery);
    const result = await request<{ item: Upstream }>(`/upstreams/${id}/models/apply`, { method: 'POST', body: JSON.stringify({ discovery }) });
    return result.item;
  },
  async routes(): Promise<Route[]> {
    if (isDemoMode()) return demo.routes();
    const result = await request<{ items: Route[] }>('/routes');
    return result.items;
  },
  async createRoute(input: CreateRouteInput): Promise<Route> {
    if (isDemoMode()) return demo.createRoute(input);
    const result = await request<{ item: Route }>('/routes', { method: 'POST', body: JSON.stringify(input) });
    return result.item;
  },
  async updateRoute(id: number, patch: UpdateRouteInput): Promise<Route> {
    if (isDemoMode()) return demo.updateRoute(id, patch);
    const result = await request<{ item: Route }>(`/routes/${id}`, { method: 'PATCH', body: JSON.stringify(patch) });
    return result.item;
  },
  async deleteRoute(id: number): Promise<void> {
    if (isDemoMode()) return demo.deleteRoute(id);
    await request<void>(`/routes/${id}`, { method: 'DELETE' });
  },
  async reorderRoute(id: number, targetIds: number[]): Promise<Route> {
    if (isDemoMode()) return demo.reorderRoute(id, targetIds);
    const result = await request<{ item: Route }>(`/routes/${id}/targets/order`, { method: 'PUT', body: JSON.stringify({ targetIds }) });
    return result.item;
  },
  async probeRoute(id: number, targetId?: number): Promise<Route> {
    if (isDemoMode()) return demo.probeRoute(id, targetId);
    const result = await request<{ item: Route }>(`/routes/${id}/probe`, { method: 'POST', body: JSON.stringify({ targetId }) });
    return result.item;
  },
  async keys(): Promise<DownstreamKey[]> {
    if (isDemoMode()) return demo.keys();
    const result = await request<{ items: DownstreamKey[] }>('/keys');
    return result.items;
  },
  async createKey(input: CreateKeyInput): Promise<{ item: DownstreamKey; secret: string }> {
    if (isDemoMode()) return demo.createKey(input);
    return request<{ item: DownstreamKey; secret: string }>('/keys', { method: 'POST', body: JSON.stringify(input) });
  },
  async updateKey(id: number, patch: UpdateKeyInput): Promise<DownstreamKey> {
    if (isDemoMode()) return demo.updateKey(id, patch);
    const result = await request<{ item: DownstreamKey }>(`/keys/${id}`, { method: 'PATCH', body: JSON.stringify(patch) });
    return result.item;
  },
  async deleteKey(id: number): Promise<void> {
    if (isDemoMode()) return demo.deleteKey(id);
    await request<void>(`/keys/${id}`, { method: 'DELETE' });
  },
  async logs(): Promise<RequestLog[]> {
    if (isDemoMode()) return demo.logs();
    const result = await request<{ items: RequestLog[] }>('/logs/requests');
    return result.items;
  },
  async requestLogs(filters: RequestLogFilter = {}, cursor: RequestLogCursor | null = null, limit = 50): Promise<RequestLogPage> {
    if (isDemoMode()) {
      let items = demoFilteredLogs(filters);
      if (cursor) {
        items = items.filter((item) => item.startedAt < cursor.beforeTime || (item.startedAt === cursor.beforeTime && item.id < cursor.beforeId));
      }
      const hasMore = items.length > limit;
      const pageItems = items.slice(0, limit);
      const last = pageItems.at(-1);
      return {
        items: pageItems,
        hasMore,
        nextCursor: hasMore && last ? { beforeTime: last.startedAt, beforeId: last.id } : null,
      };
    }
    const query = new URLSearchParams({ limit: String(limit) });
    appendRequestLogFilters(query, filters);
    if (cursor) {
      query.set('beforeTime', String(cursor.beforeTime));
      query.set('beforeId', cursor.beforeId);
    }
    return requestV2<RequestLogPage>(`/request-logs?${query.toString()}`);
  },
  async requestLogSummary(filters: RequestLogFilter = {}): Promise<RequestLogSummary> {
    if (isDemoMode()) {
      const items = demoFilteredLogs(filters);
      const successCount = items.filter((item) => item.status === 'success').length;
      const switchedCount = items.filter((item) => item.switchCount > 0).length;
      const ttft = items.flatMap((item) => item.firstTokenMs == null ? [] : [item.firstTokenMs]);
      return {
        count: items.length,
        successRate: items.length ? successCount / items.length * 100 : 0,
        costMicroUsd: items.reduce((total, item) => total + item.costMicroUsd, 0),
        switchRate: items.length ? switchedCount / items.length * 100 : 0,
        p50TtftMs: nearestRank(ttft, 0.5),
        p95TtftMs: nearestRank(ttft, 0.95),
      };
    }
    const query = new URLSearchParams();
    appendRequestLogFilters(query, filters);
    const suffix = query.size ? `?${query.toString()}` : '';
    return requestV2<RequestLogSummary>(`/request-logs/summary${suffix}`);
  },
  async log(id: string): Promise<RequestLogDetail> {
    if (isDemoMode()) return demoLogDetail(demo.log(id));
    return requestV2<RequestLogDetail>(`/request-logs/${encodeURIComponent(id)}`);
  },
  async settings(): Promise<GatewaySettings> {
    if (isDemoMode()) return demo.settings();
    return request<GatewaySettings>('/settings');
  },
  async updateSettings(patch: Partial<GatewaySettings>): Promise<GatewaySettings> {
    if (isDemoMode()) return demo.updateSettings(patch);
    return request<GatewaySettings>('/settings', { method: 'PATCH', body: JSON.stringify(patch) });
  },
};
