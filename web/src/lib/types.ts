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

export interface BalanceSnapshot {
  amount: number;
  currency: string;
  plan?: string;
  renewalAt?: string;
  sourceLabel?: string;
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
  balance?: BalanceSnapshot;
  balanceSupported?: boolean;
  usageSupported?: boolean;
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
