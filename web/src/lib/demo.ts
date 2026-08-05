import type {
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
  User,
  UpdateKeyInput,
  UpdateRouteInput,
  UpdateUpstreamInput,
} from './types';

interface DemoState {
  upstreams: Upstream[];
  routes: Route[];
  keys: DownstreamKey[];
  logs: RequestLog[];
  settings: GatewaySettings;
}

const STORAGE_KEY = 'jieshan.demo.state.v1';

function isoAgo(milliseconds: number): string {
  return new Date(Date.now() - milliseconds).toISOString();
}

function isoAhead(milliseconds: number): string {
  return new Date(Date.now() + milliseconds).toISOString();
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
      balanceSupported: false,
      usageSupported: false,
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
      balanceSupported: false,
      usageSupported: false,
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
      balanceSupported: false,
      usageSupported: false,
      models: [
        { id: '6', name: 'claude-sonnet-4-5', enabled: true, discoveredAt: isoAgo(33 * 60_000) },
      ],
    },
  ];

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
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored) return JSON.parse(stored) as DemoState;
  } catch {
    // A private browser window may reject storage access.
  }
  return seedState();
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
      balanceSupported: false,
      usageSupported: false,
      models: [],
    };
    state.upstreams.unshift(item);
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
