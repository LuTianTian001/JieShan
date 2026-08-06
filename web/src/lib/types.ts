export type HealthState =
  | 'healthy'
  | 'suspect'
  | 'cooldown'
  | 'credential_error'
  | 'probing'
  | 'unknown'
  | 'disabled';

export type Protocol = 'openai' | 'anthropic' | 'gemini' | 'compatible';

export interface User {
  id: number;
  username: string;
}

export type AccountAdapterKey = 'ciii' | 'new_api' | 'one_api';
export type AccountAuthKind = 'api_token' | 'access_refresh';
export type AccountSyncState = 'unconfigured' | 'ready' | 'syncing' | 'stale' | 'error';
export type AccountUsageRange = '24h' | '7d' | '30d';

export interface AccountTarget {
  kind: 'site' | 'legacy';
  id: number;
  name: string;
  dashboardUrl: string;
  baseUrl: string;
}

export interface SourceAmount {
  value: string;
  currency: string;
  display?: string;
  sourceLabel?: string;
}

export interface AccountAdapter {
  key: AccountAdapterKey;
  label: string;
  authKinds: AccountAuthKind[];
  capabilities: {
    balance: boolean;
    subscription: boolean;
    usage: boolean;
    tokenRefresh: boolean;
  };
}

export interface UpstreamAccount {
  configured: boolean;
  enabled: boolean;
  dashboardUrl: string;
  adapter?: Pick<AccountAdapter, 'key' | 'label'>;
  auth?: {
    kind: AccountAuthKind;
    hasApiToken: boolean;
    hasAccessToken: boolean;
    hasRefreshToken: boolean;
    accessTokenExpiresAt: string | null;
  };
  capabilities: AccountAdapter['capabilities'];
  sync: {
    state: AccountSyncState;
    lastAttemptAt: string | null;
    lastSuccessAt: string | null;
    nextAt: string | null;
    stale: boolean;
    error: { code: string; message: string } | null;
  };
  snapshot: {
    capturedAt: string;
    balance: SourceAmount | null;
    subscription: {
      planName: string;
      status: string | null;
      expiresAt: string | null;
      renewsAt: string | null;
      periodStart: string | null;
      periodEnd: string | null;
      remaining?: SourceAmount;
      total?: SourceAmount;
    } | null;
  } | null;
}

export type UpstreamAccountAuthInput =
  | { kind: 'api_token'; apiToken?: string }
  | { kind: 'access_refresh'; accessToken?: string; refreshToken?: string };

export interface ConfigureUpstreamAccountInput {
  adapterKey: AccountAdapterKey;
  dashboardUrl: string;
  enabled: boolean;
  auth: UpstreamAccountAuthInput;
  refreshNow: boolean;
}

export interface UpstreamUsageItem {
  id: string;
  externalId?: string;
  requestId?: string;
  upstreamRequestId?: string;
  occurredAt: string | null;
  syncedAt?: string;
  model: string | null;
  upstreamModel?: string | null;
  reasoningEffort?: string | null;
  amount: SourceAmount | null;
  originalCost?: string | null;
  actualCost?: string | null;
  quota?: string | null;
  inputTokens: number | null;
  cacheReadTokens?: number | null;
  cacheCreationTokens?: number | null;
  outputTokens: number | null;
  reasoningTokens?: number | null;
  totalTokens?: number | null;
  httpStatus?: number | null;
  status: string | null;
  durationMs?: number | null;
  firstTokenMs?: number | null;
  stream?: boolean | null;
  rateMultiplier?: string | null;
  modelMultiplier?: string | null;
  groupMultiplier?: string | null;
  apiKeyId?: string;
  apiKeyName?: string;
  groupId?: string;
  groupName?: string;
  endpoint?: string;
  requestType?: string;
  billingType?: string;
  billingMode?: string;
  sourceText?: string;
}

export interface UpstreamUsagePage {
  items: UpstreamUsageItem[];
  range: AccountUsageRange;
  lastSyncedAt: string | null;
  hasMore?: boolean;
  nextBeforeId?: string | null;
}

export interface UpstreamModel {
  id: string;
  name: string;
  displayName?: string;
  enabled: boolean;
  discoveredAt: string;
  endpointId?: number;
  stale?: boolean;
  credentialCount?: number;
  supportedCredentialCount?: number;
  unsupportedCredentialCount?: number;
  unknownCredentialCount?: number;
}

export interface Upstream {
  id: number;
  name: string;
  baseUrl: string;
  protocol: Protocol;
  enabled: boolean;
  state: HealthState;
  latencyMs: number | null;
  modelCount: number;
  credentialCount: number;
  lastSyncAt: string | null;
  lastError?: string;
  models?: UpstreamModel[];
}

export interface ModelDiscovery {
  upstreamId: number;
  discoveredAt: string;
  added: string[];
  removed: string[];
  unchanged: string[];
  complete: boolean;
}

export interface V2Site {
  id: number;
  name: string;
  dashboardUrl: string;
  enabled: boolean;
  revision: number;
  createdAt: number;
  updatedAt: number;
}

export interface V2SiteSummary extends V2Site {
  endpointCount: number;
  enabledEndpointCount: number;
  credentialCount: number;
  enabledCredentialCount: number;
  unavailableCredentialCount: number;
  modelCount: number;
  activeModelCount: number;
  publishedModelCount: number;
  lastModelSeenAt?: number | null;
}

export interface V2EndpointCapabilities {
  modelDiscovery: boolean;
  chatCompletions: boolean;
  responses: boolean;
  routeEligible: boolean;
}

export interface V2Endpoint {
  id: number;
  siteId: number;
  name: string;
  baseUrl: string;
  wireProtocol: string;
  compatibilityProfile: string;
  authScheme: string;
  customHeaders: Record<string, string> | null;
  capabilities?: V2EndpointCapabilities;
  position: number;
  enabled: boolean;
  revision: number;
  createdAt: number;
  updatedAt: number;
}

export interface V2Credential {
  id: number;
  siteId: number;
  name: string;
  secretConfigured: boolean;
  position: number;
  enabled: boolean;
  runtimeState: string;
  cooldownUntil?: number | null;
  lastTestAt?: number | null;
  lastTestStatus?: string;
  lastErrorMessage?: string;
  revision: number;
  createdAt: number;
  updatedAt: number;
}

export interface V2SiteDetail {
  site: V2Site;
  endpoints: V2Endpoint[];
  credentials: V2Credential[];
}

export interface V2SiteModel {
  id: number;
  siteId: number;
  endpointId: number;
  modelName: string;
  displayName?: string;
  enabled: boolean;
  stale: boolean;
  missingCount: number;
  lastSeenAt?: number | null;
  revision: number;
  createdAt: number;
  updatedAt: number;
  credentialCount: number;
  supportedCredentialCount: number;
  unsupportedCredentialCount: number;
  unknownCredentialCount: number;
}

export type V2DiscoveryStrategy = 'selected' | 'first_success' | 'all';

export interface V2DiscoveryAttempt {
  credentialId: number;
  credentialName: string;
  models: string[];
  complete: boolean;
  pagesFetched: number;
  error?: string;
}

export interface V2ModelDiscovery {
  siteId: number;
  endpointId: number;
  models: string[];
  complete: boolean;
  applied: boolean;
  attempts: V2DiscoveryAttempt[];
  discoveredAt: string;
}

export interface V2RouteSiteTarget {
  id: number;
  publishedModelId: number;
  siteId: number;
  siteName: string;
  endpointId: number;
  endpointName: string;
  wireProtocol: string;
  siteModelId: number;
  sourceModel: string;
  position: number;
  enabled: boolean;
  revision: number;
  createdAt: number;
  updatedAt: number;
}

export interface V2PublishedModel {
  id: number;
  publicName: string;
  displayName?: string;
  officialPriceSku?: string;
  enabled: boolean;
  monitorEnabled: boolean;
  monitorIntervalSeconds: number;
  cooldownSeconds: number;
  failureThreshold: number;
  failureWindowSeconds: number;
  firstOutputTimeoutSeconds: number;
  streamIdleTimeoutSeconds: number;
  requestDeadlineSeconds: number;
  maxAttempts: number;
  revision: number;
  createdAt: number;
  updatedAt: number;
  targets: V2RouteSiteTarget[];
}

export interface RoutingProfile {
  id: number;
  name: string;
  revision: number;
  modelOverrideCount: number;
  createdAt: number;
  updatedAt: number;
}

export interface RoutingProfileModelRoute {
  routingProfileId: number;
  publishedModelId: number;
  profileRevision: number;
  inheritsDefault: boolean;
  targets: V2RouteSiteTarget[];
}

export interface CreateV2PublishedModelInput {
  publicName: string;
  displayName?: string;
  officialPriceSku?: string;
  enabled?: boolean;
  monitorEnabled?: boolean;
  monitorIntervalSeconds?: number;
  cooldownSeconds?: number;
  failureThreshold?: number;
  failureWindowSeconds?: number;
  firstOutputTimeoutSeconds?: number;
  streamIdleTimeoutSeconds?: number;
  requestDeadlineSeconds?: number;
  maxAttempts?: number;
}

export interface UpdateV2PublishedModelInput extends Partial<CreateV2PublishedModelInput> {
  revision?: number;
}

export interface CreateV2RouteTargetInput {
  siteId: number;
  endpointId: number;
  siteModelId: number;
  enabled?: boolean;
  publishedRevision?: number;
}

export interface UpdateV2RouteTargetInput extends CreateV2RouteTargetInput {
  revision?: number;
}

export interface V2RouteTargetHealth {
  targetId: number;
  circuitPhase: 'closed' | 'open' | 'half_open';
  consecutiveFailures: number;
  lastFailureAt?: number | null;
  lastSuccessAt?: number | null;
  cooldownUntil?: number | null;
  halfOpenLeaseUntil?: number | null;
  capabilityState: 'unknown' | 'supported' | 'unsupported';
  lastErrorClass?: string;
  lastErrorMessage?: string;
  lastIncidentId?: string;
  updatedAt: number;
}

export interface V2ProbeAttempt {
  id: number;
  probeRunId: string;
  attemptIndex: number;
  routeSiteTargetId?: number | null;
  siteId?: number | null;
  endpointId?: number | null;
  inferenceCredentialId?: number | null;
  siteModelId?: number | null;
  siteName: string;
  endpointName: string;
  credentialName?: string;
  sourceModel: string;
  status: 'success' | 'failed' | 'skipped';
  httpStatus?: number | null;
  latencyMs?: number | null;
  firstOutputMs?: number | null;
  errorClass?: string;
  errorMessage?: string;
  startedAt: number;
  finishedAt: number;
}

export interface V2ProbeRun {
  id: string;
  publishedModelId: number;
  publicModel?: string;
  publishedModelRevision: number;
  triggerKind: 'scheduled' | 'manual' | 'recovery';
  status: 'running' | 'success' | 'partial' | 'failed' | 'cancelled';
  targetCount: number;
  successCount: number;
  failureCount: number;
  skippedCount: number;
  errorMessage?: string;
  startedAt: number;
  finishedAt?: number | null;
  attempts?: V2ProbeAttempt[];
  health?: V2RouteTargetHealth[];
}

export interface V2MonitorTarget extends V2RouteSiteTarget {
  health: V2RouteTargetHealth;
  lastProbe?: V2ProbeAttempt | null;
}

export interface V2MonitorModel extends Omit<V2PublishedModel, 'targets'> {
  targets: V2MonitorTarget[];
}

export interface V2MonitorMatrix {
  generatedAt: number;
  models: V2MonitorModel[];
}

export interface CreateV2SiteInput {
  name: string;
  dashboardUrl?: string;
  enabled?: boolean;
}

export interface CreateV2EndpointInput {
  name: string;
  baseUrl: string;
  wireProtocol: string;
  compatibilityProfile?: string;
  authScheme?: string;
  customHeaders?: Record<string, string>;
  enabled?: boolean;
  revision?: number;
}

export interface CreateV2CredentialInput {
  name: string;
  apiKey: string;
  enabled?: boolean;
  revision?: number;
}

export interface UpdateV2SiteInput {
  name?: string;
  dashboardUrl?: string;
  enabled?: boolean;
  revision?: number;
}

export interface UpdateV2EndpointInput {
  name?: string;
  baseUrl?: string;
  wireProtocol?: string;
  compatibilityProfile?: string;
  authScheme?: string;
  customHeaders?: Record<string, string>;
  enabled?: boolean;
  revision?: number;
}

export interface UpdateV2CredentialInput {
  name?: string;
  apiKey?: string;
  enabled?: boolean;
  revision?: number;
}

export interface RouteTarget {
  id: number;
  upstreamId: number;
  upstreamName: string;
  credentialName: string;
  sourceModel: string;
  state: HealthState;
  latencyMs: number | null;
  cooldownUntil: string | null;
  lastFailure?: string;
}

export interface Route {
  id: number;
  model: string;
  displayName?: string;
  enabled: boolean;
  monitored: boolean;
  revision: number;
  targets: RouteTarget[];
}

export interface MonitorMatrix {
  generatedAt: string;
  probeIntervalSeconds: number;
  routes: Route[];
}

export interface DownstreamKey {
  id: number;
  name: string;
  prefix: string;
  enabled: boolean;
  quotaUsd: number | null;
  spentUsd: number;
  allowedModels: string[];
  routingProfileId: number | null;
  routingProfileName: string;
  usesDefaultRouting: boolean;
  rpmLimit: number | null;
  expiresAt: string | null;
  lastUsedAt: string | null;
  createdAt: string;
}

export interface RequestAttempt {
  id: number;
  sequence: number;
  routingGeneration?: 'legacy' | 'v3' | string;
  targetId?: number | null;
  routeSiteTargetId?: number | null;
  upstreamId?: number | null;
  upstreamName?: string;
  siteId?: number | null;
  siteName?: string;
  endpointId?: number | null;
  endpointName?: string;
  credentialId?: number | null;
  credentialName?: string;
  siteModelId?: number | null;
  model: string;
  state: 'success' | 'failed' | 'cancelled';
  startedAt: string;
  durationMs: number | null;
  ttftMs: number | null;
  statusCode: number | null;
  switchReason?: string;
  errorClass?: string;
  error?: string;
}

export interface RequestLogFilter {
  status?: string;
  model?: string;
  siteId?: number;
  upstreamId?: number;
  downstreamKeyId?: number;
  stream?: boolean;
  switched?: boolean;
}

export interface RequestLogCursor {
  beforeTime: number;
  beforeId: string;
}

export interface RequestLogListItem {
  id: string;
  routingGeneration: 'legacy' | 'v3' | string;
  surface: 'chat_completions' | 'responses' | string;
  downstreamKeyId?: number | null;
  keyName: string;
  routeId?: number | null;
  routeRevision?: number | null;
  publishedModelId?: number | null;
  publishedModelRevision?: number | null;
  routingProfileId?: number | null;
  routingProfileName: string;
  actualUpstreamId?: number | null;
  actualUpstreamName?: string;
  actualSiteId?: number | null;
  actualSiteName?: string;
  actualEndpointId?: number | null;
  actualEndpointName?: string;
  actualCredentialId?: number | null;
  actualCredentialName?: string;
  requestedModel: string;
  actualModel?: string;
  reasoningEffort?: string;
  thinkingBudget?: number | null;
  status: string;
  httpStatus?: number | null;
  stream: boolean;
  firstTokenMs?: number | null;
  durationMs?: number | null;
  inputTokens?: number | null;
  cacheReadTokens?: number | null;
  cacheWriteTokens?: number | null;
  cacheWrite1hTokens?: number | null;
  outputTokens?: number | null;
  reasoningTokens?: number | null;
  costMicroUsd: number;
  priceSnapshot?: string;
  switchCount: number;
  errorMessage?: string;
  startedAt: number;
  finishedAt?: number | null;
}

export interface RequestLogAttemptDetail {
  id: number;
  requestId: string;
  attemptIndex: number;
  routingGeneration: 'legacy' | 'v3' | string;
  targetId?: number | null;
  upstreamId?: number | null;
  upstreamName?: string;
  routeSiteTargetId?: number | null;
  siteId?: number | null;
  siteName?: string;
  endpointId?: number | null;
  endpointName?: string;
  inferenceCredentialId?: number | null;
  credentialName?: string;
  siteModelId?: number | null;
  upstreamModel?: string;
  status: string;
  httpStatus?: number | null;
  switchReason?: string;
  errorClass?: string;
  errorMessage?: string;
  latencyMs?: number | null;
  firstTokenMs?: number | null;
  createdAt: number;
}

export interface RequestLogDetail extends RequestLogListItem {
  attempts: RequestLogAttemptDetail[];
}

export interface RequestLogPage {
  items: RequestLogListItem[];
  nextCursor: RequestLogCursor | null;
  hasMore: boolean;
}

export interface RequestLogSummary {
  count: number;
  successRate: number;
  costMicroUsd: number;
  switchRate: number;
  p50TtftMs: number | null;
  p95TtftMs: number | null;
}

export interface RequestLog {
  id: string;
  startedAt: string;
  finishedAt?: string | null;
  downstreamKeyId?: number | null;
  keyName: string;
  routeId?: number | null;
  routeRevision?: number | null;
  requestedModel: string;
  actualModel: string;
  status: 'running' | 'success' | 'failed';
  httpStatus?: number | null;
  stream?: boolean;
  durationMs: number | null;
  ttftMs: number | null;
  inputTokens: number | null;
  cacheTokens: number | null;
  outputTokens: number | null;
  reasoningTokens: number | null;
  costUsd: number;
  switchCount: number;
  reasoningEffort?: string;
  thinkingBudget?: number | null;
  errorMessage?: string | null;
  priceSnapshot?: string | null;
  attempts?: RequestAttempt[];
}

export interface GatewaySettings {
  probeIntervalSeconds: number;
  failureThreshold: number;
  failureWindowSeconds: number;
  cooldownSeconds: number;
  firstOutputTimeoutSeconds: number;
  streamIdleTimeoutSeconds: number;
  requestTimeoutSeconds: number;
  maxAttempts: number;
  logRetentionDays: number;
  priceCatalogVersion: string;
  priceCatalogUpdatedAt: string;
  priceCatalogSource: string;
  lastBackupAt: string | null;
}

export interface DashboardSummary {
  monitoredModels: number;
  healthyModels: number;
  attentionTargets: number;
  coolingTargets: number;
  successRate24h: number;
  requests24h: number;
}

export interface CreateUpstreamInput {
  name: string;
  baseUrl: string;
  protocol: Protocol;
  apiKey: string;
}

export interface UpdateUpstreamInput {
  name: string;
  baseUrl: string;
  protocol: Protocol;
  enabled: boolean;
  apiKey?: string;
}

export interface CreateKeyInput {
  name: string;
  quotaUsd: number | null;
  allowedModels: string[];
  routingProfileId: number | null;
  rpmLimit: number | null;
  expiresAt: string | null;
}

export interface CreateRouteInput {
  model: string;
  displayName?: string;
  monitored: boolean;
  targets: Array<{
    upstreamId: number;
    sourceModel: string;
  }>;
}

export interface UpdateRouteInput {
  model?: string;
  displayName?: string;
  enabled?: boolean;
  monitored?: boolean;
  targetModelIds?: number[];
}

export interface UpdateKeyInput {
  name?: string;
  enabled?: boolean;
  quotaUsd?: number;
  clearQuota?: boolean;
  rpmLimit?: number;
  allowedModels?: string[];
  routingProfileId?: number;
  clearRoutingProfile?: boolean;
  expiresAt?: string;
}
