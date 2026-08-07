import type {
  AuthScheme,
  CatalogState,
  CreateDownstreamKeyInput,
  CreateRouteInput,
  CreateRoutingProfileInput,
  CreateSiteInput,
  DiscoveredModel,
  DownstreamKey,
  EndpointInput,
  GatewayLog,
  GatewayLogAttempt,
  GatewayRouteCandidate,
  GatewayMeteringStatus,
  GatewayLogPage,
  GatewayQuotaLedgerEvent,
  GatewayLogSummary,
  GatewaySettings,
  InferenceSurface,
  IssuedDownstreamKey,
  ModelRoute,
  ModelTarget,
  MonitorSetting,
  MonitorSnapshot,
  MonitorTargetHistory,
  PriceCatalog,
  PriceCatalogImportResult,
  PriceCatalogList,
  PriceCatalogPreview,
  ProviderModel,
  RoutingProfile,
  Site,
  SiteAccountConnection,
  SiteCredential,
  SiteEndpoint,
  SiteUsagePage,
  UpdateDownstreamKeyInput,
  UpdateRouteInput,
  UpdateSiteInput,
  User,
  WireProtocol,
  CredentialBinding,
  SiteBalance,
} from './types';

const SESSION_PREFIX = import.meta.env.VITE_SESSION_API_PREFIX || '/api/vnext/auth';
const INVENTORY_PREFIX = '/api/vnext/inventory';
const KEYS_PREFIX = '/api/vnext/downstream-keys';
const ROUTING_PREFIX = '/api/vnext/routing-profiles';
const SITE_ACCOUNTS_PREFIX = '/api/vnext/site-accounts';
const PRICING_PREFIX = '/api/vnext/pricing';
const MONITOR_PREFIX = import.meta.env.VITE_JIESHAN_MONITOR_PREFIX || '/api/vnext/monitor';
const LOGS_PREFIX = import.meta.env.VITE_JIESHAN_LOGS_PREFIX || '/api/vnext/request-logs';
const SETTINGS_PREFIX = '/api/vnext/settings';
const CSRF_COOKIE_NAME = 'jieshan_admin_csrf';
const CSRF_HEADER_NAME = 'X-CSRF-Token';

export const AUTH_EXPIRED_EVENT = 'jieshan:auth-expired';

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code: string,
    readonly body?: unknown,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

export class ApiUnavailableError extends ApiError {
  constructor(message: string, status: number, code: string, body?: unknown) {
    super(message, status, code, body);
    this.name = 'ApiUnavailableError';
  }
}

interface ErrorEnvelope {
  error?: string | { code?: string; message?: string };
  code?: string;
  message?: string;
}

function errorDetail(body: unknown, status: number): { code: string; message: string } {
  const fallback = `请求失败 (${status})`;
  if (!body || typeof body !== 'object') return { code: 'request_failed', message: fallback };
  const envelope = body as ErrorEnvelope;
  if (typeof envelope.error === 'object' && envelope.error) {
    return {
      code: envelope.error.code || envelope.code || 'request_failed',
      message: envelope.error.message || envelope.message || fallback,
    };
  }
  return {
    code: envelope.code || 'request_failed',
    message: envelope.message || (typeof envelope.error === 'string' ? envelope.error : fallback),
  };
}

async function requestJSON<T>(path: string, init: RequestInit = {}): Promise<T> {
  const method = (init.method || 'GET').toUpperCase();
  const headers = new Headers(init.headers);
  headers.set('Accept', 'application/json');
  if (init.body) headers.set('Content-Type', 'application/json');
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) {
    const csrf = document.cookie
      .split(';')
      .map((item) => item.trim())
      .find((item) => item.startsWith(`${CSRF_COOKIE_NAME}=`))
      ?.slice(CSRF_COOKIE_NAME.length + 1);
    if (csrf) headers.set(CSRF_HEADER_NAME, decodeURIComponent(csrf));
  }
  const response = await fetch(path, {
    ...init,
    method,
    credentials: 'include',
    headers,
  });

  if (!response.ok) {
    let body: unknown;
    try {
      body = await response.json();
    } catch {
      body = undefined;
    }
    const detail = errorDetail(body, response.status);
    if (response.status === 401 && detail.code === 'unauthenticated') {
      window.dispatchEvent(new Event(AUTH_EXPIRED_EVENT));
    }
    const Unavailable = [404, 501, 503].includes(response.status) ? ApiUnavailableError : ApiError;
    throw new Unavailable(detail.message, response.status, detail.code, body);
  }

  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

function jsonInit(method: string, value: unknown, revision?: number): RequestInit {
  return {
    method,
    body: JSON.stringify(value),
    headers: revision === undefined ? undefined : { 'If-Match': `"${revision}"` },
  };
}

function revisionInit(method: string, revision: number): RequestInit {
  return { method, headers: { 'If-Match': `"${revision}"` } };
}

function createInit(value: unknown): RequestInit {
  return {
    method: 'POST',
    body: JSON.stringify(value),
    headers: { 'If-Match': '*' },
  };
}

function inventory(path: string): string {
  return `${INVENTORY_PREFIX}${path}`;
}

function keyPath(path = ''): string {
  return `${KEYS_PREFIX}${path}`;
}

export interface ModelTargetFilter {
  query?: string;
  protocol?: WireProtocol | '';
  surface?: InferenceSurface | '';
  siteId?: number;
  enabled?: boolean;
}

export interface SiteUsageFilter {
  cursor?: string;
  from?: number;
  to?: number;
  limit?: number;
  model?: string;
  status?: string;
  apiKey?: string;
  requestId?: string;
  search?: string;
}

export interface AccountSecretInput {
  authorization?: string;
  accessToken?: string;
  refreshToken?: string;
  cookie?: string;
  expiresAt?: number;
}

export interface AccountConnectionInput {
  adapterKind: string;
  origin: string;
  enabled?: boolean;
  secrets: AccountSecretInput;
}

export interface ModelDiscoveryPreviewInput {
  baseUrl: string;
  wireProtocol: WireProtocol;
  surface: InferenceSurface;
  authScheme: AuthScheme;
  apiKey: string;
}

export interface ModelDiscoveryPreviewResult {
  models: string[];
  complete: boolean;
}

export interface GatewayLogFilter {
  cursor?: string;
  limit?: number;
  query?: string;
  status?: string;
  model?: string;
  keyId?: number;
  surface?: InferenceSurface;
  siteId?: number;
  from?: number;
  to?: number;
}

type GatewayLogSummaryFilter = Omit<GatewayLogFilter, 'cursor' | 'limit'>;

interface AuthStatusResponse {
  authenticated: boolean;
  username?: string;
  expires_at?: number;
}

interface RequestLogResponse {
  id: string;
  downstreamKeyId: number;
  downstreamKeyName: string;
  publishedModelId: number;
  publishedModelRevision: number;
  effectiveRoutingProfileId: number;
  effectiveRoutingProfileName: string;
  sourceRoutingProfileId: number;
  sourceRoutingProfileName: string;
  routeRevision: number;
  publicModel: string;
  apiSurface: InferenceSurface;
  reasoningEffort: string;
  thinkingBudgetTokens: number | null;
  stream: boolean;
  priceCatalogVersion: string;
  priceSku: string;
  reservationNanoUsd: number;
  billingMultiplierBPS: number;
  status: string;
  meteringStatus: GatewayMeteringStatus;
  meteringErrorCode: string;
  finalAttemptIndex: number | null;
  httpStatus: number | null;
  firstOutputMs: number | null;
  totalDurationMs: number | null;
  inputTokens: number | null;
  outputTokens: number | null;
  cacheReadTokens: number | null;
  cacheWriteTokens: number | null;
  cacheWrite5mTokens: number | null;
  cacheWrite1hTokens: number | null;
  reasoningTokens: number | null;
  officialCostNanoUsd: number;
  chargedNanoUsd: number;
  quotaCapped: boolean;
  errorCode: string;
  startedAt: number;
  finishedAt: number | null;
  finalAttempt: RequestAttemptResponse | null;
}

type DownstreamKeyResponse = Omit<DownstreamKey, 'billingMultiplier'> & {
  billingMultiplierBPS: number;
};

type DownstreamKeyRequest = Omit<CreateDownstreamKeyInput, 'billingMultiplier'> & {
  billingMultiplierBPS?: number;
};

type DownstreamKeyUpdateRequest = Omit<UpdateDownstreamKeyInput, 'billingMultiplier'> & {
  billingMultiplierBPS?: number;
};

interface IssuedDownstreamKeyResponse {
  item: DownstreamKeyResponse;
  secret: string;
}

function billingMultiplierFromBPS(value: number): number {
  return value / 10_000;
}

function billingMultiplierToBPS(value: number | undefined): number | undefined {
  return value === undefined ? undefined : Math.round(value * 10_000);
}

function downstreamKey(item: DownstreamKeyResponse): DownstreamKey {
  const { billingMultiplierBPS, ...rest } = item;
  return { ...rest, billingMultiplier: billingMultiplierFromBPS(billingMultiplierBPS) };
}

function downstreamKeyRequest(input: CreateDownstreamKeyInput): DownstreamKeyRequest {
  const { billingMultiplier, ...rest } = input;
  return { ...rest, billingMultiplierBPS: billingMultiplierToBPS(billingMultiplier) };
}

function downstreamKeyUpdateRequest(input: UpdateDownstreamKeyInput): DownstreamKeyUpdateRequest {
  const { billingMultiplier, ...rest } = input;
  return { ...rest, billingMultiplierBPS: billingMultiplierToBPS(billingMultiplier) };
}

interface RequestAttemptResponse {
  id: number;
  attemptIndex: number;
  publishedModelTargetId: number;
  publishedModelTargetRevision: number;
  providerModelTargetId: number;
  providerModelTargetRevision: number;
  siteId: number;
  siteName: string;
  endpointId: number;
  endpointName: string;
  credentialId: number;
  credentialName: string;
  sourceModel: string;
  responseModel: string;
  wireProtocol: WireProtocol;
  apiSurface: InferenceSurface;
  status: string;
  httpStatus: number | null;
  failureKind: string;
  errorCode: string;
  switchReason: string;
  firstOutputMs: number | null;
  durationMs: number;
  startedAt: number;
  finishedAt: number;
}

interface RouteCredentialResponse {
  id: number;
  name: string;
  position: number;
  runtimeState: string;
  coolingUntil: number | null;
}

interface RouteCandidateResponse {
  position: number;
  publishedModelTargetId: number;
  publishedModelTargetRevision: number;
  providerModelTargetId: number;
  providerModelTargetRevision: number;
  siteId: number;
  siteName: string;
  endpointId: number;
  endpointName: string;
  sourceModel: string;
  wireProtocol: WireProtocol;
  apiSurface: InferenceSurface;
  credentials: RouteCredentialResponse[];
  initialEligibility: string;
  initialReason: string;
  disposition: string;
  dispositionReason: string;
  attemptCount: number;
  firstAttemptIndex: number | null;
  lastAttemptIndex: number | null;
}

interface QuotaLedgerResponse {
  id: number;
  eventType: string;
  reservedDeltaNanoUsd: number;
  usedDeltaNanoUsd: number;
  priceCatalogVersion: string;
  priceSku: string;
  createdAt: number;
}

function authUser(response: AuthStatusResponse): User {
  if (!response.authenticated || !response.username) {
    throw new ApiError('管理员会话尚未登录', 401, 'unauthenticated', response);
  }
  return { username: response.username, expiresAt: response.expires_at };
}

function gatewayAttempt(item: RequestAttemptResponse): GatewayLogAttempt {
  return {
    id: item.id,
    attemptIndex: item.attemptIndex,
    publishedModelTargetId: item.publishedModelTargetId,
    publishedModelTargetRevision: item.publishedModelTargetRevision,
    providerModelTargetId: item.providerModelTargetId,
    providerModelTargetRevision: item.providerModelTargetRevision,
    siteId: item.siteId,
    siteName: item.siteName,
    endpointId: item.endpointId,
    endpointName: item.endpointName,
    credentialId: item.credentialId,
    credentialName: item.credentialName,
    sourceModel: item.sourceModel,
    responseModel: item.responseModel,
    wireProtocol: item.wireProtocol,
    apiSurface: item.apiSurface,
    status: item.status,
    httpStatus: item.httpStatus,
    failureKind: item.failureKind,
    errorCode: item.errorCode,
    switchReason: item.switchReason,
    durationMs: item.durationMs,
    firstOutputMs: item.firstOutputMs,
    startedAt: item.startedAt,
    finishedAt: item.finishedAt,
  };
}

function gatewayRouteCandidate(item: RouteCandidateResponse): GatewayRouteCandidate {
  return {
    position: item.position,
    publishedModelTargetId: item.publishedModelTargetId,
    publishedModelTargetRevision: item.publishedModelTargetRevision,
    providerModelTargetId: item.providerModelTargetId,
    providerModelTargetRevision: item.providerModelTargetRevision,
    siteId: item.siteId,
    siteName: item.siteName,
    endpointId: item.endpointId,
    endpointName: item.endpointName,
    sourceModel: item.sourceModel,
    wireProtocol: item.wireProtocol,
    apiSurface: item.apiSurface,
    credentials: [...item.credentials].sort((left, right) => left.position - right.position),
    initialEligibility: item.initialEligibility,
    initialReason: item.initialReason,
    disposition: item.disposition,
    dispositionReason: item.dispositionReason,
    attemptCount: item.attemptCount,
    firstAttemptIndex: item.firstAttemptIndex,
    lastAttemptIndex: item.lastAttemptIndex,
  };
}

function quotaLedgerEvent(item: QuotaLedgerResponse): GatewayQuotaLedgerEvent {
  return {
    id: item.id,
    eventType: item.eventType,
    reservedDeltaNanoUSD: item.reservedDeltaNanoUsd,
    usedDeltaNanoUSD: item.usedDeltaNanoUsd,
    priceCatalogVersion: item.priceCatalogVersion,
    priceSKU: item.priceSku,
    createdAt: item.createdAt,
  };
}

function gatewayLog(
  item: RequestLogResponse,
  attempts: GatewayLogAttempt[] = [],
  ledger: GatewayQuotaLedgerEvent[] = [],
  routeCandidates: GatewayRouteCandidate[] = [],
): GatewayLog {
  const orderedAttempts = [...attempts].sort((left, right) => left.attemptIndex - right.attemptIndex);
  const listedFinal = item.finalAttempt ? gatewayAttempt(item.finalAttempt) : null;
  const final = orderedAttempts[orderedAttempts.length - 1] || listedFinal;
  return {
    id: item.id,
    downstreamKeyId: item.downstreamKeyId,
    startedAt: item.startedAt,
    finishedAt: item.finishedAt,
    keyName: item.downstreamKeyName,
    publishedModelId: item.publishedModelId,
    publishedModelRevision: item.publishedModelRevision,
    effectiveRoutingProfileId: item.effectiveRoutingProfileId,
    effectiveRoutingProfileName: item.effectiveRoutingProfileName,
    sourceRoutingProfileId: item.sourceRoutingProfileId,
    sourceRoutingProfileName: item.sourceRoutingProfileName,
    routeRevision: item.routeRevision,
    publicModel: item.publicModel,
    actualModel: final?.responseModel || '',
    surface: item.apiSurface,
    reasoningEffort: item.reasoningEffort,
    thinkingBudgetTokens: item.thinkingBudgetTokens,
    status: item.status,
    meteringStatus: item.meteringStatus,
    meteringErrorCode: item.meteringErrorCode,
    httpStatus: item.httpStatus,
    stream: item.stream,
    firstOutputMs: item.firstOutputMs,
    durationMs: item.totalDurationMs,
    inputTokens: item.inputTokens,
    outputTokens: item.outputTokens,
    cacheReadTokens: item.cacheReadTokens,
    cacheWriteTokens: item.cacheWriteTokens,
    cacheWrite5mTokens: item.cacheWrite5mTokens,
    cacheWrite1hTokens: item.cacheWrite1hTokens,
    reasoningTokens: item.reasoningTokens,
    priceCatalogVersion: item.priceCatalogVersion,
    priceSKU: item.priceSku,
    reservationNanoUSD: item.reservationNanoUsd,
    billingMultiplier: billingMultiplierFromBPS(item.billingMultiplierBPS),
    officialCostNanoUSD: item.officialCostNanoUsd,
    chargedNanoUSD: item.chargedNanoUsd,
    quotaCapped: item.quotaCapped,
    errorCode: item.errorCode,
    switchCount: Math.max(0, orderedAttempts.length ? orderedAttempts.length - 1 : (item.finalAttemptIndex || 0)),
    finalAttempt: final || null,
    routeCandidates: [...routeCandidates].sort((left, right) => left.position - right.position),
    attempts: orderedAttempts,
    ledger,
  };
}

function gatewayLogQuery(filter: GatewayLogFilter, includePagination: boolean): string {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(filter)) {
    if (!includePagination && (key === 'cursor' || key === 'limit')) continue;
    if (value !== undefined && value !== null && String(value).trim() !== '') query.set(key, String(value));
  }
  if (filter.query?.trim()) {
    query.delete('query');
    query.set('search', filter.query.trim());
  }
  if (filter.keyId) {
    query.delete('keyId');
    query.set('downstreamKeyId', String(filter.keyId));
  }
  return query.size ? `?${query.toString()}` : '';
}

export const api = {
  async me(): Promise<User> {
    return authUser(await requestJSON<AuthStatusResponse>(`${SESSION_PREFIX}/status`));
  },

  async login(password: string): Promise<User> {
    return authUser(await requestJSON<AuthStatusResponse>(
      `${SESSION_PREFIX}/login`,
      jsonInit('POST', { username: 'admin', password }),
    ));
  },

  logout(): Promise<void> {
    return requestJSON<void>(`${SESSION_PREFIX}/logout`, { method: 'POST' });
  },

  async listSites(): Promise<Site[]> {
    return (await requestJSON<{ items: Site[] }>(inventory('/sites'))).items;
  },

  async getSite(siteId: number): Promise<Site> {
    return (await requestJSON<{ item: Site }>(inventory(`/sites/${siteId}`))).item;
  },

  async createSite(input: CreateSiteInput): Promise<Site> {
    return (await requestJSON<{ item: Site }>(inventory('/sites'), jsonInit('POST', input))).item;
  },

  async updateSite(siteId: number, revision: number, input: UpdateSiteInput): Promise<Site> {
    return (await requestJSON<{ item: Site }>(inventory(`/sites/${siteId}`), jsonInit('PATCH', input, revision))).item;
  },

  async listEndpoints(siteId: number): Promise<SiteEndpoint[]> {
    return (await requestJSON<{ items: SiteEndpoint[] }>(inventory(`/sites/${siteId}/endpoints`))).items;
  },

  async createEndpoint(siteId: number, input: EndpointInput): Promise<SiteEndpoint> {
    return (await requestJSON<{ item: SiteEndpoint }>(inventory(`/sites/${siteId}/endpoints`), jsonInit('POST', input))).item;
  },

  async updateEndpoint(siteId: number, endpointId: number, revision: number, input: Partial<EndpointInput>): Promise<SiteEndpoint> {
    return (await requestJSON<{ item: SiteEndpoint }>(
      inventory(`/sites/${siteId}/endpoints/${endpointId}`),
      jsonInit('PATCH', input, revision),
    )).item;
  },

  async listCredentials(siteId: number): Promise<SiteCredential[]> {
    return (await requestJSON<{ items: SiteCredential[] }>(inventory(`/sites/${siteId}/credentials`))).items;
  },

  async createCredential(siteId: number, input: { name: string; secret: string; enabled?: boolean }): Promise<SiteCredential> {
    return (await requestJSON<{ item: SiteCredential }>(
      inventory(`/sites/${siteId}/credentials`),
      jsonInit('POST', input),
    )).item;
  },

  async updateCredential(
    siteId: number,
    credentialId: number,
    revision: number,
    input: { name?: string; enabled?: boolean },
  ): Promise<SiteCredential> {
    return (await requestJSON<{ item: SiteCredential }>(
      inventory(`/sites/${siteId}/credentials/${credentialId}`),
      jsonInit('PATCH', input, revision),
    )).item;
  },

  async replaceCredentialSecret(siteId: number, credentialId: number, revision: number, secret: string): Promise<SiteCredential> {
    return (await requestJSON<{ item: SiteCredential }>(
      inventory(`/sites/${siteId}/credentials/${credentialId}/secret`),
      jsonInit('PUT', { secret }, revision),
    )).item;
  },

  async listEndpointCredentialBindings(siteId: number, endpointId: number): Promise<CredentialBinding[]> {
    return (await requestJSON<{ items: CredentialBinding[] }>(inventory(`/sites/${siteId}/endpoints/${endpointId}/credentials`))).items;
  },

  async replaceEndpointCredentialBindings(
    siteId: number,
    endpointId: number,
    revision: number,
    credentialIds: number[],
  ): Promise<{ endpoint: SiteEndpoint; items: CredentialBinding[] }> {
    return requestJSON(inventory(`/sites/${siteId}/endpoints/${endpointId}/credentials`), jsonInit('PUT', { credentialIds }, revision));
  },

  async listProviderModels(siteId: number, endpointId: number): Promise<ProviderModel[]> {
    return (await requestJSON<{ items: ProviderModel[] }>(inventory(`/sites/${siteId}/endpoints/${endpointId}/models`))).items;
  },

  previewModels(input: ModelDiscoveryPreviewInput): Promise<ModelDiscoveryPreviewResult> {
    return requestJSON<ModelDiscoveryPreviewResult>(
      inventory('/model-discovery/preview'),
      jsonInit('POST', input),
    );
  },

  async discoverModels(siteId: number, endpointId: number, credentialId: number): Promise<DiscoveredModel[]> {
    return (await requestJSON<{ items: DiscoveredModel[] }>(
      inventory(`/sites/${siteId}/endpoints/${endpointId}/models/discover`),
      jsonInit('POST', { credentialId }),
    )).items;
  },

  async importModels(siteId: number, endpointId: number, credentialId: number, models: string[]): Promise<ProviderModel[]> {
    return (await requestJSON<{ items: ProviderModel[] }>(
      inventory(`/sites/${siteId}/endpoints/${endpointId}/models/import`),
      jsonInit('POST', { credentialId, models }),
    )).items;
  },

  async listModelTargets(filter: ModelTargetFilter = {}): Promise<ModelTarget[]> {
    const query = new URLSearchParams();
    if (filter.query?.trim()) query.set('q', filter.query.trim());
    if (filter.protocol) query.set('protocol', filter.protocol);
    if (filter.surface) query.set('surface', filter.surface);
    if (filter.siteId) query.set('siteId', String(filter.siteId));
    if (filter.enabled !== undefined) query.set('enabled', String(filter.enabled));
    const suffix = query.size ? `?${query.toString()}` : '';
    return (await requestJSON<{ items: ModelTarget[] }>(inventory(`/model-targets${suffix}`))).items;
  },

  async listDownstreamKeys(): Promise<DownstreamKey[]> {
    return (await requestJSON<{ items: DownstreamKeyResponse[] }>(keyPath())).items.map(downstreamKey);
  },

  async getDownstreamKey(keyId: number): Promise<DownstreamKey> {
    return downstreamKey((await requestJSON<{ item: DownstreamKeyResponse }>(keyPath(`/${keyId}`))).item);
  },

  async createDownstreamKey(input: CreateDownstreamKeyInput): Promise<IssuedDownstreamKey> {
    const issued = await requestJSON<IssuedDownstreamKeyResponse>(keyPath(), jsonInit('POST', downstreamKeyRequest(input)));
    return { item: downstreamKey(issued.item), secret: issued.secret };
  },

  async revealDownstreamKey(keyId: number): Promise<string> {
    return (await requestJSON<{ secret: string }>(keyPath(`/${keyId}/reveal`), { method: 'POST' })).secret;
  },

  async rotateDownstreamKey(keyId: number, revision: number): Promise<IssuedDownstreamKey> {
    const issued = await requestJSON<IssuedDownstreamKeyResponse>(keyPath(`/${keyId}/rotate`), revisionInit('POST', revision));
    return { item: downstreamKey(issued.item), secret: issued.secret };
  },

  async updateDownstreamKey(keyId: number, revision: number, input: UpdateDownstreamKeyInput): Promise<DownstreamKey> {
    return downstreamKey((await requestJSON<{ item: DownstreamKeyResponse }>(
      keyPath(`/${keyId}`),
      jsonInit('PATCH', downstreamKeyUpdateRequest(input), revision),
    )).item);
  },

  async listDownstreamKeyModels(keyId: number): Promise<string[]> {
    const response = await requestJSON<{ items: Array<string | { id: string }> }>(keyPath(`/${keyId}/models`));
    return response.items.map((item) => typeof item === 'string' ? item : item.id);
  },

  async listRoutingProfiles(): Promise<RoutingProfile[]> {
    return (await requestJSON<{ items: RoutingProfile[] }>(ROUTING_PREFIX)).items;
  },

  async createRoutingProfile(input: CreateRoutingProfileInput): Promise<RoutingProfile> {
    return (await requestJSON<{ item: RoutingProfile }>(ROUTING_PREFIX, createInit(input))).item;
  },

  async updateRoutingProfile(profileId: number, revision: number, input: Partial<CreateRoutingProfileInput>): Promise<RoutingProfile> {
    return (await requestJSON<{ item: RoutingProfile }>(`${ROUTING_PREFIX}/${profileId}`, jsonInit('PATCH', input, revision))).item;
  },

  deleteRoutingProfile(profileId: number, revision: number): Promise<void> {
    return requestJSON<void>(`${ROUTING_PREFIX}/${profileId}`, revisionInit('DELETE', revision));
  },

  async listProfileRoutes(profileId: number): Promise<ModelRoute[]> {
    return (await requestJSON<{ items: ModelRoute[] }>(`${ROUTING_PREFIX}/${profileId}/routes`)).items;
  },

  async createProfileRoute(profileId: number, profileRevision: number, input: CreateRouteInput): Promise<ModelRoute> {
    return (await requestJSON<{ item: ModelRoute }>(
      `${ROUTING_PREFIX}/${profileId}/routes`,
      jsonInit('POST', input, profileRevision),
    )).item;
  },

  async updateProfileRoute(profileId: number, publishedModelId: number, revision: number, input: UpdateRouteInput): Promise<ModelRoute> {
    return (await requestJSON<{ item: ModelRoute }>(
      `${ROUTING_PREFIX}/${profileId}/routes/${publishedModelId}`,
      jsonInit('PATCH', input, revision),
    )).item;
  },

  async replaceProfileRouteTargets(profileId: number, publishedModelId: number, revision: number, providerTargetIds: number[]): Promise<ModelRoute> {
    return (await requestJSON<{ item: ModelRoute }>(
      `${ROUTING_PREFIX}/${profileId}/routes/${publishedModelId}/targets`,
      jsonInit('PUT', { providerTargetIds }, revision),
    )).item;
  },

  deleteProfileRoute(profileId: number, publishedModelId: number, revision: number): Promise<void> {
    return requestJSON<void>(
      `${ROUTING_PREFIX}/${profileId}/routes/${publishedModelId}`,
      revisionInit('DELETE', revision),
    );
  },

  async getSiteAccount(siteId: number): Promise<SiteAccountConnection | null> {
    try {
      return await requestJSON<SiteAccountConnection>(`${SITE_ACCOUNTS_PREFIX}/sites/${siteId}`);
    } catch (error) {
      if (error instanceof ApiUnavailableError && error.status === 404) return null;
      throw error;
    }
  },

  configureSiteAccount(siteId: number, input: AccountConnectionInput): Promise<SiteAccountConnection> {
    return requestJSON(`${SITE_ACCOUNTS_PREFIX}/sites/${siteId}`, jsonInit('PUT', input));
  },

  updateSiteAccount(
    siteId: number,
    revision: number,
    input: { adapterKind?: string; origin?: string; enabled?: boolean },
  ): Promise<SiteAccountConnection> {
    return requestJSON(`${SITE_ACCOUNTS_PREFIX}/sites/${siteId}`, jsonInit('PATCH', input, revision));
  },

  deleteSiteAccount(siteId: number, revision: number): Promise<void> {
    return requestJSON(`${SITE_ACCOUNTS_PREFIX}/sites/${siteId}`, revisionInit('DELETE', revision));
  },

  replaceSiteAccountSecret(siteId: number, revision: number, secrets: AccountSecretInput): Promise<SiteAccountConnection> {
    return requestJSON(`${SITE_ACCOUNTS_PREFIX}/sites/${siteId}/secret`, jsonInit('PUT', secrets, revision));
  },

  async refreshSiteBalance(siteId: number): Promise<SiteBalance> {
    return (await requestJSON<{ balance: SiteBalance }>(`${SITE_ACCOUNTS_PREFIX}/sites/${siteId}/balance/refresh`, { method: 'POST' })).balance;
  },

  async listSiteUsage(siteId: number, filter: SiteUsageFilter = {}): Promise<SiteUsagePage> {
    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(filter)) {
      if (value !== undefined && value !== null && String(value).trim() !== '') query.set(key, String(value));
    }
    const suffix = query.size ? `?${query.toString()}` : '';
    return requestJSON(`${SITE_ACCOUNTS_PREFIX}/sites/${siteId}/usage${suffix}`);
  },

  async syncSiteUsage(siteId: number, filter: Omit<SiteUsageFilter, 'search'> = {}): Promise<void> {
    await requestJSON(`${SITE_ACCOUNTS_PREFIX}/sites/${siteId}/usage/sync`, jsonInit('POST', filter));
  },

  async pricingState(): Promise<CatalogState> {
    return (await requestJSON<{ state: CatalogState }>(`${PRICING_PREFIX}/state`)).state;
  },

  listPriceCatalogs(): Promise<PriceCatalogList> {
    return requestJSON<PriceCatalogList>(`${PRICING_PREFIX}/catalogs`);
  },

  async getPriceCatalog(version: string): Promise<PriceCatalog> {
    return (await requestJSON<{ catalog: PriceCatalog }>(
      `${PRICING_PREFIX}/catalogs/${encodeURIComponent(version)}`,
    )).catalog;
  },

  previewPriceCatalog(catalog: PriceCatalog): Promise<PriceCatalogPreview> {
    return requestJSON<PriceCatalogPreview>(
      `${PRICING_PREFIX}/catalogs/preview`,
      jsonInit('POST', { catalog }),
    );
  },

  importPriceCatalog(catalog: PriceCatalog, expectedDigest: string): Promise<PriceCatalogImportResult> {
    return requestJSON<PriceCatalogImportResult>(
      `${PRICING_PREFIX}/catalogs`,
      jsonInit('POST', { catalog, expected_digest: expectedDigest }),
    );
  },

  async activatePriceCatalog(version: string, revision: number): Promise<CatalogState> {
    return (await requestJSON<{ state: CatalogState }>(
      `${PRICING_PREFIX}/catalogs/${encodeURIComponent(version)}/activate`,
      revisionInit('POST', revision),
    )).state;
  },

  monitorSnapshot(): Promise<MonitorSnapshot> {
    return requestJSON<MonitorSnapshot>(MONITOR_PREFIX);
  },

  async createMonitorModel(
    publishedModelId: number,
    input: { enabled: boolean; historyLimit?: number },
  ): Promise<MonitorSetting> {
    return (await requestJSON<{ item: MonitorSetting }>(
      `${MONITOR_PREFIX}/models/${publishedModelId}`,
      jsonInit('POST', input),
    )).item;
  },

  async updateMonitorModel(
    publishedModelId: number,
    revision: number,
    input: { enabled?: boolean; historyLimit?: number },
  ): Promise<MonitorSetting> {
    return (await requestJSON<{ item: MonitorSetting }>(
      `${MONITOR_PREFIX}/models/${publishedModelId}`,
      jsonInit('PATCH', input, revision),
    )).item;
  },

  async probeModel(publishedModelId: number): Promise<void> {
    await requestJSON<unknown>(`${MONITOR_PREFIX}/models/${publishedModelId}/probe`, { method: 'POST' });
  },

  monitorTargetHistory(publishedModelId: number, providerModelTargetId: number, limit = 200): Promise<MonitorTargetHistory> {
    const query = new URLSearchParams({ limit: String(limit) });
    return requestJSON<MonitorTargetHistory>(
      `${MONITOR_PREFIX}/models/${publishedModelId}/targets/${providerModelTargetId}/history?${query.toString()}`,
    );
  },

  gatewayLogs(filter: GatewayLogFilter = {}): Promise<GatewayLogPage> {
    const suffix = gatewayLogQuery(filter, true);
    return requestJSON<{ items: RequestLogResponse[]; hasMore: boolean; nextCursor: string }>(`${LOGS_PREFIX}${suffix}`)
      .then((page) => ({ ...page, items: page.items.map((item) => gatewayLog(item)) }));
  },

  gatewayLogSummary(filter: GatewayLogSummaryFilter = {}): Promise<GatewayLogSummary> {
    return requestJSON<GatewayLogSummary>(`${LOGS_PREFIX}/summary${gatewayLogQuery(filter, false)}`);
  },

  async gatewayLogDetail(requestId: string): Promise<GatewayLog> {
    const detail = await requestJSON<{
      request: RequestLogResponse;
      routeCandidates: RouteCandidateResponse[];
      attempts: RequestAttemptResponse[];
      ledger: QuotaLedgerResponse[];
    }>(`${LOGS_PREFIX}/${encodeURIComponent(requestId)}`);
    return gatewayLog(
      detail.request,
      detail.attempts.map(gatewayAttempt),
      detail.ledger.map(quotaLedgerEvent),
      detail.routeCandidates.map(gatewayRouteCandidate),
    );
  },

  settings(): Promise<GatewaySettings> {
    return requestJSON<GatewaySettings>(SETTINGS_PREFIX);
  },

  updateSettings(revision: number, input: Omit<GatewaySettings, 'revision'>): Promise<GatewaySettings> {
    return requestJSON<GatewaySettings>(SETTINGS_PREFIX, jsonInit('PATCH', input, revision));
  },
};

export const endpointProfiles: Array<{
  id: string;
  label: string;
  protocol: WireProtocol;
  surface: InferenceSurface;
  authScheme: AuthScheme;
  baseUrlPlaceholder: string;
}> = [
  {
    id: 'openai-chat',
    label: 'OpenAI Chat Completions',
    protocol: 'openai',
    surface: 'openai.chat_completions',
    authScheme: 'bearer',
    baseUrlPlaceholder: 'https://relay.example.com/v1',
  },
  {
    id: 'openai-responses',
    label: 'OpenAI Responses',
    protocol: 'openai',
    surface: 'openai.responses',
    authScheme: 'bearer',
    baseUrlPlaceholder: 'https://relay.example.com/v1',
  },
  {
    id: 'anthropic-messages',
    label: 'Anthropic Messages',
    protocol: 'anthropic',
    surface: 'anthropic.messages',
    authScheme: 'x-api-key',
    baseUrlPlaceholder: 'https://relay.example.com',
  },
  {
    id: 'gemini-generate-content',
    label: 'Gemini GenerateContent',
    protocol: 'gemini',
    surface: 'gemini.generate_content',
    authScheme: 'x-goog-api-key',
    baseUrlPlaceholder: 'https://relay.example.com',
  },
];
