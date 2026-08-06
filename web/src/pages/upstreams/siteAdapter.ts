import type {
  AccountTarget,
  HealthState,
  Route,
  SourceAmount,
  Upstream,
  UpstreamAccount,
  UpstreamModel,
  V2Credential,
  V2Endpoint,
  V2EndpointCapabilities,
  V2PublishedModel,
  V2SiteDetail,
  V2SiteModel,
  V2SiteSummary,
} from '../../lib/types';
import { inferenceProtocolCapabilities } from '../../lib/inferenceProtocols';

export type UpstreamSourceVersion = 'legacy' | 'v2';

export interface SiteEndpointView {
  id: string;
  endpointId: number | null;
  name: string;
  baseUrl: string;
  protocol: string;
  compatibilityProfile: string;
  authScheme: string;
  capabilities: V2EndpointCapabilities;
  position: number;
  enabled: boolean;
  revision: number | null;
  v2: V2Endpoint | null;
}

export interface SiteCredentialView {
  id: string;
  credentialId: number | null;
  upstreamId: number | null;
  name: string;
  enabled: boolean;
  state: HealthState;
  runtimeState: string;
  modelCount: number | null;
  lastSyncAt: string | null;
  lastTestStatus: string | null;
  lastErrorMessage: string | null;
  cooldownUntil: string | null;
  secretConfigured: boolean;
  revision: number | null;
  protocol: string;
  legacy: Upstream | null;
  v2: V2Credential | null;
}

export interface SiteRouteAvailability {
  healthy: number;
  total: number;
  unknown: number;
  attention: number;
}

export interface UpstreamSiteView {
  id: string;
  siteId: number | null;
  siteRevision: number | null;
  sourceVersion: UpstreamSourceVersion;
  memberUpstreamIds: number[];
  name: string;
  dashboardUrl: string;
  baseUrl: string;
  origin: string;
  protocol: string;
  enabled: boolean;
  endpoints: SiteEndpointView[];
  credentials: SiteCredentialView[];
  models: UpstreamModel[];
  modelCount: number;
  activeModelCount: number;
  publishedModelCount: number;
  account: UpstreamAccount | null;
  accountTarget: AccountTarget;
  routeAvailability: SiteRouteAvailability;
  lastModelSyncAt: string | null;
  lastAccountSyncAt: string | null;
  loadError: string | null;
  raw: unknown;
}

export interface LegacySiteAdapterInput {
  rawItems: Upstream[];
  accountsByUpstreamId: Map<number, UpstreamAccount>;
  routes: Route[];
}

export interface V2SiteAdapterInput {
  summaries: V2SiteSummary[];
  detailsBySiteId: Map<number, V2SiteDetail>;
  modelsBySiteId: Map<number, V2SiteModel[]>;
  accountsBySiteId: Map<number, UpstreamAccount>;
  errorsBySiteId?: Map<number, string>;
  publishedModels: V2PublishedModel[];
}

export function adaptLegacyUpstreamSites({ rawItems, accountsByUpstreamId, routes }: LegacySiteAdapterInput): UpstreamSiteView[] {
  return adaptLegacySites(rawItems, accountsByUpstreamId, routes);
}

export function adaptV2UpstreamSites({ summaries, detailsBySiteId, modelsBySiteId, accountsBySiteId, errorsBySiteId, publishedModels }: V2SiteAdapterInput): UpstreamSiteView[] {
  return summaries.map((summary) => {
    const detail = detailsBySiteId.get(summary.id) ?? { site: summary, endpoints: [], credentials: [] };
    const endpoints = [...detail.endpoints].sort(byPosition).map(adaptV2Endpoint);
    const primaryEndpoint = endpoints.find((item) => item.enabled) ?? endpoints[0] ?? null;
    const credentials = [...detail.credentials].sort(byPosition).map((item) => adaptV2Credential(item, primaryEndpoint?.protocol ?? 'openai'));
    const models = (modelsBySiteId.get(summary.id) ?? []).map(adaptV2Model).sort((left, right) => left.name.localeCompare(right.name));
    const routeAvailability = v2RouteAvailability(summary.id, publishedModels);
    const baseUrl = primaryEndpoint?.baseUrl ?? '';
    const name = detail.site.name || summary.name;
    const dashboardUrl = detail.site.dashboardUrl || summary.dashboardUrl || '';
    const account = accountsBySiteId.get(summary.id) ?? null;

    return {
      id: String(summary.id),
      siteId: summary.id,
      siteRevision: detail.site.revision,
      sourceVersion: 'v2' as const,
      memberUpstreamIds: [],
      name,
      dashboardUrl,
      baseUrl,
      origin: canonicalOrigin(baseUrl || detail.site.dashboardUrl || summary.dashboardUrl || ''),
      protocol: endpointProtocolLabel(endpoints),
      enabled: detail.site.enabled,
      endpoints,
      credentials,
      models,
      modelCount: summary.modelCount || models.length,
      activeModelCount: summary.activeModelCount || models.filter((model) => model.enabled && !model.stale).length,
      publishedModelCount: summary.publishedModelCount,
      account,
      accountTarget: {
        kind: 'site' as const,
        id: summary.id,
        name,
        dashboardUrl: account?.dashboardUrl || dashboardUrl,
        baseUrl,
      },
      routeAvailability,
      lastModelSyncAt: timestampToIso(summary.lastModelSeenAt),
      lastAccountSyncAt: account ? latestDate([
        account.sync.lastSuccessAt,
        account.sync.lastAttemptAt,
        account.snapshot?.capturedAt ?? null,
      ]) : null,
      loadError: errorsBySiteId?.get(summary.id) ?? null,
      raw: { summary, detail, models: modelsBySiteId.get(summary.id) ?? [] },
    };
  }).sort(compareSites);
}

export function findSiteByMemberId(sites: UpstreamSiteView[], value: string | undefined): UpstreamSiteView | null {
  if (!value) return null;
  return sites.find((site) => site.id === value || site.memberUpstreamIds.some((id) => String(id) === value)) ?? null;
}

export function formatSourceAmount(amount: SourceAmount | null | undefined): string {
  if (!amount) return '未连接账户';
  return amount.display || `${amount.value} ${amount.currency}`;
}

function adaptLegacySites(items: Upstream[], accounts: Map<number, UpstreamAccount>, routes: Route[]): UpstreamSiteView[] {
  const groups = new Map<string, Upstream[]>();
  for (const item of items) {
    const key = canonicalOrigin(item.baseUrl);
    const current = groups.get(key) ?? [];
    current.push(item);
    groups.set(key, current);
  }

  return Array.from(groups.entries()).map<UpstreamSiteView>(([origin, members]) => {
    members.sort((left, right) => left.id - right.id);
    const ids = new Set(members.map((item) => item.id));
    const accountEntries = members
      .map((item) => ({ upstreamId: item.id, account: accounts.get(item.id) ?? null }))
      .filter((entry): entry is { upstreamId: number; account: UpstreamAccount } => Boolean(entry.account));
    const configuredAccounts = accountEntries.filter((entry) => entry.account.configured);
    const selectedAccount = configuredAccounts.sort((left, right) => accountTimestamp(right.account) - accountTimestamp(left.account))[0]
      ?? accountEntries[0]
      ?? null;
    const models = mergeModels(members.flatMap((item) => item.models ?? []));
    const protocols = Array.from(new Set(members.map((item) => item.protocol)));
    const primary = members[0];
    const endpoints = uniqueLegacyEndpoints(members);
    const name = deriveSiteName(members, origin);
    const accountHost = members.find((item) => item.id === selectedAccount?.upstreamId) ?? primary;

    return {
      id: String(primary.id),
      siteId: null,
      siteRevision: null,
      sourceVersion: 'legacy',
      memberUpstreamIds: members.map((item) => item.id),
      name,
      dashboardUrl: selectedAccount?.account.dashboardUrl ?? '',
      baseUrl: primary.baseUrl,
      origin,
      protocol: protocols.length === 1 ? protocols[0] : 'mixed',
      enabled: members.some((item) => item.enabled),
      endpoints,
      credentials: members.map((item, index) => ({
        id: `legacy-${item.id}`,
        credentialId: null,
        upstreamId: item.id,
        name: deriveCredentialName(item.name, members.length, index),
        enabled: item.enabled,
        state: item.enabled ? item.state : 'disabled',
        runtimeState: item.state,
        modelCount: item.modelCount,
        lastSyncAt: item.lastSyncAt,
        lastTestStatus: item.state === 'healthy' ? 'success' : item.lastError ? 'failed' : null,
        lastErrorMessage: item.lastError ?? null,
        cooldownUntil: null,
        secretConfigured: true,
        revision: null,
        protocol: item.protocol,
        legacy: item,
        v2: null,
      })),
      models,
      modelCount: models.length || Math.max(...members.map((item) => item.modelCount), 0),
      activeModelCount: models.filter((model) => model.enabled).length,
      publishedModelCount: 0,
      account: selectedAccount?.account ?? null,
      accountTarget: {
        kind: 'legacy',
        id: accountHost.id,
        name,
        dashboardUrl: selectedAccount?.account.dashboardUrl ?? '',
        baseUrl: accountHost.baseUrl,
      },
      routeAvailability: routeAvailability(ids, routes),
      lastModelSyncAt: latestDate(members.map((item) => item.lastSyncAt)),
      lastAccountSyncAt: selectedAccount ? latestDate([
        selectedAccount.account.sync.lastSuccessAt,
        selectedAccount.account.sync.lastAttemptAt,
        selectedAccount.account.snapshot?.capturedAt ?? null,
      ]) : null,
      loadError: null,
      raw: members,
    };
  }).sort(compareSites);
}

function adaptV2Endpoint(item: V2Endpoint): SiteEndpointView {
  return {
    id: String(item.id),
    endpointId: item.id,
    name: item.name,
    baseUrl: item.baseUrl,
    protocol: item.wireProtocol || 'openai',
    compatibilityProfile: item.compatibilityProfile || 'generic',
    authScheme: item.authScheme || 'bearer',
    capabilities: item.capabilities ?? inferenceProtocolCapabilities(item.wireProtocol),
    position: item.position,
    enabled: item.enabled,
    revision: item.revision,
    v2: item,
  };
}

function adaptV2Credential(item: V2Credential, protocol: string): SiteCredentialView {
  return {
    id: String(item.id),
    credentialId: item.id,
    upstreamId: null,
    name: item.name,
    enabled: item.enabled,
    state: credentialHealthState(item),
    runtimeState: item.runtimeState || 'unknown',
    modelCount: null,
    lastSyncAt: timestampToIso(item.lastTestAt),
    lastTestStatus: item.lastTestStatus || null,
    lastErrorMessage: item.lastErrorMessage || null,
    cooldownUntil: timestampToIso(item.cooldownUntil),
    secretConfigured: item.secretConfigured,
    revision: item.revision,
    protocol,
    legacy: null,
    v2: item,
  };
}

function adaptV2Model(item: V2SiteModel): UpstreamModel {
  return {
    id: String(item.id),
    name: item.modelName,
    displayName: item.displayName,
    enabled: item.enabled,
    discoveredAt: timestampToIso(item.lastSeenAt) ?? timestampToIso(item.updatedAt) ?? '',
    endpointId: item.endpointId,
    stale: item.stale,
    credentialCount: item.credentialCount,
    supportedCredentialCount: item.supportedCredentialCount,
    unsupportedCredentialCount: item.unsupportedCredentialCount,
    unknownCredentialCount: item.unknownCredentialCount,
  };
}

function credentialHealthState(item: V2Credential): HealthState {
  if (!item.enabled) return 'disabled';
  const state = item.runtimeState.toLowerCase();
  if (state === 'active' || state === 'healthy' || item.lastTestStatus === 'success') return 'healthy';
  if (state === 'cooldown') return 'cooldown';
  if (state === 'probing') return 'probing';
  if (state === 'suspect') return 'suspect';
  if (state === 'credential_error' || state === 'unavailable' || item.lastTestStatus === 'failed') return 'credential_error';
  return 'unknown';
}

function v2RouteAvailability(siteId: number, publishedModels: V2PublishedModel[]): SiteRouteAvailability {
  const targets = publishedModels
    .filter((model) => model.enabled && model.monitorEnabled)
    .flatMap((model) => model.targets)
    .filter((target) => target.enabled && target.siteId === siteId);
  return { healthy: 0, total: targets.length, unknown: targets.length, attention: 0 };
}

function routeAvailability(upstreamIds: Set<number>, routes: Route[]): SiteRouteAvailability {
  const targets = routes
    .filter((route) => route.enabled && route.monitored)
    .flatMap((route) => route.targets)
    .filter((target) => upstreamIds.has(target.upstreamId) && target.state !== 'disabled');
  const healthy = targets.filter((target) => target.state === 'healthy').length;
  const unknown = targets.filter((target) => target.state === 'unknown' || target.state === 'probing').length;
  return { healthy, total: targets.length, unknown, attention: Math.max(0, targets.length - healthy - unknown) };
}

function uniqueLegacyEndpoints(items: Upstream[]): SiteEndpointView[] {
  const found = new Map<string, SiteEndpointView>();
  for (const item of items) {
    const key = `${item.baseUrl}\u0000${item.protocol}`;
    if (found.has(key)) continue;
    found.set(key, {
      id: `legacy-endpoint-${item.id}`,
      endpointId: null,
      name: found.size ? `接入地址 ${found.size + 1}` : '主要接入地址',
      baseUrl: item.baseUrl,
      protocol: item.protocol,
      compatibilityProfile: 'legacy',
      authScheme: 'bearer',
      capabilities: inferenceProtocolCapabilities(item.protocol),
      position: found.size,
      enabled: item.enabled,
      revision: null,
      v2: null,
    });
  }
  return Array.from(found.values());
}

function mergeModels(models: UpstreamModel[]): UpstreamModel[] {
  const merged = new Map<string, UpstreamModel>();
  for (const model of models) {
    const current = merged.get(model.name);
    if (!current || (!current.enabled && model.enabled)) merged.set(model.name, model);
  }
  return Array.from(merged.values()).sort((left, right) => left.name.localeCompare(right.name));
}

function canonicalOrigin(value: string): string {
  try {
    const parsed = new URL(value);
    return parsed.origin.toLowerCase();
  } catch {
    return value.trim().replace(/\/$/, '').toLowerCase();
  }
}

function deriveSiteName(items: Upstream[], origin: string): string {
  if (items.length === 1) return items[0].name;
  const prefixes = items.map((item) => item.name.split(/\s*[/|｜]\s*/, 1)[0].trim()).filter(Boolean);
  if (prefixes.length === items.length && prefixes.every((value) => value === prefixes[0])) return prefixes[0];
  return hostLabel(origin);
}

function deriveCredentialName(name: string, total: number, index: number): string {
  if (total === 1) return '默认 Key';
  const parts = name.split(/\s*[/|｜]\s*/).map((value) => value.trim()).filter(Boolean);
  return parts.length > 1 ? parts.slice(1).join(' / ') : `Key ${index + 1}`;
}

function endpointProtocolLabel(endpoints: SiteEndpointView[]): string {
  const protocols = Array.from(new Set(endpoints.map((item) => item.protocol).filter(Boolean)));
  if (protocols.length === 0) return '未配置';
  return protocols.length === 1 ? protocols[0] : 'mixed';
}

function hostLabel(origin: string): string {
  try {
    return new URL(origin).hostname;
  } catch {
    return origin || '未命名站点';
  }
}

function accountTimestamp(account: UpstreamAccount): number {
  const value = account.snapshot?.capturedAt || account.sync.lastSuccessAt || account.sync.lastAttemptAt;
  return value ? new Date(value).getTime() : 0;
}

function latestDate(values: Array<string | null | undefined>): string | null {
  return values.reduce<string | null>((latest, value) => {
    if (!value) return latest;
    if (!latest || new Date(value).getTime() > new Date(latest).getTime()) return value;
    return latest;
  }, null);
}

function timestampToIso(value: number | null | undefined): string | null {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? new Date(value).toISOString() : null;
}

function compareSites(left: UpstreamSiteView, right: UpstreamSiteView): number {
  return left.name.localeCompare(right.name, 'zh-CN');
}

function byPosition<T extends { position: number; id: number }>(left: T, right: T): number {
  return left.position - right.position || left.id - right.id;
}
