export type WireProtocol = 'openai' | 'anthropic' | 'gemini';

export type InferenceSurface =
  | 'openai.chat_completions'
  | 'openai.responses'
  | 'anthropic.messages'
  | 'gemini.generate_content';

export type AuthScheme = 'bearer' | 'x-api-key' | 'x-goog-api-key' | 'query-key';

export interface User {
  username: string;
  expiresAt?: number;
}

export interface Site {
  id: number;
  name: string;
  dashboardUrl: string;
  enabled: boolean;
  revision: number;
  createdAt: number;
  updatedAt: number;
}

export interface CreateSiteInput {
  name: string;
  dashboardUrl?: string;
  enabled?: boolean;
}

export interface UpdateSiteInput {
  name?: string;
  dashboardUrl?: string | null;
  enabled?: boolean;
}

export interface SiteEndpoint {
  id: number;
  siteId: number;
  name: string;
  baseUrl: string;
  wireProtocol: WireProtocol;
  surface: InferenceSurface;
  adapterKind: string;
  authScheme: AuthScheme;
  headers: Record<string, string>;
  secretHeadersConfigured: boolean;
  position: number;
  enabled: boolean;
  revision: number;
  createdAt: number;
  updatedAt: number;
}

export interface EndpointInput {
  name: string;
  baseUrl: string;
  wireProtocol: WireProtocol;
  surface: InferenceSurface;
  adapterKind?: string;
  authScheme?: AuthScheme;
  headers?: Record<string, string>;
  enabled?: boolean;
}

export interface SiteCredential {
  id: number;
  siteId: number;
  name: string;
  secretConfigured: boolean;
  enabled: boolean;
  revision: number;
  runtimeState: string;
  coolingUntil: number | null;
  lastHttpStatus: number | null;
  lastErrorCode: string;
  runtimeRevision: number;
  runtimeUpdatedAt: number;
  createdAt: number;
  updatedAt: number;
}

export interface CredentialBinding {
  credentialId: number;
  credentialName: string;
  position: number;
  enabled: boolean;
  createdAt: number;
  updatedAt: number;
}

export interface ProviderModel {
  id: number;
  siteId: number;
  endpointId: number;
  sourceModel: string;
  displayName: string;
  enabled: boolean;
  revision: number;
  lastSeenAt: number | null;
  createdAt: number;
  updatedAt: number;
}

export interface DiscoveredModel {
  sourceModel: string;
  imported: boolean;
  targetId: number | null;
  enabled: boolean | null;
  revision: number | null;
}

export interface ProtocolCapabilities {
  discovery: boolean;
  request: boolean;
  response: boolean;
  stream: boolean;
  usage: boolean;
  error: boolean;
}

export interface ModelTarget extends ProviderModel {
  siteName: string;
  siteEnabled: boolean;
  endpointName: string;
  endpointEnabled: boolean;
  baseUrl: string;
  wireProtocol: WireProtocol;
  surface: InferenceSurface;
  adapterKind: string;
  authScheme: AuthScheme;
  boundCredentialCount: number;
  usableCredentialCount: number;
  unknownCredentialCount: number;
  credentialIds?: number[];
  credentialNames?: string[];
  capabilities: ProtocolCapabilities;
  routable: boolean;
}

export interface RouteTarget {
  id: number;
  publishedModelId: number;
  siteId: number;
  siteName: string;
  endpointId: number;
  endpointName: string;
  providerModelTargetId: number;
  sourceModel: string;
  wireProtocol: WireProtocol;
  apiSurface: InferenceSurface;
  position: number;
  revision: number;
  createdAt: number;
  updatedAt: number;
}

export interface ModelRoute {
  routingProfileId: number;
  routingProfileName: string;
  sourceProfileId: number;
  sourceProfileName: string;
  inherited: boolean;
  targetsOverridden: boolean;
  publishedModelId: number;
  publicName: string;
  officialPriceSku: string;
  enabled: boolean;
  publishedModelRevision: number;
  revision: number;
  targets: RouteTarget[];
  createdAt: number;
  updatedAt: number;
}

export interface RoutingProfile {
  id: number;
  name: string;
  isDefault: boolean;
  revision: number;
  modelCount: number;
  localModelCount: number;
  inheritedModelCount: number;
  downstreamKeyCount: number;
  createdAt: number;
  updatedAt: number;
}

export interface CreateRoutingProfileInput {
  name: string;
}

export interface CreateDefaultRouteInput {
  publicName: string;
  officialPriceSku: string;
  enabled: boolean;
  providerTargetIds: number[];
}

export interface CreateCustomRouteInput {
  publishedModelId: number;
  enabled: boolean;
  providerTargetIds?: number[];
}

export type CreateRouteInput = CreateDefaultRouteInput | CreateCustomRouteInput;

export interface UpdateRouteInput {
  publicName?: string;
  officialPriceSku?: string;
  enabled?: boolean;
}

export interface DownstreamKey {
  id: number;
  name: string;
  keyPrefix: string;
  enabled: boolean;
  revealable: boolean;
  quotaNanoUSD: number | null;
  usedNanoUSD: number;
  reservedNanoUSD: number;
  hourlyQuotaNanoUSD: number | null;
  usedThisHourNanoUSD: number;
  reservedThisHourNanoUSD: number;
  hourlyWindowStartedAt: number;
  billingMultiplier: number;
  expires: number | null;
  lastUsedAt: number | null;
  revision: number;
  routingProfileId: number;
  routingProfileName: string;
  usesDefaultRoutingProfile: boolean;
  models: string[];
  createdAt: number;
  updatedAt: number;
}

export interface CreateDownstreamKeyInput {
  name: string;
  quotaNanoUSD?: number | null;
  hourlyQuotaNanoUSD?: number | null;
  billingMultiplier?: number;
  expires?: number | null;
  routingProfileId?: number | null;
  enabled?: boolean;
}

export interface UpdateDownstreamKeyInput {
  name?: string;
  quotaNanoUSD?: number | null;
  hourlyQuotaNanoUSD?: number | null;
  billingMultiplier?: number;
  expires?: number | null;
  routingProfileId?: number | null;
  enabled?: boolean;
}

export interface IssuedDownstreamKey {
  item: DownstreamKey;
  secret: string;
}

export interface SiteAccountCapabilities {
  sessionRefresh: boolean;
  balance: boolean;
  usage: boolean;
}

export interface SiteBalance {
  id: number;
  accountRemoteId: string;
  accountName: string;
  availableValue: string;
  availableUnit: string;
  usedValue: string | null;
  usedUnit: string | null;
  capturedAt: number;
}

export interface SiteAccountConnection {
  id: number;
  siteId: number;
  siteName: string;
  adapterKind: string;
  origin: string;
  secretConfigured: boolean;
  enabled: boolean;
  capabilities: SiteAccountCapabilities;
  adapterAvailable: boolean;
  lastSessionRefreshAt: number | null;
  lastBalanceRefreshAt: number | null;
  lastUsageRefreshAt: number | null;
  lastErrorOperation: string;
  lastErrorCode: string;
  lastErrorAt: number | null;
  latestBalance: SiteBalance | null;
  revision: number;
  createdAt: number;
  updatedAt: number;
}

export interface SiteUsageRecord {
  id: number;
  remoteId: string;
  requestId: string;
  upstreamRequestId: string;
  occurredAt: number;
  model: string;
  upstreamModel: string;
  status: string;
  httpStatus: number | null;
  inputTokens: number | null;
  outputTokens: number | null;
  cacheReadTokens: number | null;
  cacheWriteTokens: number | null;
  reasoningTokens: number | null;
  totalTokens: number | null;
  chargeValue: string | null;
  chargeUnit: string | null;
  durationMs: number | null;
  firstOutputMs?: number | null;
  apiKeyName: string;
  reasoningEffort?: string;
  requestPath?: string;
  requestType?: string;
  billingMode?: string;
  requestIp?: string;
  region?: string;
  group?: string;
  sourceFetchedAt: number;
}

export interface SiteUsagePage {
  items: SiteUsageRecord[];
  hasMore: boolean;
  nextCursor: string;
}

export interface CatalogState {
  active_version?: string;
  revision: number;
  updated_at: string;
}

export type PriceTokenClass =
  | 'input'
  | 'output'
  | 'cache_read'
  | 'cache_write'
  | 'cache_write_5m'
  | 'cache_write_1h'
  | 'reasoning';

export interface PriceRate {
  class: PriceTokenClass;
  native_price_per_million?: string;
  nano_usd_per_million: number;
}

export interface PriceLongContextTier {
  threshold_tokens: number;
  rates: PriceRate[];
}

export interface PriceCatalogEntry {
  sku: string;
  provider: string;
  model_pattern: string;
  pricing_basis?: string;
  verification_status?: string;
  source_url?: string;
  source_digest?: string;
  verified_at?: string;
  native_currency?: string;
  usd_per_native_unit?: string;
  rates: PriceRate[];
  long_context?: PriceLongContextTier;
}

export interface PriceCatalog {
  schema_version?: number;
  version: string;
  digest?: string;
  settlement_currency?: string;
  source: string;
  source_digest: string;
  fx_version?: string;
  fx_source_url?: string;
  fx_source_digest?: string;
  fx_verified_at?: string;
  fetched_at: string;
  verified_at?: string;
  effective_at: string;
  imported_at?: string;
  entries: PriceCatalogEntry[];
}

export interface PriceCatalogSummary {
  version: string;
  digest: string;
  settlement_currency: string;
  source: string;
  source_digest: string;
  entry_count: number;
  effective_at: string;
  verified_at: string;
  imported_at: string;
  active: boolean;
}

export interface PriceCatalogList {
  items: PriceCatalogSummary[];
  state: CatalogState;
}

export interface PriceDiffSummary {
  added_entries: number;
  removed_entries: number;
  changed_entries: number;
  unchanged_entries: number;
}

export interface PriceRateDiff {
  class: PriceTokenClass;
  kind: 'added' | 'removed' | 'changed';
  before?: PriceRate;
  after?: PriceRate;
}

export interface PriceLongContextDiff {
  kind: 'added' | 'removed' | 'changed';
  before_threshold_tokens?: number;
  after_threshold_tokens?: number;
  rates?: PriceRateDiff[];
}

export interface PriceEntryDiff {
  sku: string;
  kind: 'added' | 'removed' | 'changed';
  metadata_changed?: boolean;
  rates?: PriceRateDiff[];
  long_context?: PriceLongContextDiff;
}

export interface PriceCatalogDiff {
  active_version?: string;
  active_digest?: string;
  candidate_version: string;
  candidate_digest: string;
  summary: PriceDiffSummary;
  entries: PriceEntryDiff[];
}

export interface PriceCatalogPreview {
  candidate: PriceCatalog;
  state: CatalogState;
  diff: PriceCatalogDiff;
  can_activate: boolean;
}

export interface PriceCatalogImportResult {
  catalog: PriceCatalog;
  imported: boolean;
  state: CatalogState;
}

export type MonitorHealth =
  | 'healthy'
  | 'degraded'
  | 'unavailable'
  | 'unprobed'
  | 'suspect'
  | 'cooling'
  | 'recovering'
  | 'disabled'
  | 'model_disabled'
  | 'no_credentials'
  | 'unsupported'
  | 'skipped';

export interface MonitorSetting {
  enabled: boolean;
  intervalMs: number;
  historyLimit: number;
  nextProbeAt: number;
  lastProbeStartedAt: number | null;
  lastProbeFinishedAt: number | null;
  busy: boolean;
  revision: number;
  createdAt: number;
  updatedAt: number;
}

export interface RoutingHealth {
  phase: string;
  capability: string;
  consecutiveFailures: number;
  failureWindowStartedAt: number | null;
  lastFailureAt: number | null;
  lastSuccessAt: number | null;
  cooldownUntil: number | null;
  halfOpenLeaseUntil: number | null;
  lastEventAt: number | null;
  lastFailureKind: string;
  providerTargetRevision: number;
  stateVersion: number;
  updatedAt: number;
}

export interface MonitorProbePoint {
  id?: number;
  runId: string;
  providerModelTargetRevision: number;
  outcome: 'success' | 'failure' | 'skipped';
  permitMode: string;
  permitReason: string;
  httpStatus: number | null;
  failureKind: string;
  errorCode: string;
  totalLatencyMs: number;
  firstOutputMs: number | null;
  startedAt: number;
  finishedAt: number;
  healthApplied: boolean;
  healthApplyReason: string;
  healthErrorCode: string;
}

export interface MonitorTarget {
  publishedModelTargetId: number;
  publishedModelTargetRevision: number;
  providerModelTargetId: number;
  providerModelTargetRevision: number;
  position: number;
  siteId: number;
  siteName: string;
  endpointId: number;
  endpointName: string;
  sourceModel: string;
  wireProtocol: WireProtocol;
  apiSurface: InferenceSurface;
  enabled: boolean;
  usableCredentialCount: number;
  credentialId?: number;
  credentialName?: string;
  status: MonitorHealth;
  successes: number;
  failures: number;
  skipped: number;
  successBasisPoints: number;
  latest: MonitorProbePoint | null;
  statusBar: MonitorProbePoint[];
  health: RoutingHealth | null;
}

export interface MonitorModel {
  publishedModelId: number;
  publicModel: string;
  officialPriceSku: string;
  publishedModelEnabled: boolean;
  publishedModelRevision: number;
  status: MonitorHealth;
  monitor: MonitorSetting;
  targets: MonitorTarget[];
  successes: number;
  failures: number;
  skipped: number;
  successBasisPoints: number;
}

export interface MonitorSnapshot {
  items: MonitorModel[];
}

export interface MonitorTargetIdentity {
  publishedModelTargetId: number;
  providerModelTargetId: number;
  position: number;
  siteId: number;
  siteName: string;
  endpointId: number;
  endpointName: string;
  sourceModel: string;
  wireProtocol: WireProtocol;
  apiSurface: InferenceSurface;
}

export interface MonitorTargetHistory {
  publishedModelId: number;
  publicModel: string;
  target: MonitorTargetIdentity;
  status: MonitorHealth;
  successes: number;
  failures: number;
  skipped: number;
  total: number;
  attempted: number;
  successBasisPoints: number;
  health: RoutingHealth | null;
  order: 'oldest_first';
  items: MonitorProbePoint[];
}

export interface GatewayLogAttempt {
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
  durationMs: number | null;
  firstOutputMs: number | null;
  startedAt: number;
  finishedAt: number;
}

export interface GatewayRouteCredentialSnapshot {
  id: number;
  name: string;
  position: number;
  runtimeState: string;
  coolingUntil: number | null;
}

export interface GatewayRouteCandidate {
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
  credentials: GatewayRouteCredentialSnapshot[];
  initialEligibility: string;
  initialReason: string;
  disposition: string;
  dispositionReason: string;
  attemptCount: number;
  firstAttemptIndex: number | null;
  lastAttemptIndex: number | null;
}

export type GatewayMeteringStatus = 'pending' | 'metered' | 'unavailable' | 'not_applicable';

export interface GatewayQuotaLedgerEvent {
  id: number;
  eventType: string;
  reservedDeltaNanoUSD: number;
  usedDeltaNanoUSD: number;
  priceCatalogVersion: string;
  priceSKU: string;
  createdAt: number;
}

export interface GatewayLog {
  id: string;
  downstreamKeyId: number;
  startedAt: number;
  finishedAt: number | null;
  keyName: string;
  publishedModelId: number;
  publishedModelRevision: number;
  effectiveRoutingProfileId: number;
  effectiveRoutingProfileName: string;
  sourceRoutingProfileId: number;
  sourceRoutingProfileName: string;
  routeRevision: number;
  publicModel: string;
  actualModel: string;
  surface: InferenceSurface;
  reasoningEffort: string;
  thinkingBudgetTokens: number | null;
  status: string;
  meteringStatus: GatewayMeteringStatus;
  meteringErrorCode: string;
  httpStatus: number | null;
  stream: boolean;
  firstOutputMs: number | null;
  durationMs: number | null;
  inputTokens: number | null;
  outputTokens: number | null;
  cacheReadTokens: number | null;
  cacheWriteTokens: number | null;
  cacheWrite5mTokens: number | null;
  cacheWrite1hTokens: number | null;
  reasoningTokens: number | null;
  priceCatalogVersion: string;
  priceSKU: string;
  reservationNanoUSD: number;
  billingMultiplier: number;
  officialCostNanoUSD: number;
  chargedNanoUSD: number;
  quotaCapped: boolean;
  errorCode: string;
  switchCount: number;
  finalAttempt: GatewayLogAttempt | null;
  routeCandidates: GatewayRouteCandidate[];
  attempts: GatewayLogAttempt[];
  ledger: GatewayQuotaLedgerEvent[];
}

export interface GatewayLogPage {
  items: GatewayLog[];
  hasMore: boolean;
  nextCursor: string;
}

export interface GatewayLogSummary {
  requests: number;
  succeeded: number;
  failed: number;
  cancelled: number;
  running: number;
  successBasisPoints: number;
  totalChargedNanoUsd: number;
  totalOfficialNanoUsd: number;
  totalAttempts: number;
  requestsWithSwitches: number;
  averageDurationMs: number | null;
  p50DurationMs: number | null;
  p95DurationMs: number | null;
  p50FirstOutputMs: number | null;
  p95FirstOutputMs: number | null;
}

export interface GatewaySettings {
  failureThreshold: number;
  failureWindowMs: number;
  cooldownMs: number;
  probeIntervalMs: number;
  firstOutputTimeoutMs: number;
  streamIdleTimeoutMs: number;
  requestTimeoutMs: number;
  maxAttempts: number;
  logRetentionDays: number;
  revision: number;
}
