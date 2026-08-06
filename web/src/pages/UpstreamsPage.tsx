import {
  ArrowRight,
  CircleDollarSign,
  KeyRound,
  ListFilter,
  Package,
  Plus,
  RefreshCw,
  Route as RouteIcon,
  Server,
  Waypoints,
} from 'lucide-react';
import { useMemo, useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { Badge, Button, Dialog, EmptyState, ErrorState, Field, IconButton, LoadingState, PageHeader, Surface } from '../components/ui';
import { useToast } from '../components/Toast';
import { api } from '../lib/api';
import { formatRelativeTime } from '../lib/format';
import { inferenceProtocolAuthScheme, inferenceProtocolHint } from '../lib/inferenceProtocols';
import type { Protocol } from '../lib/types';
import { UpstreamErrorBoundary } from './upstreams/UpstreamErrorBoundary';
import { formatSourceAmount, type UpstreamSiteView } from './upstreams/siteAdapter';
import { useUpstreamSites } from './upstreams/useUpstreamSites';

interface AddSiteForm {
  name: string;
  dashboardUrl: string;
  baseUrl: string;
  apiKey: string;
  protocol: Protocol;
}

const emptyForm: AddSiteForm = { name: '', dashboardUrl: '', baseUrl: '', apiKey: '', protocol: 'compatible' };

export function UpstreamsPage() {
  return <UpstreamErrorBoundary resetKey="upstream-list"><UpstreamList /></UpstreamErrorBoundary>;
}

function UpstreamList() {
  const navigate = useNavigate();
  const toast = useToast();
  const inventory = useUpstreamSites();
  const [query, setQuery] = useState('');
  const [addOpen, setAddOpen] = useState(false);
  const [form, setForm] = useState<AddSiteForm>(emptyForm);
  const [saving, setSaving] = useState(false);

  const sites = inventory.data ?? [];
  const filtered = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    if (!normalized) return sites;
    return sites.filter((site) => `${site.name} ${site.origin} ${site.baseUrl}`.toLowerCase().includes(normalized));
  }, [query, sites]);

  const createSite = async (event: FormEvent) => {
    event.preventDefault();
    const baseUrl = form.baseUrl.trim();
    const apiKey = form.apiKey.trim();
    if (!baseUrl || !apiKey) return;
    const name = form.name.trim() || inferredName(baseUrl);
    let createdSiteId: number | null = null;
    setSaving(true);
    try {
      const created = await api.createV2Site({
        name,
        dashboardUrl: form.dashboardUrl.trim() || inferredDashboardUrl(baseUrl),
        enabled: true,
      });
      createdSiteId = created.id;
      await api.createV2Endpoint(created.id, {
        name: '主要接入地址',
        baseUrl,
        wireProtocol: form.protocol,
        compatibilityProfile: 'generic',
        authScheme: inferenceProtocolAuthScheme(form.protocol),
        enabled: true,
      });
      await api.createV2Credential(created.id, { name: '默认 Key', apiKey, enabled: true });
      setForm(emptyForm);
      setAddOpen(false);
      toast.show('站点、接入地址和 API Key 已创建', 'success');
      navigate(`/upstreams/${created.id}?tab=models&discover=1`);
    } catch (reason) {
      if (createdSiteId !== null) {
        try {
          await api.deleteV2Site(createdSiteId);
        } catch {
          toast.show('创建未完成，且自动清理失败，请刷新后删除残留站点', 'error');
          return;
        }
      }
      toast.show(reason instanceof Error ? `添加站点失败：${reason.message}` : '添加站点失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  if (inventory.loading && !inventory.data) return <div className="page"><LoadingState label="正在读取上游站点" /></div>;
  if (inventory.error && !inventory.data) return <div className="page"><ErrorState message={inventory.error} onRetry={() => void inventory.refresh()} /></div>;

  return (
    <div className="page upstream-sites-page">
      <PageHeader
        title="上游站点"
        description="一个网站只显示一次，API Key、模型、账户数据和路由状态在站点内统一管理。"
        actions={<Button variant="primary" icon={Plus} onClick={() => setAddOpen(true)}>添加站点</Button>}
      />

      <Surface className="upstream-sites-surface">
        <div className="toolbar upstream-sites-toolbar">
          <div className="search-box">
            <ListFilter size={15} />
            <input className="input" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索站点或域名" />
          </div>
          <span className="toolbar-note">{sites.length} 个站点 · {sites.reduce((sum, site) => sum + site.credentials.filter((item) => item.enabled).length, 0)} 枚启用 Key</span>
          <span className="toolbar-spacer" />
          <Button size="sm" variant="ghost" icon={RefreshCw} busy={inventory.loading} onClick={() => void inventory.refresh()}>刷新</Button>
        </div>

        {filtered.length === 0 ? (
          <EmptyState
            title={sites.length ? '没有匹配的站点' : '还没有上游站点'}
            description={sites.length ? '换一个关键词试试。' : '添加站点和 API Key 后即可获取模型并加入路由。'}
            action={!sites.length ? <Button variant="primary" icon={Plus} onClick={() => setAddOpen(true)}>添加站点</Button> : undefined}
          />
        ) : (
          <>
            <div className="data-scroller upstream-sites-desktop">
              <table className="data-table upstream-site-table">
                <thead><tr><th>站点</th><th>API Key</th><th>模型</th><th>余额</th><th>套餐</th><th>路由可用性</th><th>最近同步</th><th aria-label="操作" /></tr></thead>
                <tbody>{filtered.map((site) => (
                  <tr key={site.id} onDoubleClick={() => navigate(`/upstreams/${site.id}`)}>
                    <td><SiteIdentity site={site} /></td>
                    <td><CountCell icon={KeyRound} value={site.credentials.length} hint={`${site.credentials.filter((item) => item.enabled).length} 枚启用`} /></td>
                    <td><CountCell icon={Waypoints} value={site.modelCount} hint={site.lastModelSyncAt ? `${formatRelativeTime(site.lastModelSyncAt)}更新` : '尚未获取'} /></td>
                    <td><BalanceCell site={site} /></td>
                    <td><PlanCell site={site} /></td>
                    <td><AvailabilityCell site={site} /></td>
                    <td><SyncCell site={site} /></td>
                    <td><div className="row-actions"><IconButton label={`打开 ${site.name}`} onClick={() => navigate(`/upstreams/${site.id}`)}><ArrowRight size={17} /></IconButton></div></td>
                  </tr>
                ))}</tbody>
              </table>
            </div>
            <div className="upstream-mobile-list">
              {filtered.map((site) => <MobileSiteCard key={site.id} site={site} onOpen={() => navigate(`/upstreams/${site.id}`)} />)}
            </div>
          </>
        )}
      </Surface>

      <Dialog
        open={addOpen}
        title="添加上游站点"
        description="一次创建网站、主要接入地址和首枚 API Key。"
        onClose={() => setAddOpen(false)}
        footer={<><Button onClick={() => setAddOpen(false)}>取消</Button><Button type="submit" form="add-site-form" variant="primary" busy={saving}>添加并检测</Button></>}
      >
        <form id="add-site-form" className="site-create-form" onSubmit={(event) => void createSite(event)}>
          <Field label="站点名称" hint="留空时使用站点域名。">
            <input className="input" value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} placeholder="例如：Ciii" />
          </Field>
          <Field label="站点网址" hint="用于打开控制台和后续连接余额；留空时取 API 域名。">
            <input className="input" type="url" value={form.dashboardUrl} onChange={(event) => setForm({ ...form, dashboardUrl: event.target.value })} placeholder="https://example.com" />
          </Field>
          <Field label="API 地址">
            <input className="input" type="url" value={form.baseUrl} onChange={(event) => setForm({ ...form, baseUrl: event.target.value })} placeholder="https://example.com/v1" required />
          </Field>
          <Field label="API 格式" hint={inferenceProtocolHint(form.protocol)}>
            <select className="select" value={form.protocol} onChange={(event) => setForm({ ...form, protocol: event.target.value as Protocol })}>
              <option value="compatible">OpenAI 兼容 · 可路由</option>
              <option value="openai">OpenAI 官方 · 可路由</option>
              <option value="anthropic">Anthropic 原生 · 仅获取模型</option>
              <option value="gemini">Gemini 原生 · 仅获取模型</option>
            </select>
          </Field>
          <Field label="API Key">
            <input className="input" type="password" value={form.apiKey} onChange={(event) => setForm({ ...form, apiKey: event.target.value })} autoComplete="new-password" placeholder="sk-..." required />
          </Field>
        </form>
      </Dialog>
    </div>
  );
}

function SiteIdentity({ site }: { site: UpstreamSiteView }) {
  return (
    <div className="site-identity">
      <span className="site-icon"><Server size={16} aria-hidden="true" /></span>
      <div><strong>{site.name}</strong><code>{site.origin || site.baseUrl}</code></div>
    </div>
  );
}

function CountCell({ icon: Icon, value, hint }: { icon: typeof KeyRound; value: number; hint: string }) {
  return <div className="site-count-cell"><span><Icon size={14} aria-hidden="true" />{value}</span><small>{hint}</small></div>;
}

function BalanceCell({ site }: { site: UpstreamSiteView }) {
  const account = site.account;
  if (!account?.configured) return <div className="site-value-cell is-muted"><strong>未连接账户</strong><small>不影响 API 路由</small></div>;
  const stale = account.sync.stale || account.sync.state === 'stale';
  return (
    <div className="site-value-cell">
      <strong>{formatSourceAmount(account.snapshot?.balance)}</strong>
      <small className={account.sync.state === 'error' ? 'is-danger' : stale ? 'is-warning' : ''}>
        {account.sync.state === 'error' ? '同步失败' : stale ? '数据陈旧' : account.snapshot ? '站点原始余额' : '等待首次同步'}
      </small>
    </div>
  );
}

function PlanCell({ site }: { site: UpstreamSiteView }) {
  const subscription = site.account?.snapshot?.subscription;
  if (!subscription) return <div className="site-value-cell is-muted"><strong>未返回套餐</strong><small>{site.account?.configured ? '站点未提供' : '账户未连接'}</small></div>;
  return (
    <div className="site-value-cell">
      <strong>{subscription.planName || '未命名套餐'}</strong>
      <small>{subscription.remaining ? formatSourceAmount(subscription.remaining) : subscription.expiresAt ? `${formatRelativeTime(subscription.expiresAt)}到期` : subscription.status || '有效期未提供'}</small>
    </div>
  );
}

function AvailabilityCell({ site }: { site: UpstreamSiteView }) {
  const availability = site.routeAvailability;
  if (!availability.total) return <div className="site-value-cell is-muted"><strong>未加入监控</strong><small>按所选模型统计</small></div>;
  if (availability.unknown === availability.total) return <div className="site-availability-cell"><Badge tone="warning">等待探针</Badge><small>{availability.total} 个监控目标</small></div>;
  const tone = availability.attention ? 'danger' : availability.unknown ? 'warning' : 'success';
  return (
    <div className="site-availability-cell">
      <Badge tone={tone}>{availability.healthy}/{availability.total} 可用</Badge>
      <small>{availability.attention ? `${availability.attention} 个需处理` : availability.unknown ? `${availability.unknown} 个待探测` : '所选模型正常'}</small>
    </div>
  );
}

function SyncCell({ site }: { site: UpstreamSiteView }) {
  return (
    <div className="site-sync-cell">
      <span><CircleDollarSign size={13} />账户 <strong>{site.lastAccountSyncAt ? formatRelativeTime(site.lastAccountSyncAt) : site.sourceVersion === 'v2' ? '未关联' : '从未'}</strong></span>
      <span><Waypoints size={13} />模型 <strong>{site.lastModelSyncAt ? formatRelativeTime(site.lastModelSyncAt) : '从未'}</strong></span>
    </div>
  );
}

function MobileSiteCard({ site, onOpen }: { site: UpstreamSiteView; onOpen: () => void }) {
  return (
    <button type="button" className="upstream-mobile-card" onClick={onOpen}>
      <div className="upstream-mobile-heading"><SiteIdentity site={site} /><ArrowRight size={17} /></div>
      <div className="upstream-mobile-balance"><span>余额</span><strong>{formatSourceAmount(site.account?.snapshot?.balance)}</strong></div>
      <div className="upstream-mobile-plan"><Package size={14} /><span>{site.account?.snapshot?.subscription?.planName || '未返回套餐'}</span></div>
      <div className="upstream-mobile-meta">
        <span><KeyRound size={13} />{site.credentials.length} Keys</span>
        <span><Waypoints size={13} />{site.modelCount} 模型</span>
        <span><RouteIcon size={13} />{site.routeAvailability.unknown === site.routeAvailability.total && site.routeAvailability.total ? '待探针' : site.routeAvailability.total ? `${site.routeAvailability.healthy}/${site.routeAvailability.total}` : '未监控'}</span>
      </div>
    </button>
  );
}

function inferredName(baseUrl: string): string {
  try {
    return new URL(baseUrl).hostname;
  } catch {
    return '新站点';
  }
}

function inferredDashboardUrl(baseUrl: string): string {
  try {
    return new URL(baseUrl).origin;
  } catch {
    return '';
  }
}
