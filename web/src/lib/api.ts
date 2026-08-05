import { demo } from './demo';
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
  UpdateKeyInput,
  UpdateRouteInput,
  UpdateUpstreamInput,
  User,
} from './types';

const API_PREFIX = import.meta.env.VITE_API_PREFIX || '/api/v1';
const DEMO_KEY = 'jieshan.demo.enabled';
export const AUTH_EXPIRED_EVENT = 'jieshan:auth-expired';

export class ApiError extends Error {
  constructor(message: string, readonly status: number) {
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

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${API_PREFIX}${path}`, {
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
    try {
      const body = await response.json() as { message?: string; error?: string };
      message = body.message || body.error || message;
    } catch {
      // Keep the status-based message when the server did not return JSON.
    }
    if (response.status === 401 && !isDemoMode()) {
      window.dispatchEvent(new Event(AUTH_EXPIRED_EVENT));
    }
    throw new ApiError(message, response.status);
  }

  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
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
  async log(id: string): Promise<RequestLog> {
    if (isDemoMode()) return demo.log(id);
    return request<RequestLog>(`/logs/requests/${encodeURIComponent(id)}`);
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
