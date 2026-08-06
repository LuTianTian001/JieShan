import {
  Activity,
  AlertTriangle,
  CalendarClock,
  ExternalLink,
  FileClock,
  KeyRound,
  Pencil,
  RefreshCw,
  ServerCog,
  ShieldCheck,
  Trash2,
  Wallet,
} from 'lucide-react';
import { useState } from 'react';
import { Badge, Button, Dialog, EmptyState, ErrorState, LoadingState } from '../../components/ui';
import { useToast } from '../../components/Toast';
import { api } from '../../lib/api';
import { formatDateTime, formatLatency, formatTokens } from '../../lib/format';
import { useAsyncData } from '../../lib/hooks';
import type {
  AccountSyncState,
  AccountTarget,
  AccountUsageRange,
  SourceAmount,
  UpstreamAccount,
  UpstreamUsagePage,
  UpstreamUsageItem,
} from '../../lib/types';
import { AccountConfigDialog } from './AccountConfigDialog';

interface AccountBundle {
  adapters: Awaited<ReturnType<typeof api.accountAdapters>>;
  account: UpstreamAccount;
}

const syncLabels: Record<AccountSyncState, { label: string; tone: 'neutral' | 'success' | 'warning' | 'danger' | 'info' }> = {
  unconfigured: { label: '未配置', tone: 'neutral' },
  ready: { label: '同步正常', tone: 'success' },
  syncing: { label: '同步中', tone: 'info' },
  stale: { label: '数据陈旧', tone: 'warning' },
  error: { label: '同步失败', tone: 'danger' },
};

const rangeLabels: Array<{ value: AccountUsageRange; label: string }> = [
  { value: '24h', label: '24 小时' },
  { value: '7d', label: '7 天' },
  { value: '30d', label: '30 天' },
];

function amountText(amount: SourceAmount | null | undefined): string {
  if (!amount) return '-';
  return amount.display || `${amount.value} ${amount.currency}`;
}

function credentialSummary(account: UpstreamAccount): string {
  if (!account.auth) return '-';
  if (account.auth.kind === 'api_token') return account.auth.hasApiToken ? '管理 Token 已保存' : '管理 Token 缺失';
  const saved = [account.auth.hasAccessToken && 'Access', account.auth.hasRefreshToken && 'Refresh'].filter(Boolean);
  return saved.length === 2 ? '管理 Access + Refresh 已保存' : `${saved.join(' + ') || '管理 Token'} 不完整`;
}

function usageStatusTone(status: string | null): 'neutral' | 'success' | 'warning' | 'danger' {
  const normalized = status?.toLowerCase();
  if (!normalized) return 'neutral';
  if (['success', 'succeeded', 'ok', 'completed'].includes(normalized)) return 'success';
  if (['pending', 'processing', 'running'].includes(normalized)) return 'warning';
  return 'danger';
}

function UsageRow({ item }: { item: UpstreamUsageItem }) {
  const tokenSummary = item.inputTokens == null && item.outputTokens == null
    ? '-'
    : `${item.inputTokens == null ? '-' : formatTokens(item.inputTokens)} / ${item.outputTokens == null ? '-' : formatTokens(item.outputTokens)}`;
  const cacheSummary = item.cacheReadTokens == null && item.cacheCreationTokens == null
    ? '-'
    : `${item.cacheReadTokens == null ? '-' : formatTokens(item.cacheReadTokens)} / ${item.cacheCreationTokens == null ? '-' : formatTokens(item.cacheCreationTokens)}`;
  const reasoningSummary = [item.reasoningEffort, item.reasoningTokens == null ? null : formatTokens(item.reasoningTokens)].filter(Boolean).join(' · ') || '-';
  const timingSummary = item.firstTokenMs == null && item.durationMs == null
    ? '-'
    : `${formatLatency(item.firstTokenMs ?? null)} / ${formatLatency(item.durationMs ?? null)}`;
  const requestLabel = item.requestId || item.upstreamRequestId || item.externalId || '无请求 ID';
  const statusLabel = item.status || (item.httpStatus ? `HTTP ${item.httpStatus}` : '站点未提供');

  return (
    <div className="account-usage-row">
      <div className="account-usage-primary">
        <strong>{formatDateTime(item.occurredAt)}</strong>
        <code>{item.model || item.upstreamModel || '站点未提供模型'}</code>
        <span title={requestLabel}>{requestLabel}</span>
      </div>
      <div className="account-usage-metric"><span>输入 / 输出</span><strong>{tokenSummary}</strong></div>
      <div className="account-usage-metric"><span>缓存读 / 写</span><strong>{cacheSummary}</strong><small>{reasoningSummary === '-' ? '无思考数据' : `思考 ${reasoningSummary}`}</small></div>
      <div className="account-usage-metric"><span>TTFT / 总耗时</span><strong>{timingSummary}</strong></div>
      <div className="account-usage-metric"><span>站点扣费</span><strong>{amountText(item.amount)}</strong></div>
      <div className="account-usage-meta">
        <Badge tone={usageStatusTone(item.status)}>{statusLabel}</Badge>
        <span>{item.apiKeyName || item.groupName || '站点未提供 Key'}</span>
      </div>
    </div>
  );
}

export function AccountTab({ target, onChanged }: { target: AccountTarget; onChanged?: () => void }) {
  const toast = useToast();
  const [configOpen, setConfigOpen] = useState(false);
  const [removeOpen, setRemoveOpen] = useState(false);
  const [range, setRange] = useState<AccountUsageRange>('7d');
  const [usageVersion, setUsageVersion] = useState(0);
  const [refreshing, setRefreshing] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [olderItems, setOlderItems] = useState<UpstreamUsageItem[]>([]);
  const [nextBeforeId, setNextBeforeId] = useState<string | null>(null);
  const [hasMoreOverride, setHasMoreOverride] = useState<boolean | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);

  const bundle = useAsyncData<AccountBundle>(async () => {
    const [adapters, account] = await Promise.all([
      api.accountAdapters(),
      readAccount(target),
    ]);
    return { adapters, account };
  }, [target.kind, target.id]);

  const account = bundle.data?.account ?? null;
  const usageEnabled = Boolean(account?.configured && account.enabled && account.capabilities.usage);
  const usage = useAsyncData(async () => {
    if (!usageEnabled) return { items: [], range, lastSyncedAt: null };
    return readAccountUsage(target, range, 50);
  }, [target.kind, target.id, range, usageEnabled, usageVersion]);

  const updateAccount = (updated: UpstreamAccount) => {
    bundle.setData((current) => current ? { ...current, account: updated } : current);
    setOlderItems([]);
    setNextBeforeId(null);
    setHasMoreOverride(null);
    setUsageVersion((value) => value + 1);
    onChanged?.();
  };

  const changeRange = (next: AccountUsageRange) => {
    setRange(next);
    setOlderItems([]);
    setNextBeforeId(null);
    setHasMoreOverride(null);
  };

  const loadMore = async () => {
    const cursor = nextBeforeId ?? usage.data?.nextBeforeId ?? null;
    if (!cursor || loadingMore) return;
    setLoadingMore(true);
    try {
      const page = await readAccountUsage(target, range, 50, cursor);
      setOlderItems((current) => {
        const seen = new Set(current.map((item) => item.id));
        return [...current, ...page.items.filter((item) => !seen.has(item.id))];
      });
      setNextBeforeId(page.nextBeforeId ?? null);
      setHasMoreOverride(Boolean(page.hasMore));
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '加载更多记录失败', 'error');
    } finally {
      setLoadingMore(false);
    }
  };

  const refreshAccount = async () => {
    if (!account?.configured || !account.enabled) return;
    setRefreshing(true);
    try {
      updateAccount(await refreshAccountTarget(target));
      toast.show('账户数据已刷新', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '账户刷新失败', 'error');
      await bundle.refresh();
    } finally {
      setRefreshing(false);
    }
  };

  const removeAccount = async () => {
    setRemoving(true);
    try {
      await deleteAccountTarget(target);
      const updated = await readAccount(target);
      updateAccount(updated);
      setRemoveOpen(false);
      toast.show('账户连接已移除，推理 API Key 未受影响', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '移除账户连接失败', 'error');
    } finally {
      setRemoving(false);
    }
  };

  if (bundle.loading && !bundle.data) return <LoadingState label="正在读取账户连接" />;
  if (bundle.error && !bundle.data) return <ErrorState message={bundle.error} onRetry={() => void bundle.refresh()} />;
  if (!bundle.data || !account) return null;

  const sync = syncLabels[account.sync.state];
  const snapshot = account.snapshot;
  const subscription = snapshot?.subscription;

  return (
    <div className="account-tab">
      <div className="account-scope-notice">
        <ShieldCheck size={17} aria-hidden="true" />
        <div><strong>只展示上游原始账户数据</strong><span>余额、套餐和使用记录不会参与线路路由，也不会改变下游官方美元计费。</span></div>
      </div>

      {!account.configured ? (
        <div className="account-unconfigured">
          <ServerCog size={28} aria-hidden="true" />
          <div><strong>尚未连接站点账户</strong><p>选择站点适配器并提供管理 Token 后，面板才能定时读取余额、套餐和原始使用记录；这里不使用推理 API Key。</p></div>
          <Button variant="primary" icon={KeyRound} disabled={bundle.data.adapters.length === 0} onClick={() => setConfigOpen(true)}>配置账户</Button>
          {bundle.data.adapters.length === 0 && <span className="muted-copy">服务端没有返回可用的账户适配器。</span>}
        </div>
      ) : (
        <>
          <div className="account-toolbar">
            <div className="account-sync-identity">
              <Badge tone={account.enabled ? sync.tone : 'neutral'}>{account.enabled ? sync.label : '同步已停用'}</Badge>
              <span>{account.adapter?.label || account.adapter?.key || '未知适配器'}</span>
            </div>
            <div className="account-toolbar-actions">
              <Button size="sm" variant="ghost" icon={Pencil} onClick={() => setConfigOpen(true)}>编辑</Button>
              <Button size="sm" icon={RefreshCw} busy={refreshing} disabled={!account.enabled} onClick={() => void refreshAccount()}>立即同步</Button>
            </div>
          </div>

          <div className="account-summary-grid">
            <div className="account-summary-item account-balance-summary">
              <span><Wallet size={15} aria-hidden="true" />站点余额</span>
              <strong>{amountText(snapshot?.balance)}</strong>
              <small>{snapshot?.balance?.sourceLabel || '按站点原始口径'}</small>
            </div>
            <div className="account-summary-item">
              <span><Activity size={15} aria-hidden="true" />当前套餐</span>
              <strong>{subscription?.planName || '-'}</strong>
              <small>{subscription?.remaining ? amountText(subscription.remaining) : subscription?.status || '站点未返回套餐'}</small>
            </div>
            <div className="account-summary-item">
              <span><CalendarClock size={15} aria-hidden="true" />到期 / 续期</span>
              <strong>{formatDateTime(subscription?.expiresAt || subscription?.renewsAt || null)}</strong>
              <small>{subscription?.expiresAt ? '套餐到期时间' : subscription?.renewsAt ? '下次续期时间' : '站点未返回时间'}</small>
            </div>
          </div>

          <div className={`account-sync-panel account-sync-${account.enabled ? account.sync.state : 'unconfigured'}`}>
            <div className="account-panel-heading">
              <div><FileClock size={16} aria-hidden="true" /><strong>账户同步</strong><Badge tone={account.enabled ? sync.tone : 'neutral'}>{account.enabled ? sync.label : '已停用'}</Badge></div>
              <span>独立于路由健康状态</span>
            </div>
            <dl className="account-sync-times">
              <div><dt>最近成功</dt><dd>{formatDateTime(account.sync.lastSuccessAt)}</dd></div>
              <div><dt>最近尝试</dt><dd>{formatDateTime(account.sync.lastAttemptAt)}</dd></div>
              <div><dt>下次同步</dt><dd>{account.enabled ? formatDateTime(account.sync.nextAt) : '-'}</dd></div>
              <div><dt>快照时间</dt><dd>{formatDateTime(snapshot?.capturedAt || null)}</dd></div>
            </dl>
            {account.sync.error && <div className="account-sync-error"><AlertTriangle size={15} aria-hidden="true" /><span><strong>{account.sync.error.code}</strong>{account.sync.error.message}</span></div>}
            {account.sync.stale && <p className="account-stale-note">当前继续展示上一次成功快照，请以标注的快照时间为准。</p>}
          </div>

          <section className="account-usage-section">
            <div className="account-panel-heading">
              <div><Activity size={16} aria-hidden="true" /><strong>站点使用记录</strong></div>
              {account.capabilities.usage && <div className="usage-range-switch" role="group" aria-label="使用记录时间范围">
                {rangeLabels.map((item) => <button type="button" className={range === item.value ? 'is-active' : ''} key={item.value} onClick={() => changeRange(item.value)}>{item.label}</button>)}
              </div>}
            </div>
            {!account.capabilities.usage ? (
              <p className="account-section-note">当前适配器不支持读取站点使用记录。</p>
            ) : usage.loading && !usage.data ? (
              <div className="account-section-loading"><RefreshCw className="spin" size={16} />正在读取原始记录</div>
            ) : usage.error ? (
              <div className="account-inline-error"><span>{usage.error}</span><Button size="sm" onClick={() => void usage.refresh()}>重试</Button></div>
            ) : usage.data?.items.length || olderItems.length ? (
              <div className="account-usage-list">{[...(usage.data?.items ?? []), ...olderItems].map((item) => <UsageRow item={item} key={item.id} />)}</div>
            ) : (
              <EmptyState title="当前范围没有记录" description="站点没有返回可展示的原始使用记录。" />
            )}
            {account.capabilities.usage && usage.data && <div className="account-usage-footnote"><span>{(hasMoreOverride ?? usage.data.hasMore) && (nextBeforeId ?? usage.data.nextBeforeId) ? <Button size="sm" busy={loadingMore} onClick={() => void loadMore()}>加载更多</Button> : '已显示当前范围内全部记录'}</span><span>最近同步 {formatDateTime(usage.data.lastSyncedAt)}</span></div>}
          </section>

          <section className="account-connection-section">
            <div className="account-panel-heading">
              <div><ServerCog size={16} aria-hidden="true" /><strong>连接信息</strong></div>
              <Button size="sm" variant="ghost" icon={Trash2} onClick={() => setRemoveOpen(true)}>移除连接</Button>
            </div>
            <dl className="compact-definition account-connection-definition">
              <div><dt>适配器</dt><dd>{account.adapter?.label || '-'}</dd></div>
              <div><dt>管理凭据</dt><dd>{credentialSummary(account)}</dd></div>
              {account.auth?.kind === 'access_refresh' && <div><dt>Access 到期</dt><dd>{formatDateTime(account.auth.accessTokenExpiresAt)}</dd></div>}
              <div><dt>站点面板</dt><dd>{account.dashboardUrl ? <a href={account.dashboardUrl} target="_blank" rel="noreferrer">打开站点<ExternalLink size={12} aria-hidden="true" /></a> : '-'}</dd></div>
            </dl>
          </section>
        </>
      )}

      <AccountConfigDialog
        open={configOpen}
        target={target}
        adapters={bundle.data.adapters}
        account={account}
        onClose={() => setConfigOpen(false)}
        onSaved={updateAccount}
      />

      <Dialog
        open={removeOpen}
        title="移除账户连接"
        description="只会删除余额、套餐和使用记录的连接配置，不会删除站点、接入地址或推理 API Key。"
        onClose={() => setRemoveOpen(false)}
        width="sm"
        footer={<><Button onClick={() => setRemoveOpen(false)}>取消</Button><Button variant="danger" icon={Trash2} busy={removing} onClick={() => void removeAccount()}>确认移除</Button></>}
      >
        <div className="account-remove-summary"><strong>{target.name}</strong><span>{account.adapter?.label || '账户适配器'}</span><code>{account.dashboardUrl}</code></div>
      </Dialog>
    </div>
  );
}

function readAccount(target: AccountTarget): Promise<UpstreamAccount> {
  return target.kind === 'site' ? api.siteAccount(target.id) : api.upstreamAccount(target.id);
}

function readAccountUsage(target: AccountTarget, range: AccountUsageRange, limit: number, beforeId?: string): Promise<UpstreamUsagePage> {
  return target.kind === 'site'
    ? api.siteAccountUsage(target.id, range, limit, beforeId)
    : api.upstreamAccountUsage(target.id, range, limit, beforeId);
}

function refreshAccountTarget(target: AccountTarget): Promise<UpstreamAccount> {
  return target.kind === 'site' ? api.refreshSiteAccount(target.id) : api.refreshUpstreamAccount(target.id);
}

function deleteAccountTarget(target: AccountTarget): Promise<void> {
  return target.kind === 'site' ? api.deleteSiteAccount(target.id) : api.deleteUpstreamAccount(target.id);
}
