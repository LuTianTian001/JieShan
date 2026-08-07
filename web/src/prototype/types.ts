import type {
  CatalogState,
  DownstreamKey,
  GatewaySettings,
  ModelRoute,
  ModelTarget,
  MonitorSnapshot,
  MonitorTargetHistory,
  PriceCatalog,
  RoutingHealth,
  RoutingProfile,
  Site,
  SiteAccountConnection,
  SiteCredential,
  SiteEndpoint,
  SitePlatformDetection,
  SiteRuntimeStatus,
  SiteUsageRecord,
  SystemHealthOverview,
  TokenJsonImportPreview,
} from '../lib/types';

export interface PrototypeRequestAttempt {
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
  wireProtocol: 'openai' | 'anthropic' | 'gemini';
  apiSurface:
    | 'openai.chat_completions'
    | 'openai.responses'
    | 'anthropic.messages'
    | 'gemini.generate_content';
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

export interface PrototypeRouteCandidate {
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
  wireProtocol: PrototypeRequestAttempt['wireProtocol'];
  apiSurface: PrototypeRequestAttempt['apiSurface'];
  credentials: Array<{
    id: number;
    name: string;
    position: number;
    runtimeState: string;
    coolingUntil: number | null;
  }>;
  initialEligibility: string;
  initialReason: string;
  disposition: string;
  dispositionReason: string;
  attemptCount: number;
  firstAttemptIndex: number | null;
  lastAttemptIndex: number | null;
}

export interface PrototypeQuotaLedger {
  id: number;
  eventType: string;
  reservedDeltaNanoUsd: number;
  usedDeltaNanoUsd: number;
  priceCatalogVersion: string;
  priceSku: string;
  createdAt: number;
}

export interface PrototypeRequestLog {
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
  apiSurface: PrototypeRequestAttempt['apiSurface'];
  reasoningEffort: string;
  thinkingBudgetTokens: number | null;
  stream: boolean;
  priceCatalogVersion: string;
  priceSku: string;
  reservationNanoUsd: number;
  billingMultiplierBPS: number;
  status: string;
  meteringStatus: 'pending' | 'metered' | 'unavailable' | 'not_applicable';
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
  finalAttempt: PrototypeRequestAttempt | null;
  routeCandidates: PrototypeRouteCandidate[];
  attempts: PrototypeRequestAttempt[];
  ledger: PrototypeQuotaLedger[];
}

export interface PrototypeState {
  authenticated: boolean;
  username: string;
  sessionExpiresAt: number;
  csrfToken: string;
  sites: Site[];
  endpoints: SiteEndpoint[];
  credentials: SiteCredential[];
  endpointCredentialIds: Record<string, number[]>;
  modelTargets: ModelTarget[];
  discoveredModels: Record<string, string[]>;
  routingProfiles: RoutingProfile[];
  routes: ModelRoute[];
  downstreamKeys: DownstreamKey[];
  downstreamSecrets: Record<number, string>;
  siteAccounts: Record<number, SiteAccountConnection>;
  siteUsage: Record<number, SiteUsageRecord[]>;
  sitePlatformDetections: Record<number, SitePlatformDetection>;
  siteRuntimeStatus: Record<number, SiteRuntimeStatus>;
  tokenImportPreviews: Record<string, TokenJsonImportPreview>;
  priceCatalogs: PriceCatalog[];
  catalogState: CatalogState;
  monitorSnapshot: MonitorSnapshot;
  monitorHistories: Record<string, MonitorTargetHistory>;
  routingHealth: Record<number, RoutingHealth>;
  requestLogs: PrototypeRequestLog[];
  settings: GatewaySettings;
  systemHealth: SystemHealthOverview;
  nextIds: Record<string, number>;
  failNextTargetIds: number[];
}

export interface PrototypeFailureOptions {
  targetId?: number;
  times?: number;
}

export interface PrototypeController {
  uninstall(): void;
  reset(): void;
  failNextRequest(options?: PrototypeFailureOptions | number): void;
  recoverTarget(targetId: number): void;
  getState(): PrototypeState;
}
