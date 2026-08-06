import { api, ApiError } from '../../lib/api';
import { useAsyncData } from '../../lib/hooks';
import type { UpstreamAccount, V2SiteDetail, V2SiteModel } from '../../lib/types';
import { adaptLegacyUpstreamSites, adaptV2UpstreamSites } from './siteAdapter';

export function useUpstreamSites() {
  return useAsyncData(async () => {
    const summaries = await loadV2Summaries();
    if (summaries.length > 0) {
      const [siteResources, publishedModels] = await Promise.all([
        Promise.all(summaries.map(async (summary) => {
          const [detail, models, account] = await Promise.all([
            loadResource<V2SiteDetail>('站点详情', () => api.v2Site(summary.id)),
            loadResource<V2SiteModel[]>('模型清单', () => api.v2SiteModels(summary.id)),
            loadResource<UpstreamAccount>('账户数据', () => api.siteAccount(summary.id)),
          ]);
          const error = [detail.error, models.error, account.error].filter(Boolean).join('；');
          return {
            summary,
            detail: detail.data,
            models: models.data ?? [],
            account: account.data,
            error,
          };
        })),
        api.v2PublishedModels().catch(() => []),
      ]);

      const detailsBySiteId = new Map<number, V2SiteDetail>();
      const modelsBySiteId = new Map<number, V2SiteModel[]>();
      const accountsBySiteId = new Map<number, UpstreamAccount>();
      const errorsBySiteId = new Map<number, string>();
      for (const resource of siteResources) {
        if (resource.detail) detailsBySiteId.set(resource.summary.id, resource.detail);
        modelsBySiteId.set(resource.summary.id, resource.models);
        if (resource.account) accountsBySiteId.set(resource.summary.id, resource.account);
        if (resource.error) errorsBySiteId.set(resource.summary.id, resource.error);
      }
      return adaptV2UpstreamSites({ summaries, detailsBySiteId, modelsBySiteId, accountsBySiteId, errorsBySiteId, publishedModels });
    }

    const [legacyItems, routes] = await Promise.all([
      api.upstreams(),
      api.routes().catch(() => []),
    ]);
    const accounts = await Promise.all(legacyItems.map(async (item) => {
      try {
        return [item.id, await api.upstreamAccount(item.id)] as const;
      } catch {
        return [item.id, null] as const;
      }
    }));
    const accountsByUpstreamId = new Map<number, UpstreamAccount>();
    for (const [id, account] of accounts) {
      if (account) accountsByUpstreamId.set(id, account);
    }
    return adaptLegacyUpstreamSites({ rawItems: legacyItems, accountsByUpstreamId, routes });
  }, []);
}

async function loadV2Summaries() {
  try {
    return await api.v2Sites();
  } catch (reason) {
    if (reason instanceof ApiError && (reason.status === 404 || reason.status === 405)) return [];
    throw reason;
  }
}

function errorMessage(reason: unknown): string {
  return reason instanceof Error ? reason.message : '读取失败';
}

async function loadResource<T>(label: string, loader: () => Promise<T>): Promise<{ data: T | null; error: string | null }> {
  try {
    return { data: await loader(), error: null };
  } catch (reason) {
    return { data: null, error: `${label}：${errorMessage(reason)}` };
  }
}
