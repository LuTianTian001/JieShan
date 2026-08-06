import type {
  AccountAdapter,
  AccountUsageRange,
  ConfigureUpstreamAccountInput,
  CreateKeyInput,
  CreateRouteInput,
  CreateUpstreamInput,
  DashboardSummary,
  DownstreamKey,
  GatewaySettings,
  ModelDiscovery,
  MonitorMatrix,
  RequestLog,
  Route,
  Upstream,
  UpstreamAccount,
  UpstreamUsageItem,
  UpstreamUsagePage,
  User,
  UpdateKeyInput,
  UpdateRouteInput,
  UpdateUpstreamInput,
} from './types';

interface DemoState {
  upstreams: Upstream[];
  accounts: Record<number, UpstreamAccount>;
  accountUsage: Record<number, UpstreamUsageItem[]>;
  routes: Route[];
  keys: DownstreamKey[];
  logs: RequestLog[];
  settings: GatewaySettings;
}

const STORAGE_KEY = 'jieshan.demo.state.v1';

const accountAdapterItems: AccountAdapter[] = [
  { key: 'new_api', label: 'New API', authKinds: ['api_token'], capabilities: { balance: true, subscription: true, usage: true, tokenRefresh: false } },
  { key: 'one_api', label: 'One API', authKinds: ['api_token'], capabilities: { balance: true, subscription: false, usage: true, tokenRefresh: false } },
  { key: 'ciii', label: 'Ciii', authKinds: ['access_refresh'], capabilities: { balance: true, subscription: true, usage: true, tokenRefresh: true } },
];

function emptyAccount(dashboardUrl = ''): UpstreamAccount {
  return {
    configured: false,
    enabled: false,
    dashboardUrl,
    capabilities: { balance: false, subscription: false, usage: false, tokenRefresh: false },
    sync: { state: 'unconfigured', lastAttemptAt: null, lastSuccessAt: null, nextAt: null, stale: false, error: null },
    snapshot: null,
  };
}

function isoAgo(milliseconds: number): string {
  return new Date(Date.now() - milliseconds).toISOString();
}

function isoAhead(milliseconds: number): string {
  return new Date(Date.now() + milliseconds).toISOString();
}

function dashboardURL(baseURL: string): string {
  try {
    return new URL(baseURL).origin;
  } catch {
    return baseURL.replace(/\/$/, '');
  }
}

function seedState(): DemoState {
  const upstreams: Upstream[] = [
    {
      id: 1,
      name: '主线路',
      baseUrl: 'https://api.primary.example/v1',
      protocol: 'openai',
      enabled: true,
      state: 'healthy',
      latencyMs: 684,
      modelCount: 8,
      credentialCount: 1,
      lastSyncAt: isoAgo(18 * 60_000),
      models: [
        { id: '1', name: 'claude-sonnet-4-5', enabled: true, discoveredAt: isoAgo(18 * 60_000) },
        { id: '2', name: 'gpt-5.2', enabled: true, discoveredAt: isoAgo(18 * 60_000) },
        { id: '3', name: 'deepseek-v3.1', enabled: true, discoveredAt: isoAgo(18 * 60_000) },
      ],
    },
    {
      id: 2,
      name: '备用线路 A',
      baseUrl: 'https://relay-a.example/api',
      protocol: 'compatible',
      enabled: true,
      state: 'suspect',
      latencyMs: 1428,
      modelCount: 12,
      credentialCount: 2,
      lastSyncAt: isoAgo(2 * 3_600_000),
      lastError: '最近一次探针首字节超时',
      models: [
        { id: '4', name: 'claude-sonnet-4-5', enabled: true, discoveredAt: isoAgo(2 * 3_600_000) },
        { id: '5', name: 'gpt-5.2', enabled: true, discoveredAt: isoAgo(2 * 3_600_000) },
      ],
    },
    {
      id: 3,
      name: '备用线路 B',
      baseUrl: 'https://relay-b.example/v1',
      protocol: 'compatible',
      enabled: true,
      state: 'cooldown',
      latencyMs: null,
      modelCount: 5,
      credentialCount: 1,
      lastSyncAt: isoAgo(33 * 60_000),
      lastError: '连续两次连接失败，等待半开探测',
      models: [
        { id: '6', name: 'claude-sonnet-4-5', enabled: true, discoveredAt: isoAgo(33 * 60_000) },
      ],
    },
  ];

  const accounts: Record<number, UpstreamAccount> = {
    1: {
      configured: true,
      enabled: true,
      dashboardUrl: 'https://api.primary.example',
      adapter: { key: 'new_api', label: 'New API' },
      auth: { kind: 'api_token', hasApiToken: true, hasAccessToken: false, hasRefreshToken: false, accessTokenExpiresAt: null },
      capabilities: { balance: true, subscription: true, usage: true, tokenRefresh: false },
      sync: { state: 'ready', lastAttemptAt: isoAgo(12 * 60_000), lastSuccessAt: isoAgo(12 * 60_000), nextAt: isoAhead(18 * 60_000), stale: false, error: null },
      snapshot: {
        capturedAt: isoAgo(12 * 60_000),
        balance: { value: '23.4700', currency: 'USD', display: '$23.47', sourceLabel: '站点余额' },
        subscription: { planName: 'Pro 月付', status: 'active', expiresAt: isoAhead(24 * 86_400_000), renewsAt: isoAhead(24 * 86_400_000), periodStart: isoAgo(6 * 86_400_000), periodEnd: isoAhead(24 * 86_400_000) },
      },
    },
    2: {
      configured: true,
      enabled: true,
      dashboardUrl: 'https://relay-a.example',
      adapter: { key: 'ciii', label: 'Ciii' },
      auth: { kind: 'access_refresh', hasApiToken: false, hasAccessToken: true, hasRefreshToken: true, accessTokenExpiresAt: isoAhead(42 * 60_000) },
      capabilities: { balance: true, subscription: true, usage: true, tokenRefresh: true },
      sync: { state: 'stale', lastAttemptAt: isoAgo(46 * 60_000), lastSuccessAt: isoAgo(2 * 3_600_000), nextAt: isoAhead(14 * 60_000), stale: true, error: { code: 'account_timeout', message: '最近一次账户同步超时，继续展示上次成功数据。' } },
      snapshot: {
        capturedAt: isoAgo(2 * 3_600_000),
        balance: { value: '104.2500', currency: 'CNY', display: '¥104.25', sourceLabel: '账户余额' },
        subscription: { planName: '月度订阅', status: 'active', expiresAt: isoAhead(11 * 86_400_000), renewsAt: null, periodStart: isoAgo(19 * 86_400_000), periodEnd: isoAhead(11 * 86_400_000), remaining: { value: '68.4', currency: 'percent', display: '剩余 68.4%' } },
      },
    },
    3: emptyAccount('https://relay-b.example'),
  };

  const accountUsage: Record<number, UpstreamUsageItem[]> = {
    1: [
      { id: 'usage-primary-1', externalId: 'primary-9021', occurredAt: isoAgo(9 * 60_000), model: 'claude-sonnet-4-5', amount: { value: '0.01482', currency: 'USD', display: '$0.01482' }, inputTokens: 2214, outputTokens: 638, status: 'success', sourceText: '站点原始扣费' },
      { id: 'usage-primary-2', externalId: 'primary-9017', occurredAt: isoAgo(41 * 60_000), model: 'gpt-5.2', amount: { value: '0.02810', currency: 'USD', display: '$0.02810' }, inputTokens: 3810, outputTokens: 946, status: 'success', sourceText: '站点原始扣费' },
      { id: 'usage-primary-3', externalId: 'primary-8982', occurredAt: isoAgo(8 * 3_600_000), model: 'deepseek-v3.1', amount: { value: '0.00360', currency: 'USD', display: '$0.00360' }, inputTokens: 1440, outputTokens: 512, status: 'success', sourceText: '站点原始扣费' },
      { id: 'usage-primary-4', externalId: 'primary-8711', occurredAt: isoAgo(3 * 86_400_000), model: 'claude-sonnet-4-5', amount: { value: '0.02100', currency: 'USD', display: '$0.02100' }, inputTokens: 2892, outputTokens: 816, status: 'success', sourceText: '站点原始扣费' },
    ],
    2: [
      { id: 'usage-relay-a-1', externalId: 'relay-a-482', occurredAt: isoAgo(22 * 60_000), model: 'claude-sonnet-4-5', amount: { value: '0.0840', currency: 'CNY', display: '¥0.0840' }, inputTokens: 1602, outputTokens: 428, status: 'success', sourceText: '套餐内记录' },
      { id: 'usage-relay-a-2', externalId: 'relay-a-479', occurredAt: isoAgo(5 * 3_600_000), model: 'gpt-5.2', amount: { value: '0.1900', currency: 'CNY', display: '¥0.1900' }, inputTokens: 4077, outputTokens: 1031, status: 'success', sourceText: '套餐内记录' },
      { id: 'usage-relay-a-3', externalId: 'relay-a-433', occurredAt: isoAgo(4 * 86_400_000), model: 'claude-sonnet-4-5', amount: { value: '0.1260', currency: 'CNY', display: '¥0.1260' }, inputTokens: 2940, outputTokens: 711, status: 'success', sourceText: '套餐内记录' },
    ],
    3: [],
  };

  const routes: Route[] = [
    {
      id: 101,
      model: 'claude-sonnet-4-5',
      displayName: 'Claude Sonnet 4.5',
      enabled: true,
      monitored: true,
      revision: 7,
      targets: [
        { id: 1001, upstreamId: 1, upstreamName: '主线路', credentialName: '默认密钥', sourceModel: 'claude-sonnet-4-5', state: 'healthy', latencyMs: 684, cooldownUntil: null },
        { id: 1002, upstreamId: 2, upstreamName: '备用线路 A', credentialName: '账号 01', sourceModel: 'claude-sonnet-4-5', state: 'suspect', latencyMs: 1428, cooldownUntil: null, lastFailure: '首字节超时' },
        { id: 1003, upstreamId: 3, upstreamName: '备用线路 B', credentialName: '默认密钥', sourceModel: 'claude-sonnet-4-5', state: 'cooldown', latencyMs: null, cooldownUntil: isoAhead(3 * 60_000 + 18_000), lastFailure: '连接失败' },
      ],
    },
    {
      id: 102,
      model: 'gpt-5.2',
      displayName: 'GPT-5.2',
      enabled: true,
      monitored: true,
      revision: 3,
      targets: [
        { id: 1004, upstreamId: 2, upstreamName: '备用线路 A', credentialName: '账号 02', sourceModel: 'gpt-5.2', state: 'healthy', latencyMs: 912, cooldownUntil: null },
        { id: 1005, upstreamId: 1, upstreamName: '主线路', credentialName: '默认密钥', sourceModel: 'gpt-5.2', state: 'healthy', latencyMs: 1040, cooldownUntil: null },
      ],
    },
    {
      id: 103,
      model: 'deepseek-v3.1',
      displayName: 'DeepSeek V3.1',
      enabled: true,
      monitored: false,
      revision: 1,
      targets: [
        { id: 1006, upstreamId: 1, upstreamName: '主线路', credentialName: '默认密钥', sourceModel: 'deepseek-v3.1', state: 'healthy', latencyMs: 731, cooldownUntil: null },
      ],
    },
  ];

  const keys: DownstreamKey[] = [
    { id: 201, name: '个人主密钥', prefix: 'sk-js-8F3A', enabled: true, quotaUsd: 50, spentUsd: 7.8642, allowedModels: [], rpmLimit: 60, expiresAt: null, lastUsedAt: isoAgo(3 * 60_000), createdAt: isoAgo(16 * 86_400_000) },
    { id: 202, name: '笔记本测试', prefix: 'sk-js-20D1', enabled: true, quotaUsd: 5, spentUsd: 1.1038, allowedModels: ['claude-sonnet-4-5'], rpmLimit: 20, expiresAt: isoAhead(18 * 86_400_000), lastUsedAt: isoAgo(8 * 3_600_000), createdAt: isoAgo(5 * 86_400_000) },
  ];

  const logs: RequestLog[] = [
    {
      id: 'req_01K22H8SJ3Q2', startedAt: isoAgo(3 * 60_000), keyName: '个人主密钥', requestedModel: 'claude-sonnet-4-5', actualModel: 'claude-sonnet-4-5', status: 'success', durationMs: 4821, ttftMs: 721, inputTokens: 1832, cacheTokens: 1044, outputTokens: 616, reasoningTokens: 0, costUsd: 0.0117, switchCount: 0, reasoningEffort: 'medium',
      attempts: [{ id: 1, sequence: 1, upstreamName: '主线路', model: 'claude-sonnet-4-5', state: 'success', startedAt: isoAgo(3 * 60_000), durationMs: 4821, ttftMs: 721, statusCode: 200 }],
    },
    {
      id: 'req_01K22H4W9P8D', startedAt: isoAgo(11 * 60_000), keyName: '个人主密钥', requestedModel: 'gpt-5.2', actualModel: 'gpt-5.2', status: 'success', durationMs: 6388, ttftMs: 1062, inputTokens: 4210, cacheTokens: 0, outputTokens: 908, reasoningTokens: 1280, costUsd: 0.0346, switchCount: 1, reasoningEffort: 'high', thinkingBudget: 4096,
      attempts: [
        { id: 2, sequence: 1, upstreamName: '备用线路 A', model: 'gpt-5.2', state: 'failed', startedAt: isoAgo(11 * 60_000), durationMs: 1800, ttftMs: null, statusCode: 504, switchReason: '首字节超时，切换下一目标', error: 'upstream timeout' },
        { id: 3, sequence: 2, upstreamName: '主线路', model: 'gpt-5.2', state: 'success', startedAt: isoAgo(11 * 60_000 - 1_830), durationMs: 4558, ttftMs: 1062, statusCode: 200 },
      ],
    },
    {
      id: 'req_01K22GVR6QY8', startedAt: isoAgo(36 * 60_000), keyName: '笔记本测试', requestedModel: 'claude-sonnet-4-5', actualModel: 'claude-sonnet-4-5', status: 'failed', durationMs: 5014, ttftMs: null, inputTokens: 744, cacheTokens: 0, outputTokens: 0, reasoningTokens: 0, costUsd: 0, switchCount: 2, reasoningEffort: 'low',
      attempts: [
        { id: 4, sequence: 1, upstreamName: '主线路', model: 'claude-sonnet-4-5', state: 'failed', startedAt: isoAgo(36 * 60_000), durationMs: 2110, ttftMs: null, statusCode: 503, switchReason: '上游暂不可用' },
        { id: 5, sequence: 2, upstreamName: '备用线路 A', model: 'claude-sonnet-4-5', state: 'failed', startedAt: isoAgo(36 * 60_000 - 2_130), durationMs: 1810, ttftMs: null, statusCode: 504, switchReason: '首字节超时' },
        { id: 6, sequence: 3, upstreamName: '备用线路 B', model: 'claude-sonnet-4-5', state: 'failed', startedAt: isoAgo(36 * 60_000 - 3_960), durationMs: 1054, ttftMs: null, statusCode: null, error: 'connection refused' },
      ],
    },
  ];

  return {
    upstreams,
    accounts,
    accountUsage,
    routes,
    keys,
    logs,
    settings: {
      probeIntervalSeconds: 300,
      failureThreshold: 2,
      cooldownSeconds: 300,
      requestTimeoutSeconds: 120,
      maxAttempts: 3,
      logRetentionDays: 30,
      priceCatalogVersion: '2026.08.01',
      priceCatalogUpdatedAt: isoAgo(5 * 86_400_000),
      priceCatalogSource: 'JieShan 官方价格目录',
      lastBackupAt: isoAgo(2 * 86_400_000),
    },
  };
}

function loadState(): DemoState {
  const seeded = seedState();
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored) {
      const parsed = JSON.parse(stored) as Partial<DemoState>;
      return {
        ...seeded,
        ...parsed,
        upstreams: parsed.upstreams ?? seeded.upstreams,
        accounts: { ...seeded.accounts, ...(parsed.accounts ?? {}) },
        accountUsage: { ...seeded.accountUsage, ...(parsed.accountUsage ?? {}) },
        routes: parsed.routes ?? seeded.routes,
        keys: parsed.keys ?? seeded.keys,
        logs: parsed.logs ?? seeded.logs,
        settings: { ...seeded.settings, ...(parsed.settings ?? {}) },
      };
    }
  } catch {
    // A private browser window may reject storage access.
  }
  return seeded;
}

let state = loadState();

function save(): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
  } catch {
    // Demo mode still works in memory.
  }
}

function copy<T>(value: T): T {
  return structuredClone(value);
}

function usageWindow(range: AccountUsageRange): number {
  if (range === '24h') return 86_400_000;
  if (range === '7d') return 7 * 86_400_000;
  return 30 * 86_400_000;
}

function snapshotFor(adapterKey: AccountAdapter['key']): NonNullable<UpstreamAccount['snapshot']> {
  const currency = adapterKey === 'ciii' ? 'CNY' : 'USD';
  return {
    capturedAt: new Date().toISOString(),
    balance: adapterKey === 'ciii'
      ? { value: '104.2500', currency, display: '¥104.25', sourceLabel: '账户余额' }
      : { value: '23.4700', currency, display: '$23.47', sourceLabel: '站点余额' },
    subscription: adapterKey === 'one_api' ? null : {
      planName: adapterKey === 'ciii' ? '月度订阅' : 'Pro 月付',
      status: 'active',
      expiresAt: isoAhead(21 * 86_400_000),
      renewsAt: adapterKey === 'ciii' ? null : isoAhead(21 * 86_400_000),
      periodStart: isoAgo(9 * 86_400_000),
      periodEnd: isoAhead(21 * 86_400_000),
    },
  };
}

function refreshDemoAccount(id: number): UpstreamAccount {
  const account = state.accounts[id];
  if (!account?.configured) throw new Error('账户观测尚未配置');
  if (!account.enabled) throw new Error('账户观测已停用');
  const now = new Date().toISOString();
  account.snapshot = account.snapshot ?? snapshotFor(account.adapter?.key ?? 'new_api');
  account.snapshot.capturedAt = now;
  account.sync = { state: 'ready', lastAttemptAt: now, lastSuccessAt: now, nextAt: isoAhead(30 * 60_000), stale: false, error: null };
  save();
  return copy(account);
}

export const demo = {
  user(): User {
    return { id: 1, username: 'JieShan' };
  },
  dashboard(): DashboardSummary {
    const monitored = state.routes.filter((route) => route.monitored);
    const targets = monitored.flatMap((route) => route.targets);
    return {
      monitoredModels: monitored.length,
      healthyModels: monitored.filter((route) => route.targets.some((target) => target.state === 'healthy')).length,
      attentionTargets: targets.filter((target) => ['suspect', 'credential_error'].includes(target.state)).length,
      coolingTargets: targets.filter((target) => target.state === 'cooldown').length,
      successRate24h: 99.42,
      requests24h: 386,
    };
  },
  monitor(): MonitorMatrix {
    return { generatedAt: new Date().toISOString(), probeIntervalSeconds: state.settings.probeIntervalSeconds, routes: copy(state.routes) };
  },
  upstreams(): Upstream[] {
    return copy(state.upstreams);
  },
  accountAdapters(): AccountAdapter[] {
    return copy(accountAdapterItems);
  },
  upstreamAccount(id: number): UpstreamAccount {
    const upstream = state.upstreams.find((entry) => entry.id === id);
    if (!upstream) throw new Error('上游不存在');
    state.accounts[id] ??= emptyAccount(dashboardURL(upstream.baseUrl));
    return copy(state.accounts[id]);
  },
  configureUpstreamAccount(id: number, input: ConfigureUpstreamAccountInput): UpstreamAccount {
    const upstream = state.upstreams.find((entry) => entry.id === id);
    if (!upstream) throw new Error('上游不存在');
    const adapter = accountAdapterItems.find((entry) => entry.key === input.adapterKey);
    if (!adapter) throw new Error('账户适配器不存在');
    if (!adapter.authKinds.includes(input.auth.kind)) throw new Error('该适配器不支持所选认证方式');
    const previous = state.accounts[id] ?? emptyAccount(dashboardURL(upstream.baseUrl));
    const previousAuth = previous.auth?.kind === input.auth.kind ? previous.auth : undefined;
    const auth = input.auth.kind === 'api_token'
      ? {
          kind: 'api_token' as const,
          hasApiToken: Boolean(input.auth.apiToken?.trim()) || Boolean(previousAuth?.hasApiToken),
          hasAccessToken: false,
          hasRefreshToken: false,
          accessTokenExpiresAt: null,
        }
      : {
          kind: 'access_refresh' as const,
          hasApiToken: false,
          hasAccessToken: Boolean(input.auth.accessToken?.trim()) || Boolean(previousAuth?.hasAccessToken),
          hasRefreshToken: Boolean(input.auth.refreshToken?.trim()) || Boolean(previousAuth?.hasRefreshToken),
          accessTokenExpiresAt: previousAuth?.accessTokenExpiresAt ?? isoAhead(60 * 60_000),
        };
    if (auth.kind === 'api_token' && !auth.hasApiToken) throw new Error('请输入 API Token');
    if (auth.kind === 'access_refresh' && (!auth.hasAccessToken || !auth.hasRefreshToken)) throw new Error('请输入 Access Token 和 Refresh Token');
    state.accounts[id] = {
      configured: true,
      enabled: input.enabled,
      dashboardUrl: input.dashboardUrl.trim().replace(/\/$/, ''),
      adapter: { key: adapter.key, label: adapter.label },
      auth,
      capabilities: copy(adapter.capabilities),
      sync: { state: 'ready', lastAttemptAt: previous.sync.lastAttemptAt, lastSuccessAt: previous.sync.lastSuccessAt, nextAt: previous.sync.nextAt, stale: false, error: null },
      snapshot: previous.adapter?.key === adapter.key ? previous.snapshot : null,
    };
    save();
    return input.refreshNow && input.enabled ? refreshDemoAccount(id) : copy(state.accounts[id]);
  },
  deleteUpstreamAccount(id: number): void {
    const upstream = state.upstreams.find((entry) => entry.id === id);
    if (!upstream) throw new Error('上游不存在');
    state.accounts[id] = emptyAccount(dashboardURL(upstream.baseUrl));
    state.accountUsage[id] = [];
    save();
  },
  refreshUpstreamAccount(id: number): UpstreamAccount {
    return refreshDemoAccount(id);
  },
  upstreamAccountUsage(id: number, range: AccountUsageRange, limit: number): UpstreamUsagePage {
    const account = state.accounts[id];
    if (!account?.configured) throw new Error('账户观测尚未配置');
    const cutoff = Date.now() - usageWindow(range);
    const items = (state.accountUsage[id] ?? [])
      .filter((item) => item.occurredAt == null || new Date(item.occurredAt).getTime() >= cutoff)
      .slice(0, Math.min(200, Math.max(1, limit)));
    return { items: copy(items), range, lastSyncedAt: account.sync.lastSuccessAt };
  },
  createUpstream(input: CreateUpstreamInput): Upstream {
    const item: Upstream = {
      id: Math.max(0, ...state.upstreams.map((entry) => entry.id)) + 1,
      name: input.name,
      baseUrl: input.baseUrl.replace(/\/$/, ''),
      protocol: input.protocol,
      enabled: true,
      state: 'unknown',
      latencyMs: null,
      modelCount: 0,
      credentialCount: 1,
      lastSyncAt: null,
      models: [],
    };
    state.upstreams.unshift(item);
    state.accounts[item.id] = emptyAccount(dashboardURL(item.baseUrl));
    state.accountUsage[item.id] = [];
    save();
    return copy(item);
  },
  updateUpstream(id: number, patch: UpdateUpstreamInput): Upstream {
    const item = state.upstreams.find((entry) => entry.id === id);
    if (!item) throw new Error('上游不存在');
    item.name = patch.name;
    item.baseUrl = patch.baseUrl.replace(/\/$/, '');
    item.protocol = patch.protocol;
    item.enabled = patch.enabled;
    item.state = patch.enabled ? 'unknown' : 'disabled';
    item.latencyMs = null;
    item.lastError = undefined;
    for (const route of state.routes) {
      route.targets = route.targets.map((target) => target.upstreamId === item.id ? { ...target, upstreamName: item.name, state: item.enabled ? 'unknown' : 'disabled', latencyMs: null } : target);
    }
    save();
    return copy(item);
  },
  deleteUpstream(id: number): void {
    const before = state.upstreams.length;
    state.upstreams = state.upstreams.filter((entry) => entry.id !== id);
    if (before === state.upstreams.length) throw new Error('上游不存在');
    delete state.accounts[id];
    delete state.accountUsage[id];
    state.routes = state.routes.map((route) => ({ ...route, revision: route.targets.some((target) => target.upstreamId === id) ? route.revision + 1 : route.revision, targets: route.targets.filter((target) => target.upstreamId !== id) }));
    save();
  },
  testUpstream(id: number): Upstream {
    const item = state.upstreams.find((entry) => entry.id === id);
    if (!item) throw new Error('上游不存在');
    item.state = 'healthy';
    item.latencyMs = 520 + Math.round(Math.random() * 480);
    item.lastError = undefined;
    save();
    return copy(item);
  },
  discoverModels(id: number): ModelDiscovery {
    const item = state.upstreams.find((entry) => entry.id === id);
    if (!item) throw new Error('上游不存在');
    return {
      upstreamId: id,
      discoveredAt: new Date().toISOString(),
      added: item.modelCount ? ['gemini-2.5-pro'] : ['claude-sonnet-4-5', 'gpt-5.2', 'gemini-2.5-pro'],
      removed: [],
      unchanged: item.models?.map((model) => model.name) ?? [],
      complete: true,
    };
  },
  applyModels(id: number, discovery: ModelDiscovery): Upstream {
    const item = state.upstreams.find((entry) => entry.id === id);
    if (!item) throw new Error('上游不存在');
    const names = new Set([...(item.models?.map((model) => model.name) ?? []), ...discovery.added]);
    item.models = [...names].map((name, index) => ({ id: `${id}-${index + 1}`, name, enabled: true, discoveredAt: discovery.discoveredAt }));
    item.modelCount = item.models.length;
    item.lastSyncAt = discovery.discoveredAt;
    save();
    return copy(item);
  },
  routes(): Route[] {
    return copy(state.routes);
  },
  createRoute(input: CreateRouteInput): Route {
    const id = Math.max(100, ...state.routes.map((entry) => entry.id)) + 1;
    const nextTargetId = Math.max(1000, ...state.routes.flatMap((entry) => entry.targets.map((target) => target.id))) + 1;
    const route: Route = {
      id,
      model: input.model,
      displayName: input.displayName?.trim() || undefined,
      enabled: true,
      monitored: input.monitored,
      revision: 1,
      targets: input.targets.map((target, index) => {
        const upstream = state.upstreams.find((entry) => entry.id === target.upstreamId);
        if (!upstream) throw new Error('上游不存在');
        return {
          id: nextTargetId + index,
          upstreamId: upstream.id,
          upstreamName: upstream.name,
          credentialName: '默认密钥',
          sourceModel: target.sourceModel,
          state: upstream.state,
          latencyMs: upstream.latencyMs,
          cooldownUntil: null,
        };
      }),
    };
    state.routes.unshift(route);
    save();
    return copy(route);
  },
  updateRoute(id: number, patch: UpdateRouteInput): Route {
    const route = state.routes.find((entry) => entry.id === id);
    if (!route) throw new Error('路由不存在');
    if (patch.model !== undefined) route.model = patch.model;
    if (patch.displayName !== undefined) route.displayName = patch.displayName;
    if (patch.enabled !== undefined) route.enabled = patch.enabled;
    if (patch.monitored !== undefined) route.monitored = patch.monitored;
    if (patch.targetModelIds !== undefined) {
      route.targets = patch.targetModelIds.map((modelId, index) => {
        const located = state.upstreams.flatMap((upstream) => upstream.models?.map((model) => ({ upstream, model })) ?? []).find((entry) => Number(entry.model.id) === modelId);
        if (!located) throw new Error(`上游模型 ${modelId} 不存在`);
        return {
          id: Math.max(1000, ...state.routes.flatMap((entry) => entry.targets.map((target) => target.id))) + index + 1,
          upstreamId: located.upstream.id,
          upstreamName: located.upstream.name,
          credentialName: '默认密钥',
          sourceModel: located.model.name,
          state: located.upstream.enabled ? located.upstream.state : 'disabled',
          latencyMs: located.upstream.latencyMs,
          cooldownUntil: null,
        };
      });
    }
    route.revision += 1;
    save();
    return copy(route);
  },
  deleteRoute(id: number): void {
    const before = state.routes.length;
    state.routes = state.routes.filter((entry) => entry.id !== id);
    if (before === state.routes.length) throw new Error('路由不存在');
    save();
  },
  reorderRoute(id: number, targetIds: number[]): Route {
    const route = state.routes.find((entry) => entry.id === id);
    if (!route) throw new Error('路由不存在');
    const byId = new Map(route.targets.map((target) => [target.id, target]));
    route.targets = targetIds.map((targetId) => byId.get(targetId)).filter((target): target is NonNullable<typeof target> => Boolean(target));
    route.revision += 1;
    save();
    return copy(route);
  },
  probeRoute(id: number, targetId?: number): Route {
    const route = state.routes.find((entry) => entry.id === id);
    if (!route) throw new Error('路由不存在');
    route.targets = route.targets.map((target) => targetId && target.id !== targetId ? target : {
      ...target,
      state: 'healthy',
      latencyMs: 480 + Math.round(Math.random() * 620),
      cooldownUntil: null,
      lastFailure: undefined,
    });
    save();
    return copy(route);
  },
  keys(): DownstreamKey[] {
    return copy(state.keys);
  },
  createKey(input: CreateKeyInput): { item: DownstreamKey; secret: string } {
    const id = Math.max(0, ...state.keys.map((entry) => entry.id)) + 1;
    const suffix = crypto.randomUUID().replace(/-/g, '').slice(0, 28);
    const item: DownstreamKey = {
      id,
      name: input.name,
      prefix: `sk-js-${suffix.slice(0, 4).toUpperCase()}`,
      enabled: true,
      quotaUsd: input.quotaUsd,
      spentUsd: 0,
      allowedModels: input.allowedModels,
      rpmLimit: input.rpmLimit,
      expiresAt: input.expiresAt,
      lastUsedAt: null,
      createdAt: new Date().toISOString(),
    };
    state.keys.unshift(item);
    save();
    return { item: copy(item), secret: `sk-js-${suffix}` };
  },
  updateKey(id: number, patch: UpdateKeyInput): DownstreamKey {
    const item = state.keys.find((entry) => entry.id === id);
    if (!item) throw new Error('密钥不存在');
    if (patch.clearQuota) item.quotaUsd = null;
    const { clearQuota: _clearQuota, ...values } = patch;
    Object.assign(item, values);
    save();
    return copy(item);
  },
  deleteKey(id: number): void {
    const before = state.keys.length;
    state.keys = state.keys.filter((entry) => entry.id !== id);
    if (before === state.keys.length) throw new Error('密钥不存在');
    save();
  },
  logs(): RequestLog[] {
    return copy(state.logs);
  },
  log(id: string): RequestLog {
    const item = state.logs.find((entry) => entry.id === id);
    if (!item) throw new Error('日志不存在');
    return copy(item);
  },
  settings(): GatewaySettings {
    return copy(state.settings);
  },
  updateSettings(patch: Partial<GatewaySettings>): GatewaySettings {
    state.settings = { ...state.settings, ...patch };
    save();
    return copy(state.settings);
  },
};
