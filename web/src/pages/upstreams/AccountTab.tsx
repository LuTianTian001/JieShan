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
import { formatDateTime, formatRelativeTime, formatTokens } from '../../lib/format';
import { useAsyncData } from '../../lib/hooks';
import type {
  AccountSyncState,
  AccountUsageRange,
  SourceAmount,
  Upstream,
  UpstreamAccount,
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
  if (account.auth.kind === 'api_token') return account.auth.hasApiToken ? 'API Token 已保存' : 'API Token 缺失';
  const saved = [account.auth.hasAccessToken && 'Access', account.auth.hasRefreshToken && 'Refresh'].filter(Boolean);
  return saved.length === 2 ? 'Access + Refresh 已保存' : `${saved.join(' + ') || 'Token'} 不完整`;
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

  return (
    <div className="account-usage-row">
      <div className="account-usage-primary">
        <code>{item.model || '未知模型'}</code>
        <span>{item.sourceText || item.externalId || '站点原始记录'}</span>
      </div>
      <div className="account-usage-metric"><span>输入 / 输出</span><strong>{tokenSummary}</strong></div>
      <div className="account-usage-metric"><span>站点扣费</span><strong>{amountText(item.amount)}</strong></div>
      <div className="account-usage-meta">
        <Badge tone={usageStatusTone(item.status)}>{item.status || '未知'}</Badge>
        <span title={formatDateTime(item.occurredAt)}>{formatRelativeTime(item.occurredAt)}</span>
      </div>
    </div>
  );
}

export function AccountTab({ upstream }: { upstream: Upstream }) {
  const toast = useToast();
  const [configOpen, setConfigOpen] = useState(false);
  const [removeOpen, setRemoveOpen] = useState(false);
  const [range, setRange] = useState<AccountUsageRange>('7d');
  const [usageVersion, setUsageVersion] = useState(0);
  const [refreshing, setRefreshing] = useState(false);
  const [removing, setRemoving] = useState(false);

  const bundle = useAsyncData<AccountBundle>(async () => {
    const [adapters, account] = await Promise.all([
      api.accountAdapters(),
      api.upstreamAccount(upstream.id),
    ]);
    return { adapters, account };
  }, [upstream.id]);

  const account = bundle.data?.account ?? null;
  const usageEnabled = Boolean(account?.configured && account.enabled && account.capabilities.usage);
  const usage = useAsyncData(async () => {
    if (!usageEnabled) return { items: [], range, lastSyncedAt: null };
    return api.upstreamAccountUsage(upstream.id, range, 50);
  }, [upstream.id, range, usageEnabled, usageVersion]);

  const updateAccount = (updated: UpstreamAccount) => {
    bundle.setData((current) => current ? { ...current, account: updated } : current);
    setUsageVersion((value) => value + 1);
  };

  const refreshAccount = async () => {
    if (!account?.configured || !account.enabled) return;
    setRefreshing(true);
    try {
      updateAccount(await api.refreshUpstreamAccount(upstream.id));
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
      await api.deleteUpstreamAccount(upstream.id);
      const updated = await api.upstreamAccount(upstream.id);
      updateAccount(updated);
      setRemoveOpen(false);
      toast.show('账户连接已移除，上游 API Key 未受影响', 'success');
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
          <div><strong>尚未连接站点账户</strong><p>选择站点适配器并提供账户 Token 后，面板才能定时读取余额、套餐和原始使用记录。</p></div>
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
                {rangeLabels.map((item) => <button type="button" className={range === item.value ? 'is-active' : ''} key={item.value} onClick={() => setRange(item.value)}>{item.label}</button>)}
              </div>}
            </div>
            {!account.capabilities.usage ? (
              <p className="account-section-note">当前适配器不支持读取站点使用记录。</p>
            ) : usage.loading && !usage.data ? (
              <div className="account-section-loading"><RefreshCw className="spin" size={16} />正在读取原始记录</div>
            ) : usage.error ? (
              <div className="account-inline-error"><span>{usage.error}</span><Button size="sm" onClick={() => void usage.refresh()}>重试</Button></div>
            ) : usage.data?.items.length ? (
              <div className="account-usage-list">{usage.data.items.map((item) => <UsageRow item={item} key={item.id} />)}</div>
            ) : (
              <EmptyState title="当前范围没有记录" description="站点没有返回可展示的原始使用记录。" />
            )}
            {account.capabilities.usage && usage.data && <div className="account-usage-footnote"><span>最多显示最近 50 条</span><span>最近同步 {formatDateTime(usage.data.lastSyncedAt)}</span></div>}
          </section>

          <section className="account-connection-section">
            <div className="account-panel-heading">
              <div><ServerCog size={16} aria-hidden="true" /><strong>连接信息</strong></div>
              <Button size="sm" variant="ghost" icon={Trash2} onClick={() => setRemoveOpen(true)}>移除连接</Button>
            </div>
            <dl className="compact-definition account-connection-definition">
              <div><dt>适配器</dt><dd>{account.adapter?.label || '-'}</dd></div>
              <div><dt>账户凭据</dt><dd>{credentialSummary(account)}</dd></div>
              {account.auth?.kind === 'access_refresh' && <div><dt>Access 到期</dt><dd>{formatDateTime(account.auth.accessTokenExpiresAt)}</dd></div>}
              <div><dt>站点面板</dt><dd>{account.dashboardUrl ? <a href={account.dashboardUrl} target="_blank" rel="noreferrer">打开站点<ExternalLink size={12} aria-hidden="true" /></a> : '-'}</dd></div>
            </dl>
          </section>
        </>
      )}

      <AccountConfigDialog
        open={configOpen}
        upstream={upstream}
        adapters={bundle.data.adapters}
        account={account}
        onClose={() => setConfigOpen(false)}
        onSaved={updateAccount}
      />

      <Dialog
        open={removeOpen}
        title="移除账户连接"
        description="只会删除余额、套餐和使用记录的连接配置，不会删除上游或代理 API Key。"
        onClose={() => setRemoveOpen(false)}
        width="sm"
        footer={<><Button onClick={() => setRemoveOpen(false)}>取消</Button><Button variant="danger" icon={Trash2} busy={removing} onClick={() => void removeAccount()}>确认移除</Button></>}
      >
        <div className="account-remove-summary"><strong>{upstream.name}</strong><span>{account.adapter?.label || '账户适配器'}</span><code>{account.dashboardUrl}</code></div>
      </Dialog>
    </div>
  );
}
