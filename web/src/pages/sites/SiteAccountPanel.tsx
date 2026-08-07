import {
  ChevronDown,
  CircleDollarSign,
  RefreshCw,
  Search,
  ShieldCheck,
  TriangleAlert,
} from 'lucide-react';
import { Fragment, useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { useToast } from '../../components/Toast';
import {
  Badge,
  Button,
  EmptyState,
  Field,
  InlineNotice,
  LoadingState,
  Panel,
  SearchField,
  SectionHeader,
  Switch,
} from '../../components/ui';
import { api, type AccountSecretInput } from '../../lib/api';
import { formatDateTime, formatTokens } from '../../lib/format';
import type { Site, SiteAccountConnection, SiteBalance, SiteUsageRecord } from '../../lib/types';

const prototypeMode = typeof window !== 'undefined' && new URLSearchParams(window.location.search).get('prototype') === '1';

function usageStatusLabel(value: string): string {
  if (value === 'success' || value === 'succeeded') return '成功';
  if (value === 'failed' || value === 'error') return '失败';
  if (value === 'cancelled' || value === 'canceled') return '已取消';
  return value || '未知';
}

function seconds(value: number | null): string {
  if (value === null || !Number.isFinite(value)) return '—';
  return `${(value / 1000).toFixed(2)} 秒`;
}

function identifyPanel(origin: string): { label: string; adapterKind: string; capabilities: string[] } {
  const normalized = origin.toLowerCase();
  if (normalized.includes('ciii') || normalized.includes('sub2api')) {
    return { label: 'Sub2API 兼容', adapterKind: 'ciii', capabilities: ['余额读取', '使用日志', '令牌刷新'] };
  }
  if (normalized.includes('new-api') || normalized.includes('newapi')) {
    return { label: 'New API 兼容', adapterKind: 'new_api', capabilities: ['余额读取', '使用日志'] };
  }
  return { label: '通用中转面板', adapterKind: 'one_api', capabilities: ['余额读取'] };
}

function usageExtras(site: Site, item: SiteUsageRecord) {
  const model = (item.upstreamModel || item.model).toLowerCase();
  const requestPath = model.includes('claude') ? '/v1/messages'
    : model.includes('gemini') ? '/v1beta/models/:model:generateContent'
      : '/v1/chat/completions';
  return {
    reasoningEffort: item.reasoningEffort || (prototypeMode ? (item.reasoningTokens ? 'high' : 'default') : '—'),
    requestPath: item.requestPath || requestPath,
    requestType: item.requestType || (prototypeMode ? '流式' : '—'),
    billingMode: item.billingMode || (prototypeMode ? '按量' : '—'),
    requestIp: item.requestIp || (prototypeMode ? `210.16.177.${200 + (item.id % 20)}` : '—'),
    region: item.region || (prototypeMode ? '待获取地区' : '—'),
    group: item.group || (prototypeMode ? `${site.name} 默认分组` : '—'),
    firstOutputMs: item.firstOutputMs ?? (!prototypeMode || item.status === 'failed' || item.status === 'error' || item.durationMs === null
      ? null
      : Math.max(90, Math.round(item.durationMs * 0.32))),
  };
}

async function persistAccountConnection(
  site: Site,
  account: SiteAccountConnection | null,
  origin: string,
  adapterKind: string,
  enabled: boolean,
  secrets: AccountSecretInput,
) {
  if (!account) {
    await api.configureSiteAccount(site.id, { adapterKind, origin, enabled, secrets });
    return;
  }
  let revision = account.revision;
  if (secrets.accessToken || secrets.refreshToken || secrets.authorization || secrets.cookie) {
    const updated = await api.replaceSiteAccountSecret(site.id, revision, secrets);
    revision = updated.revision;
  }
  if (adapterKind !== account.adapterKind || origin !== account.origin || enabled !== account.enabled) {
    await api.updateSiteAccount(site.id, revision, { adapterKind, origin, enabled });
  }
}

export function SiteBalanceSummary({
  site,
  account,
  loading,
  onRefreshAccount,
  onOpenSettings,
}: {
  site: Site;
  account: SiteAccountConnection | null;
  loading: boolean;
  onRefreshAccount: () => Promise<void>;
  onOpenSettings: () => void;
}) {
  const toast = useToast();
  const [balance, setBalance] = useState<SiteBalance | null>(account?.latestBalance || null);
  const [refreshing, setRefreshing] = useState(false);
  const accountName = balance?.accountName.trim();

  useEffect(() => setBalance(account?.latestBalance || null), [account]);

  const refresh = async () => {
    if (!account) {
      onOpenSettings();
      return;
    }
    setRefreshing(true);
    try {
      const next = await api.refreshSiteBalance(site.id);
      setBalance(next);
      await onRefreshAccount();
      toast.show('余额已刷新', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '余额刷新失败', 'error');
    } finally {
      setRefreshing(false);
    }
  };

  return (
    <div className="site-balance-summary">
      <span className="site-balance-icon"><CircleDollarSign size={20} /></span>
      <div className="site-balance-value">
        <span>真实余额</span>
        <strong>{loading ? '读取中…' : balance ? `${balance.availableValue} ${balance.availableUnit}` : '未连接余额'}</strong>
      </div>
      <div className="site-balance-meta">
        {accountName && <span>站点账户：{accountName}</span>}
        <small>{balance ? `更新于 ${formatDateTime(balance.capturedAt, true)}` : account ? '点击刷新获取最新余额' : '不影响 API Key 模型调用'}</small>
      </div>
      <Button size="sm" icon={RefreshCw} busy={refreshing} onClick={() => void refresh()}>{account ? '刷新余额' : '连接站点账户'}</Button>
    </div>
  );
}

export function SiteAccountSettings({
  site,
  account,
  onSaved,
}: {
  site: Site;
  account: SiteAccountConnection | null;
  onSaved: () => Promise<void>;
}) {
  const toast = useToast();
  const [origin, setOrigin] = useState(account?.origin || site.dashboardUrl || '');
  const detected = useMemo(() => identifyPanel(origin), [origin]);
  const [accessToken, setAccessToken] = useState('');
  const [refreshToken, setRefreshToken] = useState('');
  const [expiresAt, setExpiresAt] = useState('');
  const [enabled, setEnabled] = useState(account?.enabled ?? true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    setOrigin(account?.origin || site.dashboardUrl || '');
    setEnabled(account?.enabled ?? true);
  }, [account, site.dashboardUrl]);

  const saveConnection = async (secrets: AccountSecretInput) => {
    if (!origin.trim()) {
      setError('请填写站点地址');
      return;
    }
    setSaving(true);
    setError('');
    try {
      await persistAccountConnection(site, account, origin.trim(), detected.adapterKind, enabled, secrets);
      await onSaved();
      setAccessToken('');
      setRefreshToken('');
      toast.show('站点账户已连接', 'success');
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '站点账户连接失败');
    } finally {
      setSaving(false);
    }
  };

  const submitTokens = async (event: FormEvent) => {
    event.preventDefault();
    if (!account && !accessToken.trim()) {
      setError('请填写 Access Token');
      return;
    }
    await saveConnection({
      accessToken: accessToken.trim() || undefined,
      refreshToken: refreshToken.trim() || undefined,
      expiresAt: expiresAt ? new Date(expiresAt).getTime() : undefined,
    });
  };

  return (
    <div className="site-account-settings">
      <InlineNotice>站点账户只负责读取真实余额、上游使用日志以及刷新会话或令牌；模型推理始终使用“API Key”中的上游密钥。</InlineNotice>
      <div className="account-detection-card">
        <span className="account-detection-icon"><ShieldCheck size={19} /></span>
        <div>
          <span>地址兼容性推断</span>
          <strong>{detected.label}</strong>
          <small>{detected.capabilities.join(' · ')}</small>
        </div>
      </div>

      <Field label="站点地址" required hint="系统根据地址选择兼容适配器，保存时由后端验证真实凭据。">
        <input className="input code-input" type="url" value={origin} onChange={(event) => setOrigin(event.target.value)} />
      </Field>

      <form className="form-stack" onSubmit={submitTokens}>
        <InlineNotice>保存真实站点令牌；已有连接时留空表示不更换对应令牌。</InlineNotice>
        <Field label="Access Token" required={!account}><textarea className="textarea code-input" rows={3} value={accessToken} onChange={(event) => setAccessToken(event.target.value)} autoComplete="off" /></Field>
        <Field label="Refresh Token" hint="Sub2API 兼容面板填写后可自动续期。"><textarea className="textarea code-input" rows={2} value={refreshToken} onChange={(event) => setRefreshToken(event.target.value)} autoComplete="off" /></Field>
        <Field label="Token 到期时间"><input className="input" type="datetime-local" value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} /></Field>
        <Switch checked={enabled} label="自动刷新站点账户的余额、上游日志和会话令牌" onChange={setEnabled} />
        <Button variant="primary" busy={saving} type="submit">保存站点账户</Button>
      </form>

      {account?.lastErrorCode && <InlineNotice tone="warning"><TriangleAlert size={14} />最近连接异常：{account.lastErrorCode} · {formatDateTime(account.lastErrorAt)}</InlineNotice>}
      {error && <p className="form-error">{error}</p>}
    </div>
  );
}

function MobileUsageRecord({
  site,
  item,
  expanded,
  onToggle,
}: {
  site: Site;
  item: SiteUsageRecord;
  expanded: boolean;
  onToggle: () => void;
}) {
  const success = item.status === 'success' || item.status === 'succeeded';
  const failed = item.status === 'failed' || item.status === 'error';
  const extra = usageExtras(site, item);

  return (
    <article className="mobile-upstream-usage-card">
      <button
        type="button"
        className="mobile-upstream-usage-summary"
        aria-expanded={expanded}
        onClick={onToggle}
      >
        <span className="mobile-upstream-usage-heading">
          <span className="cell-stack">
            <strong>{formatDateTime(item.occurredAt)}</strong>
            <code>{item.remoteId || item.requestId}</code>
          </span>
          <Badge tone={success ? 'success' : failed ? 'danger' : 'neutral'}>{usageStatusLabel(item.status)}</Badge>
        </span>
        <span className="mobile-upstream-usage-model">
          <code>{item.model || item.upstreamModel || '—'}</code>
          <small>{extra.reasoningEffort}</small>
        </span>
        <span className="mobile-upstream-usage-meta">
          <span><small>API Key</small><strong>{item.apiKeyName || '—'}</strong></span>
          <span><small>类型</small><strong>{extra.requestType}</strong></span>
        </span>
        <span className="mobile-upstream-usage-metrics">
          <span><small>Token</small><strong>{formatTokens(item.totalTokens ?? ((item.inputTokens || 0) + (item.outputTokens || 0)))}</strong><small>入 {formatTokens(item.inputTokens || 0)} · 出 {formatTokens(item.outputTokens || 0)}</small></span>
          <span><small>首字 / 总耗时</small><strong>{seconds(extra.firstOutputMs)}</strong><small>{seconds(item.durationMs)}</small></span>
          <span><small>真实扣费</small><strong>{item.chargeValue ? `${item.chargeValue} ${item.chargeUnit || ''}` : '—'}</strong></span>
        </span>
        <ChevronDown className={expanded ? 'is-open' : ''} size={16} aria-hidden="true" />
      </button>
      {expanded && <div className="mobile-upstream-usage-detail">
        <div className="usage-detail-grid">
          <div><span>JieShan 请求 ID</span><code>{item.requestId || '—'}</code></div>
          <div><span>上游请求 ID</span><code>{item.upstreamRequestId || '—'}</code></div>
          <div><span>请求路径</span><code>{extra.requestPath}</code></div>
          <div><span>请求 IP / 地区</span><strong>{extra.requestIp} · {extra.region}</strong></div>
          <div><span>上游分组</span><strong>{extra.group}</strong></div>
          <div><span>计费模式</span><strong>{extra.billingMode}</strong></div>
          <div><span>HTTP 状态</span><strong>{item.httpStatus ? `HTTP ${item.httpStatus}` : '—'}</strong></div>
          <div><span>上游实际模型</span><code>{item.upstreamModel || item.model || '—'}</code></div>
          <div><span>总 Token</span><strong>{formatTokens(item.totalTokens ?? ((item.inputTokens || 0) + (item.outputTokens || 0)))}</strong></div>
          <div><span>记录同步时间</span><strong>{formatDateTime(item.sourceFetchedAt)}</strong></div>
        </div>
      </div>}
    </article>
  );
}

export function SiteUsagePanel({
  site,
  account,
  accountLoading,
  onRefreshAccount,
  onOpenSettings,
}: {
  site: Site;
  account: SiteAccountConnection | null;
  accountLoading: boolean;
  onRefreshAccount: () => Promise<void>;
  onOpenSettings: () => void;
}) {
  const toast = useToast();
  const [usage, setUsage] = useState<SiteUsageRecord[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [hasMore, setHasMore] = useState(false);
  const [cursor, setCursor] = useState('');
  const [error, setError] = useState('');
  const [syncing, setSyncing] = useState(false);
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState('');
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const loadVersion = useRef(0);

  const loadUsage = async (nextCursor = '') => {
    if (!account?.capabilities.usage) return;
    const append = Boolean(nextCursor);
    const requestVersion = append ? loadVersion.current : ++loadVersion.current;
    if (append) setLoadingMore(true);
    else setLoading(true);
    setError('');
    try {
      const page = await api.listSiteUsage(site.id, {
        limit: 100,
        cursor: nextCursor || undefined,
        search: query.trim() || undefined,
        status: status || undefined,
      });
      if (requestVersion !== loadVersion.current) return;
      setUsage((current) => append ? [...current, ...page.items] : page.items);
      setHasMore(page.hasMore);
      setCursor(page.nextCursor);
    } catch (reason) {
      if (requestVersion !== loadVersion.current) return;
      setError(reason instanceof Error ? reason.message : '上游日志加载失败');
    } finally {
      if (requestVersion === loadVersion.current) {
        setLoading(false);
        setLoadingMore(false);
      }
    }
  };

  useEffect(() => {
    if (!account?.capabilities.usage) {
      loadVersion.current += 1;
      setUsage([]);
      return;
    }
    const timer = window.setTimeout(() => { void loadUsage(); }, 200);
    return () => window.clearTimeout(timer);
  }, [account?.id, account?.lastUsageRefreshAt, query, status]);

  const syncUsage = async () => {
    if (!account) return;
    setSyncing(true);
    try {
      await api.syncSiteUsage(site.id, { limit: 100 });
      await loadUsage();
      await onRefreshAccount();
      toast.show('上游使用记录已同步', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '同步失败', 'error');
    } finally {
      setSyncing(false);
    }
  };

  const toggleExpanded = (id: number) => {
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  if (accountLoading) return <Panel><LoadingState label="正在读取站点账户" /></Panel>;
  if (!account) return <Panel><EmptyState title="尚未连接站点账户" description="连接后可读取真实余额和上游使用日志；模型推理仍由 API Key 负责。" action={<Button variant="primary" onClick={onOpenSettings}>打开站点账户设置</Button>} /></Panel>;
  if (!account.capabilities.usage) return <Panel><EmptyState title="当前面板不支持使用日志" description="余额读取仍可独立使用；系统不会伪造站点没有返回的记录。" /></Panel>;

  const successful = usage.filter((item) => item.status === 'success' || item.status === 'succeeded').length;

  return (
    <Panel className="site-usage-panel">
      <SectionHeader
        title="上游使用日志"
        description={`站点原始记录 · 当前 ${usage.length} 条 · ${usage.length ? ((successful / usage.length) * 100).toFixed(1) : '0.0'}% 成功`}
        actions={<Button icon={RefreshCw} size="sm" busy={syncing} onClick={() => void syncUsage()}>同步最新记录</Button>}
      />
      <div className="account-log-filters">
        <SearchField value={query} onChange={setQuery} placeholder="搜索模型、请求 ID 或 API Key" />
        <div className="segmented" role="group" aria-label="上游日志状态">
          {([['', '全部'], ['success', '成功'], ['failed', '失败']] as const).map(([value, label]) => <button type="button" className={status === value ? 'is-active' : ''} onClick={() => setStatus(value)} key={value || 'all'}>{label}</button>)}
        </div>
      </div>
      {error && <InlineNotice tone="danger">{error}</InlineNotice>}
      {loading ? <LoadingState label="正在读取上游使用记录" /> : usage.length === 0 ? <EmptyState title="没有上游使用记录" description="点击同步，或调整当前搜索与状态筛选。" /> : <>
        <div className="data-scroller">
          <table className="data-table upstream-usage-table">
            <thead><tr><th>时间</th><th>API Key</th><th>模型 / 思考</th><th>类型</th><th>Token 明细</th><th>首字 / 总耗时</th><th>真实扣费</th><th><span className="sr-only">详情</span></th></tr></thead>
            <tbody>{usage.map((item) => {
              const success = item.status === 'success' || item.status === 'succeeded';
              const failed = item.status === 'failed' || item.status === 'error';
              const detailsOpen = expanded.has(item.id);
              const extra = usageExtras(site, item);
              return <Fragment key={item.id}>
                <tr className="upstream-usage-row" key={`row-${item.id}`} tabIndex={0} aria-expanded={detailsOpen} onClick={() => toggleExpanded(item.id)} onKeyDown={(event) => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); toggleExpanded(item.id); } }}>
                  <td><div className="cell-stack"><strong>{formatDateTime(item.occurredAt)}</strong><code>{item.remoteId || item.requestId}</code></div></td>
                  <td><strong>{item.apiKeyName || '—'}</strong></td>
                  <td><div className="cell-stack"><code>{item.model || item.upstreamModel || '—'}</code><span>{extra.reasoningEffort}</span></div></td>
                  <td><div className="cell-stack"><span>{extra.requestType}</span><Badge tone={success ? 'success' : failed ? 'danger' : 'neutral'}>{usageStatusLabel(item.status)}</Badge></div></td>
                  <td><div className="usage-token-grid"><span>入 {formatTokens(item.inputTokens || 0)}</span><span>出 {formatTokens(item.outputTokens || 0)}</span><span>缓存 {formatTokens(item.cacheReadTokens || 0)}</span><span>推理 {formatTokens(item.reasoningTokens || 0)}</span></div></td>
                  <td><div className="cell-stack"><strong>首字 {seconds(extra.firstOutputMs)}</strong><span>总耗时 {seconds(item.durationMs)}</span></div></td>
                  <td><strong>{item.chargeValue ? `${item.chargeValue} ${item.chargeUnit || ''}` : '—'}</strong></td>
                  <td><ChevronDown className={detailsOpen ? 'is-open' : ''} size={16} /></td>
                </tr>
                {detailsOpen && <tr className="upstream-usage-detail" key={`detail-${item.id}`}><td colSpan={8}>
                  <div className="usage-detail-grid">
                    <div><span>JieShan 请求 ID</span><code>{item.requestId || '—'}</code></div>
                    <div><span>上游请求 ID</span><code>{item.upstreamRequestId || '—'}</code></div>
                    <div><span>请求路径</span><code>{extra.requestPath}</code></div>
                    <div><span>请求 IP / 地区</span><strong>{extra.requestIp} · {extra.region}</strong></div>
                    <div><span>上游分组</span><strong>{extra.group}</strong></div>
                    <div><span>计费模式</span><strong>{extra.billingMode}</strong></div>
                    <div><span>HTTP 状态</span><strong>{item.httpStatus ? `HTTP ${item.httpStatus}` : '—'}</strong></div>
                    <div><span>上游实际模型</span><code>{item.upstreamModel || item.model || '—'}</code></div>
                    <div><span>总 Token</span><strong>{formatTokens(item.totalTokens ?? ((item.inputTokens || 0) + (item.outputTokens || 0)))}</strong></div>
                    <div><span>记录同步时间</span><strong>{formatDateTime(item.sourceFetchedAt)}</strong></div>
                  </div>
                </td></tr>}
              </Fragment>;
            })}</tbody>
          </table>
        </div>
        <div className="mobile-upstream-usage-list">
          {usage.map((item) => <MobileUsageRecord
            key={item.id}
            site={site}
            item={item}
            expanded={expanded.has(item.id)}
            onToggle={() => toggleExpanded(item.id)}
          />)}
        </div>
        {hasMore && <div className="load-more-row"><Button busy={loadingMore} disabled={!cursor} onClick={() => void loadUsage(cursor)}>加载更多记录</Button></div>}
      </>}
    </Panel>
  );
}
