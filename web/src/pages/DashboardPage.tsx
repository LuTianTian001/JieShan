import {
  Activity,
  ArrowRight,
  CircleDollarSign,
  HeartPulse,
  RefreshCw,
  Route as RouteIcon,
  Server,
  TimerReset,
} from 'lucide-react';
import { useEffect } from 'react';
import { Link } from 'react-router-dom';
import {
  Badge,
  Button,
  ErrorState,
  HealthBadge,
  LoadingState,
  MetricCard,
  PageHeader,
  Panel,
  SectionHeader,
} from '../components/ui';
import { api, ApiUnavailableError } from '../lib/api';
import { formatDateTime, formatRelativeTime, formatUSDFromNano } from '../lib/format';
import { useResource } from '../lib/hooks';
import { groupUpstreamTargets, type UpstreamGroup } from '../lib/upstreamGroups';
import type {
  CatalogState,
  DownstreamKey,
  GatewayLog,
  GatewayLogPage,
  GatewayLogSummary,
  ModelTarget,
  MonitorHealth,
  MonitorModel,
  MonitorSnapshot,
  Site,
  SiteAccountConnection,
} from '../lib/types';
import '../styles/overview-sites-polish.css';

interface DashboardData {
  sites: Site[];
  targets: ModelTarget[];
  keys: DownstreamKey[];
  accounts: Record<number, SiteAccountConnection | null>;
  pricing: CatalogState | null;
  monitor: MonitorSnapshot | null;
  summary24h: GatewayLogSummary | null;
  summaryToday: GatewayLogSummary | null;
  recentLogs: GatewayLogPage | null;
}

async function optionalResource<T>(loader: () => Promise<T>): Promise<T | null> {
  try {
    return await loader();
  } catch (error) {
    if (error instanceof ApiUnavailableError) return null;
    throw error;
  }
}

async function loadDashboard(): Promise<DashboardData> {
  const now = Date.now();
  const startOfToday = new Date(now);
  startOfToday.setHours(0, 0, 0, 0);
  const [sites, targets, keys, pricing, monitor, summary24h, summaryToday, recentLogs] = await Promise.all([
    api.listSites(),
    api.listModelTargets(),
    api.listDownstreamKeys(),
    optionalResource(() => api.pricingState()),
    optionalResource(() => api.monitorSnapshot()),
    optionalResource(() => api.gatewayLogSummary({ from: now - 24 * 60 * 60 * 1_000, to: now })),
    optionalResource(() => api.gatewayLogSummary({ from: startOfToday.getTime(), to: now })),
    optionalResource(() => api.gatewayLogs({ from: now - 24 * 60 * 60 * 1_000, to: now, limit: 20 })),
  ]);
  const accountEntries = await Promise.all(sites.map(async (site) => [site.id, await optionalResource(() => api.getSiteAccount(site.id))] as const));
  return { sites, targets, keys, accounts: Object.fromEntries(accountEntries), pricing, monitor, summary24h, summaryToday, recentLogs };
}

function basisPoints(value: number): string {
  return `${(value / 100).toLocaleString('zh-CN', { minimumFractionDigits: 1, maximumFractionDigits: 2 })}%`;
}

function successTone(summary: GatewayLogSummary | null): 'neutral' | 'success' | 'warning' | 'danger' {
  if (!summary?.requests) return 'neutral';
  if (summary.successBasisPoints >= 9_900) return 'success';
  if (summary.successBasisPoints >= 9_500) return 'warning';
  return 'danger';
}

const attentionStates = new Set<MonitorHealth>([
  'degraded',
  'unavailable',
  'suspect',
  'cooling',
  'recovering',
  'no_credentials',
  'unsupported',
]);

const routeReadyStates = new Set<MonitorHealth>(['healthy', 'degraded', 'suspect', 'recovering']);

function targetIsRouteReady(target: MonitorModel['targets'][number], routableProviderTargetIds: Set<number>): boolean {
  return target.enabled
    && target.usableCredentialCount > 0
    && routableProviderTargetIds.has(target.providerModelTargetId)
    && routeReadyStates.has(target.status);
}

function targetGroupHealth(
  group: UpstreamGroup<MonitorModel['targets'][number]>,
  routableProviderTargetIds: Set<number>,
): MonitorHealth {
  const targets = group.targets.filter((target) => target.enabled);
  if (!targets.length) return 'disabled';
  const routeReady = targets.filter((target) => targetIsRouteReady(target, routableProviderTargetIds));
  if (routeReady.length === targets.length && targets.every((target) => target.status === 'healthy')) return 'healthy';
  if (routeReady.length) return 'degraded';
  if (targets.some((target) => target.status === 'cooling')) return 'cooling';
  if (targets.some((target) => target.status === 'unavailable' || target.status === 'no_credentials' || target.status === 'unsupported')) return 'unavailable';
  if (targets.some((target) => attentionStates.has(target.status))) return 'degraded';
  return targets[0]?.status || 'unprobed';
}

function effectiveModelHealth(model: MonitorModel, routableProviderTargetIds: Set<number>): MonitorHealth {
  const states = groupUpstreamTargets(model.targets.filter((target) => target.enabled))
    .map((group) => targetGroupHealth(group, routableProviderTargetIds));
  if (states.includes('cooling')) return 'cooling';
  if (states.some((state) => state === 'unavailable' || state === 'no_credentials' || state === 'unsupported')) return 'unavailable';
  if (states.some((state) => attentionStates.has(state))) return 'degraded';
  return model.status;
}

function healthClass(state: MonitorHealth): string {
  if (state === 'healthy') return 'is-healthy';
  if (state === 'cooling' || state === 'recovering' || state === 'suspect' || state === 'degraded') return 'is-warning';
  if (state === 'unavailable' || state === 'no_credentials' || state === 'unsupported') return 'is-danger';
  return 'is-neutral';
}

function logIsSuccessful(log: GatewayLog): boolean {
  return log.status === 'success' || log.status === 'settled';
}

export function DashboardPage() {
  const resource = useResource(loadDashboard, []);

  useEffect(() => {
    const timer = window.setInterval(() => void resource.refresh(), 30_000);
    return () => window.clearInterval(timer);
  }, [resource.refresh]);

  if (resource.loading && !resource.data) return <div className="page"><LoadingState label="正在汇总网关运行状态" /></div>;
  if (resource.error && !resource.data) return <div className="page"><ErrorState message={resource.error} onRetry={() => void resource.refresh()} /></div>;
  if (!resource.data) return null;

  const { sites, targets, keys, accounts, pricing, monitor, summary24h, summaryToday, recentLogs } = resource.data;
  const monitoredModels = (monitor?.items || []).filter((model) => model.monitor.enabled);
  const routableProviderTargetIds = new Set(targets.filter((target) => target.routable).map((target) => target.id));
  const orderedModels = [...monitoredModels].sort((left, right) => {
    const leftHealthy = effectiveModelHealth(left, routableProviderTargetIds) === 'healthy' ? 1 : 0;
    const rightHealthy = effectiveModelHealth(right, routableProviderTargetIds) === 'healthy' ? 1 : 0;
    return leftHealthy - rightHealthy || left.publicModel.localeCompare(right.publicModel);
  });
  const routableModels = monitoredModels.filter((model) => model.publishedModelEnabled
    && groupUpstreamTargets(model.targets.filter((target) => target.enabled))
      .some((group) => group.targets.some((target) => targetIsRouteReady(target, routableProviderTargetIds)))).length;
  const now = Date.now();
  const monitoredSites = new Set(monitoredModels.flatMap((model) => model.targets.filter((target) => target.enabled).map((target) => target.siteId)));
  const coolingRouteGroups = monitoredModels.flatMap((model) => groupUpstreamTargets(model.targets.filter((target) => target.enabled))
    .filter((group) => group.targets.some((target) => target.status === 'cooling' || (target.health?.cooldownUntil || 0) > now))
    .map((group) => ({ modelId: model.publishedModelId, group })));
  const coolingSiteCount = new Set(coolingRouteGroups.map(({ group }) => group.siteId)).size;
  const routeEvents = (recentLogs?.items || [])
    .filter((log) => log.switchCount > 0 || !logIsSuccessful(log))
    .sort((left, right) => right.startedAt - left.startedAt)
    .slice(0, 6);
  const activeKeys = keys.filter((key) => key.enabled).length;

  return (
    <div className="page dashboard-page">
      <PageHeader
        title="总览"
        description="只看当前最重要的事情：上游是否可用、模型是否健康、路由是否发生切换以及下游实际花费。"
        meta={<Badge tone={resource.error ? 'warning' : 'neutral'}>{resource.error ? '部分数据刷新失败' : '30 秒自动刷新'}</Badge>}
        actions={(
          <>
            <Button icon={RefreshCw} busy={resource.loading} onClick={() => void resource.refresh()}>刷新</Button>
            <Link className="button button-primary button-md" to="/monitor">查看监控<ArrowRight size={16} /></Link>
          </>
        )}
      />

      <div className="metric-grid dashboard-metric-grid">
        <MetricCard
          icon={Activity}
          label="24 小时成功率"
          value={summary24h?.requests ? basisPoints(summary24h.successBasisPoints) : '-'}
          detail={summary24h?.requests ? `${summary24h.succeeded} 成功 · ${summary24h.failed} 失败 · ${summary24h.requests} 次请求` : '过去 24 小时暂无请求'}
          tone={successTone(summary24h)}
        />
        <MetricCard
          icon={HeartPulse}
          label="受监控模型"
          value={monitor ? monitoredModels.length || '-' : '-'}
          detail={!monitor ? '监控数据暂不可用' : monitoredModels.length ? `${routableModels} / ${monitoredModels.length} 个模型可路由 · ${monitoredSites.size} 个独立站点` : '尚未选择需要监控的模型'}
          tone={!monitor ? 'warning' : !monitoredModels.length ? 'neutral' : routableModels === monitoredModels.length ? 'success' : routableModels ? 'warning' : 'danger'}
        />
        <MetricCard
          icon={TimerReset}
          label="冷却中的站点"
          value={coolingSiteCount}
          detail={coolingSiteCount ? `影响 ${coolingRouteGroups.length} 个模型路由项，请求会自动尝试下一站` : '当前没有上游站点处于冷却'}
          tone={coolingSiteCount ? 'warning' : 'success'}
        />
        <MetricCard
          icon={CircleDollarSign}
          label="今日官方费用"
          value={summaryToday ? formatUSDFromNano(summaryToday.totalOfficialNanoUsd) : '-'}
          detail={pricing?.active_version ? `按目录 ${pricing.active_version} 计算 · ${activeKeys} 把下游密钥` : '官方价格目录尚未激活'}
          tone={pricing?.active_version ? 'info' : 'warning'}
        />
      </div>

      <div className="dashboard-runtime-grid">
        <Panel className="dashboard-runtime-panel">
          <SectionHeader
            title="模型运行状态"
            description="只展示已选择监控的模型，每一格代表一个逻辑上游站点。"
            actions={<Link className="text-link" to="/monitor">管理监控</Link>}
          />
          {!monitor ? (
            <div className="dashboard-empty"><HeartPulse size={18} /><span>监控数据暂不可用</span><Link to="/monitor">查看详情</Link></div>
          ) : orderedModels.length === 0 ? (
            <div className="dashboard-empty"><HeartPulse size={18} /><span>还没有选择需要监控的模型</span><Link to="/monitor">去选择</Link></div>
          ) : (
            <div className="dashboard-model-list">
              {orderedModels.slice(0, 6).map((model) => {
                const activeGroups = groupUpstreamTargets(model.targets.filter((target) => target.enabled));
                const routableGroups = model.publishedModelEnabled
                  ? activeGroups.filter((group) => group.targets.some((target) => targetIsRouteReady(target, routableProviderTargetIds))).length
                  : 0;
                const health = effectiveModelHealth(model, routableProviderTargetIds);
                return (
                  <Link to="/monitor" className="dashboard-model-row" key={model.publishedModelId}>
                    <span className={`dashboard-model-state ${healthClass(health)}`}><HeartPulse size={16} /></span>
                    <div className="dashboard-model-main">
                      <span><code>{model.publicModel}</code><HealthBadge state={health} /></span>
                      <small>{routableGroups} / {activeGroups.length} 个站点参与路由 · {formatRelativeTime(model.monitor.lastProbeFinishedAt)}探测</small>
                    </div>
                    <div className="dashboard-target-track" aria-label={`${model.publicModel} 上游状态`}>
                      {activeGroups.map((group) => (
                        <span
                          className={healthClass(targetGroupHealth(group, routableProviderTargetIds))}
                          title={`${group.siteName} · ${group.sourceModel}${group.targets.length > 1 ? ` · ${group.targets.length} 个内部 API 通道` : ''}`}
                          key={group.key}
                        />
                      ))}
                    </div>
                    <div className="dashboard-model-rate"><strong>{basisPoints(model.successBasisPoints)}</strong><span>近期成功率</span></div>
                    <ArrowRight size={16} />
                  </Link>
                );
              })}
            </div>
          )}
        </Panel>

        <Panel className="dashboard-runtime-panel">
          <SectionHeader
            title="最近路由事件"
            description="自动切换和未完成的请求会出现在这里。"
            actions={<Link className="text-link" to="/logs">全部日志</Link>}
          />
          {!recentLogs || routeEvents.length === 0 ? (
            <div className="dashboard-empty is-success"><RouteIcon size={18} /><span>过去 24 小时没有切换或失败</span></div>
          ) : (
            <div className="dashboard-event-list">
              {routeEvents.map((log) => {
                const failed = !logIsSuccessful(log);
                const siteName = log.finalAttempt?.siteName || '未命中上游';
                return (
                  <Link to="/logs" className="dashboard-event-row" key={log.id}>
                    <span className={`dashboard-event-icon ${failed ? 'is-danger' : 'is-warning'}`}>
                      {failed ? <Activity size={16} /> : <RouteIcon size={16} />}
                    </span>
                    <div>
                      <span><code>{log.publicModel || '未识别模型'}</code><Badge tone={failed ? 'danger' : 'warning'}>{failed ? '调用失败' : '自动切换'}</Badge></span>
                      <small>{failed ? (log.errorCode || `HTTP ${log.httpStatus || '-'}`) : `${log.switchCount} 次切换，由 ${siteName} 完成`}</small>
                    </div>
                    <time dateTime={new Date(log.startedAt).toISOString()} title={formatDateTime(log.startedAt)}>{formatRelativeTime(log.startedAt)}</time>
                  </Link>
                );
              })}
            </div>
          )}
        </Panel>
      </div>

      <Panel className="dashboard-sites-panel">
        <SectionHeader
          title="上游概况"
          description="余额按站点真实值显示；模型数为该站点全部 Key 去重后的结果。"
          actions={<Link className="text-link" to="/sites">管理站点</Link>}
        />
        <div className="dashboard-site-list">
          {sites.map((site) => {
            const account = accounts[site.id];
            const siteTargets = targets.filter((target) => target.siteId === site.id);
            const modelCount = new Set(siteTargets.map((target) => target.sourceModel)).size;
            const keyCount = new Set(siteTargets.flatMap((target) => target.credentialIds || [])).size
              || Math.max(0, ...siteTargets.map((target) => target.boundCredentialCount), 0);
            const runtimeHealth: MonitorHealth = siteTargets.some((target) => target.routable)
              ? 'healthy'
              : siteTargets.length
                ? 'unavailable'
                : 'unprobed';
            return (
              <Link className="dashboard-site-row" to={`/sites/${site.id}`} key={site.id}>
                <span className={`dashboard-site-icon ${healthClass(runtimeHealth)}`}><Server size={17} /></span>
                <div className="dashboard-site-identity">
                  <span><strong>{site.name}</strong><Badge tone={site.enabled ? 'success' : 'neutral'}>{site.enabled ? '已启用' : '已停用'}</Badge></span>
                  <span>{site.dashboardUrl}</span>
                </div>
                <div className="dashboard-site-runtime"><span>运行</span><HealthBadge state={runtimeHealth} /></div>
                <div className="dashboard-site-stat"><span>余额</span><strong>{account?.latestBalance ? `${account.latestBalance.availableValue} ${account.latestBalance.availableUnit}` : '未连接'}</strong></div>
                <div className="dashboard-site-stat"><span>API Key</span><strong>{keyCount}</strong></div>
                <div className="dashboard-site-stat"><span>模型</span><strong>{modelCount}</strong></div>
                <ArrowRight size={16} />
              </Link>
            );
          })}
        </div>
      </Panel>
    </div>
  );
}
