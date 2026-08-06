import {
  Activity,
  ArrowLeft,
  CircleDollarSign,
  KeyRound,
  Package,
  RefreshCw,
  Route as RouteIcon,
  Server,
  Settings2,
  Waypoints,
} from 'lucide-react';
import { useMemo, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { Badge, Button, EmptyState, ErrorState, LoadingState } from '../../components/ui';
import { formatDateTime, formatRelativeTime } from '../../lib/format';
import { inferenceProtocolLabel } from '../../lib/inferenceProtocols';
import type { UpstreamModel } from '../../lib/types';
import { AccountTab } from './AccountTab';
import { ApiKeysTab } from './ApiKeysTab';
import { ModelDiscoveryPanel } from './ModelDiscoveryPanel';
import { SiteSettingsTab } from './SiteSettingsTab';
import { UpstreamErrorBoundary } from './UpstreamErrorBoundary';
import { findSiteByMemberId, formatSourceAmount, type UpstreamSiteView } from './siteAdapter';
import { useUpstreamSites } from './useUpstreamSites';

type DetailTab = 'overview' | 'keys' | 'models' | 'account' | 'settings';

const detailTabs: Array<{ value: DetailTab; label: string; icon: typeof Server }> = [
  { value: 'overview', label: '总览', icon: Activity },
  { value: 'keys', label: 'API Keys', icon: KeyRound },
  { value: 'models', label: '模型', icon: Waypoints },
  { value: 'account', label: '账户与账单', icon: CircleDollarSign },
  { value: 'settings', label: '设置', icon: Settings2 },
];

export function UpstreamDetailPage() {
  const { upstreamId = '' } = useParams();
  return <UpstreamErrorBoundary resetKey={upstreamId}><UpstreamDetailContent upstreamId={upstreamId} /></UpstreamErrorBoundary>;
}

function UpstreamDetailContent({ upstreamId }: { upstreamId: string }) {
  const navigate = useNavigate();
  const inventory = useUpstreamSites();
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = parseTab(searchParams.get('tab'));
  const autoDiscover = searchParams.get('discover') === '1';
  const site = findSiteByMemberId(inventory.data ?? [], upstreamId);

  const setTab = (value: DetailTab) => {
    const next = new URLSearchParams(searchParams);
    next.set('tab', value);
    next.delete('discover');
    setSearchParams(next, { replace: true });
  };

  const clearAutoDiscover = () => {
    const next = new URLSearchParams(searchParams);
    next.delete('discover');
    setSearchParams(next, { replace: true });
  };

  if (inventory.loading && !inventory.data) return <div className="page"><LoadingState label="正在读取站点详情" /></div>;
  if (inventory.error && !inventory.data) return <div className="page"><ErrorState message={inventory.error} onRetry={() => void inventory.refresh()} /></div>;
  if (!site) return <div className="page"><EmptyState title="站点不存在" description="它可能已被删除或已合并到其他站点。" action={<Button onClick={() => navigate('/upstreams')}>返回上游站点</Button>} /></div>;

  return (
    <div className="page upstream-detail-page">
      <header className="upstream-detail-header">
        <div className="upstream-detail-leading">
          <Link className="upstream-back-link" to="/upstreams" aria-label="返回上游站点"><ArrowLeft size={18} /></Link>
          <span className="upstream-detail-icon"><Server size={20} /></span>
          <div><div className="upstream-detail-title"><h1>{site.name}</h1><Badge tone={site.enabled ? 'success' : 'neutral'}>{site.enabled ? '已启用' : '已停用'}</Badge></div><code>{site.baseUrl || site.dashboardUrl || '尚未配置接入地址'}</code></div>
        </div>
        <div className="upstream-detail-actions"><Button icon={RefreshCw} busy={inventory.loading} onClick={() => void inventory.refresh()}>刷新数据</Button></div>
      </header>

      <SiteSummary site={site} />

      <nav className="upstream-page-tabs" aria-label="站点详情">
        {detailTabs.map(({ value, label, icon: Icon }) => <button type="button" className={tab === value ? 'is-active' : ''} aria-current={tab === value ? 'page' : undefined} onClick={() => setTab(value)} key={value}><Icon size={15} /><span>{label}</span></button>)}
      </nav>

      <main className="upstream-detail-content">
        {site.loadError && <div className="upstream-alert-band"><strong>部分数据读取失败</strong><span>{site.loadError}</span></div>}
        {tab === 'overview' && <OverviewTab site={site} />}
        {tab === 'keys' && <ApiKeysTab site={site} onChanged={inventory.refresh} />}
        {tab === 'models' && <ModelsTab site={site} autoDiscover={autoDiscover} onAutoDiscoverHandled={clearAutoDiscover} onChanged={inventory.refresh} />}
        {tab === 'account' && <AccountTab key={`${site.accountTarget.kind}-${site.accountTarget.id}`} target={site.accountTarget} onChanged={() => void inventory.refresh()} />}
        {tab === 'settings' && <SiteSettingsTab site={site} onChanged={inventory.refresh} onDeleted={() => navigate('/upstreams')} />}
      </main>
    </div>
  );
}

function SiteSummary({ site }: { site: UpstreamSiteView }) {
  const subscription = site.account?.snapshot?.subscription;
  const route = site.routeAvailability;
  const routeText = routeSummary(route);
  return (
    <section className="upstream-summary-strip" aria-label="站点摘要">
      <SummaryItem icon={CircleDollarSign} label="站点余额" value={formatSourceAmount(site.account?.snapshot?.balance)} hint={site.account?.configured ? syncHint(site) : '账户未连接'} />
      <SummaryItem icon={Package} label="当前套餐" value={subscription?.planName || '未返回套餐'} hint={subscription?.remaining ? formatSourceAmount(subscription.remaining) : subscription?.expiresAt ? `${formatRelativeTime(subscription.expiresAt)}到期` : '按站点原始数据'} />
      <SummaryItem icon={KeyRound} label="API Keys" value={String(site.credentials.length)} hint={`${site.credentials.filter((item) => item.enabled).length} 枚启用`} />
      <SummaryItem icon={Waypoints} label="上游模型" value={String(site.modelCount)} hint={site.lastModelSyncAt ? `${formatRelativeTime(site.lastModelSyncAt)}更新` : '尚未获取'} />
      <SummaryItem icon={RouteIcon} label="路由可用性" value={routeText.value} hint={routeText.hint} />
    </section>
  );
}

function SummaryItem({ icon: Icon, label, value, hint }: { icon: typeof Server; label: string; value: string; hint: string }) {
  return <div className="upstream-summary-item"><span><Icon size={15} />{label}</span><strong>{value}</strong><small>{hint}</small></div>;
}

function OverviewTab({ site }: { site: UpstreamSiteView }) {
  const account = site.account;
  const subscription = account?.snapshot?.subscription;
  const route = site.routeAvailability;
  const credentialErrors = site.credentials.filter((item) => item.state === 'credential_error' || item.state === 'cooldown').length;
  const routeText = routeSummary(route);
  const routableEndpoints = site.endpoints.filter((endpoint) => endpoint.capabilities.routeEligible);
  const discoveryOnlyEndpoints = site.endpoints.length - routableEndpoints.length;
  return (
    <div className="upstream-overview-grid">
      {(credentialErrors > 0 || account?.sync.state === 'error') && <div className="upstream-alert-band"><strong>需要处理</strong><span>{credentialErrors ? `${credentialErrors} 枚 API Key 异常。` : ''}{account?.sync.state === 'error' ? `账户同步失败：${account.sync.error?.message || '请重新连接账户'}` : ''}</span></div>}

      <section className="upstream-section">
        <div className="upstream-section-heading"><div><CircleDollarSign size={17} /><div><h2>账户概况</h2><p>余额和套餐只按站点原始数据展示。</p></div></div>{account?.configured && <Badge tone={account.sync.state === 'error' ? 'danger' : account.sync.stale ? 'warning' : 'success'}>{account.sync.state === 'error' ? '同步失败' : account.sync.stale ? '数据陈旧' : '同步正常'}</Badge>}</div>
        <dl className="upstream-definition-grid">
          <div><dt>余额</dt><dd>{formatSourceAmount(account?.snapshot?.balance)}</dd></div>
          <div><dt>套餐</dt><dd>{subscription?.planName || '未返回套餐'}</dd></div>
          <div><dt>剩余额度</dt><dd>{subscription?.remaining ? formatSourceAmount(subscription.remaining) : '-'}</dd></div>
          <div><dt>到期 / 重置</dt><dd>{formatDateTime(subscription?.expiresAt || subscription?.renewsAt || null)}</dd></div>
          <div><dt>最近成功同步</dt><dd>{formatDateTime(account?.sync.lastSuccessAt || null)}</dd></div>
          <div><dt>下次同步</dt><dd>{formatDateTime(account?.sync.nextAt || null)}</dd></div>
        </dl>
      </section>

      <section className="upstream-section">
        <div className="upstream-section-heading"><div><RouteIcon size={17} /><div><h2>路由覆盖</h2><p>只汇总已开启监控的模型目标，未探测不会被算成故障。</p></div></div>{route.total ? <Badge tone={route.attention ? 'danger' : route.unknown ? 'warning' : 'success'}>{routeText.value}</Badge> : <Badge>未加入监控</Badge>}</div>
        <div className="route-coverage-bar" aria-label={`${route.healthy}/${route.total} 可用`}><span style={{ width: route.total ? `${Math.max(4, (route.healthy / route.total) * 100)}%` : '0%' }} /></div>
        <div className="route-coverage-legend"><span><i className="is-healthy" />{route.healthy} 可用</span><span><i className="is-unknown" />{route.unknown} 待探测</span><span><i className="is-attention" />{route.attention} 需处理</span></div>
      </section>

      <section className="upstream-section upstream-overview-wide">
        <div className="upstream-section-heading"><div><Server size={17} /><div><h2>接入信息</h2><p>请求协议与站点账户类型彼此独立；原生协议会明确标记为仅模型获取。</p></div></div></div>
        <dl className="upstream-definition-grid upstream-endpoint-definition">
          <div><dt>站点域名</dt><dd><code>{site.origin}</code></dd></div>
          <div><dt>主要 API 地址</dt><dd><code>{site.baseUrl || '未配置'}</code></dd></div>
          <div><dt>API 格式</dt><dd>{inferenceProtocolLabel(site.protocol)}</dd></div>
          <div><dt>接入地址</dt><dd>{site.endpoints.length} 个 · {routableEndpoints.length} 个具备路由能力</dd></div>
          <div><dt>协议能力</dt><dd>{routableEndpoints.length ? 'OpenAI 下游路由可用' : '仅支持模型获取'}{discoveryOnlyEndpoints ? ` · ${discoveryOnlyEndpoints} 个仅获取模型` : ''}</dd></div>
          <div><dt>网站控制台</dt><dd><code>{site.dashboardUrl || '未配置'}</code></dd></div>
          <div><dt>账户适配</dt><dd>{account?.adapter?.label || '未连接'}</dd></div>
          <div><dt>模型同步</dt><dd>{formatDateTime(site.lastModelSyncAt)}</dd></div>
          <div><dt>数据模式</dt><dd>{site.sourceVersion === 'legacy' ? '历史兼容数据' : '站点聚合'}</dd></div>
        </dl>
      </section>
    </div>
  );
}

function ModelsTab({ site, autoDiscover, onAutoDiscoverHandled, onChanged }: { site: UpstreamSiteView; autoDiscover: boolean; onAutoDiscoverHandled: () => void; onChanged: () => Promise<void> }) {
  const [query, setQuery] = useState('');
  const models = useMemo(() => {
    const value = query.trim().toLowerCase();
    return site.models.filter((model) => !value || model.name.toLowerCase().includes(value));
  }, [query, site.models]);
  return (
    <div className="detail-tab-stack">
      <ModelDiscoveryPanel site={site} autoDiscover={autoDiscover} onAutoDiscoverHandled={onAutoDiscoverHandled} onChanged={onChanged} />
      <section className="upstream-section">
        <div className="upstream-section-heading"><div><Waypoints size={17} /><div><h2>模型清单</h2><p>每个模型显示当前网站下可提供它的 Key 数量。</p></div></div><div className="model-inventory-search"><input className="input" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索模型" /></div></div>
        {site.models.length === 0 ? <EmptyState title="还没有模型" description="先从任意启用的 API Key 获取模型列表。" /> : models.length === 0 ? <EmptyState title="没有匹配模型" description="换一个模型名称试试。" /> : <div className="site-model-list">{models.map((model) => <ModelRow site={site} model={model} key={model.name} />)}</div>}
      </section>
    </div>
  );
}

function ModelRow({ site, model }: { site: UpstreamSiteView; model: UpstreamModel }) {
  const supporting = site.credentials.filter((credential) => credential.legacy?.models?.some((item) => item.name === model.name && item.enabled)).length;
  const supportText = site.sourceVersion === 'legacy'
    ? `${supporting || 1}/${site.credentials.length} Keys`
    : model.credentialCount
      ? `${model.supportedCredentialCount ?? 0}/${model.credentialCount} Keys 可用`
      : '尚未检测 Key 覆盖';
  const label = !model.enabled ? '已停用' : model.stale ? '待复核' : '可路由';
  const tone = !model.enabled ? 'neutral' : model.stale ? 'warning' : 'success';
  return <div className="site-model-row"><code>{model.name}</code><span>{supportText}</span><Badge tone={tone}>{label}</Badge></div>;
}

function parseTab(value: string | null): DetailTab {
  return value === 'keys' || value === 'models' || value === 'account' || value === 'settings' ? value : 'overview';
}

function syncHint(site: UpstreamSiteView): string {
  const account = site.account;
  if (!account) return '账户未连接';
  if (account.sync.state === 'error') return '同步失败';
  if (account.sync.stale) return '数据陈旧';
  return site.lastAccountSyncAt ? `${formatRelativeTime(site.lastAccountSyncAt)}同步` : '等待首次同步';
}

function routeSummary(route: UpstreamSiteView['routeAvailability']): { value: string; hint: string } {
  if (!route.total) return { value: '未监控', hint: '只统计所选模型' };
  if (route.unknown === route.total) return { value: `${route.total} 个目标`, hint: '等待首次探针' };
  if (route.attention) return { value: `${route.healthy}/${route.total} 可用`, hint: `${route.attention} 个需处理` };
  if (route.unknown) return { value: `${route.healthy}/${route.total} 可用`, hint: `${route.unknown} 个待探测` };
  return { value: `${route.healthy}/${route.total} 可用`, hint: '所选模型正常' };
}
