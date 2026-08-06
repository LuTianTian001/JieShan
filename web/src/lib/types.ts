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
  occurredAt: string | null;
  model: string | null;
  amount: SourceAmount | null;
  inputTokens: number | null;
  outputTokens: number | null;
  status: string | null;
  sourceText?: string;
}

export interface UpstreamUsagePage {
  items: UpstreamUsageItem[];
  range: AccountUsageRange;
  lastSyncedAt: string | null;
}

export interface UpstreamModel {
  id: string;
  name: string;
  enabled: boolean;
  discoveredAt: string;
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
  rpmLimit: number | null;
  expiresAt: string | null;
  lastUsedAt: string | null;
  createdAt: string;
}

export interface RequestAttempt {
  id: number;
  sequence: number;
  upstreamName: string;
  model: string;
  state: 'success' | 'failed' | 'cancelled';
  startedAt: string;
  durationMs: number;
  ttftMs: number | null;
  statusCode: number | null;
  switchReason?: string;
  error?: string;
}

export interface RequestLog {
  id: string;
  startedAt: string;
  keyName: string;
  requestedModel: string;
  actualModel: string;
  status: 'success' | 'failed';
  durationMs: number;
  ttftMs: number | null;
  inputTokens: number;
  cacheTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  costUsd: number;
  switchCount: number;
  reasoningEffort?: string;
  thinkingBudget?: number;
  attempts?: RequestAttempt[];
}

export interface GatewaySettings {
  probeIntervalSeconds: number;
  failureThreshold: number;
  cooldownSeconds: number;
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
  expiresAt?: string;
}
