import { ArrowRight, BrainCircuit, Check, ChevronDown, Clipboard, Clock3, Coins, SlidersHorizontal, Timer, X } from 'lucide-react';
import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  FilterBar,
  InlineNotice,
  LoadingState,
  PageHeader,
  Panel,
  SearchField,
  IconButton,
  UnavailableState,
} from '../components/ui';
import { api, ApiUnavailableError, endpointProfiles } from '../lib/api';
import type { GatewayLogFilter } from '../lib/api';
import { formatDateTime, formatTokens, formatUSDFromNano, surfaceLabel } from '../lib/format';
import { useDebouncedValue, useResource } from '../lib/hooks';
import type {
  GatewayLog,
  GatewayLogAttempt,
  GatewayLogPage,
  GatewayLogSummary,
  GatewayMeteringStatus,
  GatewayRouteCandidate,
  GatewayRouteCredentialSnapshot,
  InferenceSurface,
} from '../lib/types';

interface LogData {
  page: GatewayLogPage | null;
  summary: GatewayLogSummary | null;
}

function statusTone(status: string): 'success' | 'warning' | 'danger' | 'neutral' | 'info' {
  if (status === 'success' || status === 'settled') return 'success';
  if (status === 'running') return 'info';
  if (status === 'failed' || status === 'error') return 'danger';
  if (status === 'cancelled' || status === 'canceled') return 'warning';
  return 'neutral';
}

function statusLabel(status: string): string {
  return { success: '成功', settled: '成功', running: '进行中', failed: '失败', error: '失败', cancelled: '已取消', canceled: '已取消' }[status] || status || '未知';
}

function meteringTone(status: GatewayMeteringStatus): 'success' | 'warning' | 'neutral' | 'info' {
  if (status === 'metered') return 'success';
  if (status === 'unavailable') return 'warning';
  if (status === 'pending') return 'info';
  return 'neutral';
}

function meteringLabel(status: GatewayMeteringStatus): string {
  return {
    pending: '待计量',
    metered: '已计量',
    unavailable: '用量不可用',
    not_applicable: '未计量',
  }[status];
}

function officialCostLabel(log: GatewayLog): string {
  if (log.meteringStatus === 'unavailable') return '用量不可用';
  if (log.meteringStatus === 'not_applicable') return '未计量';
  if (log.meteringStatus === 'pending') return '待结算';
  return formatUSDFromNano(log.officialCostNanoUSD);
}

function finalAttempt(log: GatewayLog): GatewayLogAttempt | undefined {
  return log.finalAttempt || [...(log.attempts || [])].sort((left, right) => right.attemptIndex - left.attemptIndex)[0];
}

function reasoningLabel(value: string): string {
  const normalized = value?.trim().toLowerCase();
  return ['default', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh'].includes(normalized) ? normalized : normalized || 'default';
}

function surfacePath(surface: InferenceSurface): string {
  return {
    'openai.chat_completions': '/v1/chat/completions',
    'openai.responses': '/v1/responses',
    'anthropic.messages': '/v1/messages',
    'gemini.generate_content': '/v1beta/models/{model}:generateContent',
  }[surface];
}

function formatSeconds(value: number | null | undefined): string {
  if (value === null || value === undefined) return '—';
  return `${(value / 1_000).toFixed(2)} 秒`;
}

function firstOutputLabel(stream: boolean, value: number | null | undefined): string {
  return stream ? formatSeconds(value) : '非流式';
}

function attemptCount(log: GatewayLog): number {
  if (log.attempts?.length) {
    const uniqueUpstreams = new Set(log.attempts.map((attempt) => `${attempt.providerModelTargetId}:${attempt.credentialId}`));
    return uniqueUpstreams.size || log.attempts.length;
  }
  const attempt = finalAttempt(log);
  return attempt ? attempt.attemptIndex + 1 : 0;
}

function routingOutcome(log: GatewayLog): { label: string; tone: 'success' | 'warning' | 'danger' | 'info' | 'neutral' } {
  const attempts = attemptCount(log);
  const succeeded = log.status === 'success' || log.status === 'settled';
  if (!attempts) return { label: '未进入上游', tone: log.status === 'running' ? 'info' : 'neutral' };
  if (succeeded && (log.switchCount > 0 || attempts > 1)) return { label: `切换后成功 · 尝试 ${attempts} 个上游`, tone: 'warning' };
  if (succeeded) return { label: '首选成功', tone: 'success' };
  if (log.status === 'running') return { label: `请求进行中 · 已尝试 ${attempts} 个上游`, tone: 'info' };
  if (log.status === 'cancelled' || log.status === 'canceled') return { label: `请求已取消 · 尝试 ${attempts} 个上游`, tone: 'warning' };
  return { label: `所有上游失败 · 尝试 ${attempts} 个上游`, tone: 'danger' };
}

function successRate(basisPoints: number): string {
  return `${(basisPoints / 100).toLocaleString('zh-CN', { maximumFractionDigits: 2 })}%`;
}

function routeLabel(log: GatewayLog): string {
  if (!log.effectiveRoutingProfileName) return log.publishedModelId ? `发布模型 #${log.publishedModelId}` : '尚未进入路由';
  if (log.sourceRoutingProfileName && log.sourceRoutingProfileId !== log.effectiveRoutingProfileId) {
    return `${log.effectiveRoutingProfileName} · 继承 ${log.sourceRoutingProfileName}`;
  }
  return log.effectiveRoutingProfileName;
}

function dateTimeMillis(value: string): number | undefined {
  if (!value) return undefined;
  const parsed = new Date(value).getTime();
  return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined;
}

function signedUSDDelta(value: number): string {
  if (!value) return '-';
  return `${value > 0 ? '+' : '-'}${formatUSDFromNano(Math.abs(value))}`;
}

function ledgerEventLabel(value: string): string {
  return { reserve: '预留', settle: '结算' }[value] || value || '账本事件';
}

function routeRevisionLabel(value: number): string {
  return value > 0 ? `Revision ${value}` : '历史记录 / 未知版本';
}

function candidateEligibilityLabel(value: string): string {
  return { eligible: '初始可用', skipped: '初始不可用' }[value] || value || '初始状态未知';
}

function candidateEligibilityTone(value: string): 'success' | 'warning' | 'neutral' {
  if (value === 'eligible') return 'success';
  if (value === 'skipped') return 'warning';
  return 'neutral';
}

function candidateDispositionLabel(value: string): string {
  return {
    attempted: '已尝试',
    skipped: '已跳过',
    not_attempted: '未轮询到',
    pending: '请求处理中',
  }[value] || value || '最终状态未知';
}

function candidateDispositionTone(value: string): 'info' | 'warning' | 'neutral' {
  if (value === 'attempted' || value === 'pending') return 'info';
  if (value === 'skipped') return 'warning';
  return 'neutral';
}

function routeReasonLabel(value: string): string {
  const label = {
    ready: '目标和凭证均可用',
    half_open: '冷却结束后的半开试探',
    target_disabled: '目标已停用',
    no_eligible_credentials: '没有可用凭证',
    cooling: '目标仍在冷却',
    half_open_lease_held: '半开探针名额已占用',
    unsupported: '当前目标不支持该模型',
    stale_revision: '目标版本已过期',
    stale_sequence: '健康状态序列已过期',
    request_succeeded: '请求已由前序目标完成',
    request_cancelled: '请求已取消',
    request_timeout: '请求已超时',
    candidates_exhausted: '候选目标已耗尽',
    runtime_unavailable: '运行时不可用',
    request_failed: '请求失败',
  }[value];
  return label ? `${label} (${value})` : value || '-';
}

function candidateAttemptLabel(candidate: GatewayRouteCandidate): string {
  if (!candidate.attemptCount) return '未尝试';
  if (candidate.firstAttemptIndex === null || candidate.lastAttemptIndex === null) return `${candidate.attemptCount} 次`;
  const first = candidate.firstAttemptIndex + 1;
  const last = candidate.lastAttemptIndex + 1;
  return first === last ? `${candidate.attemptCount} 次（#${first}）` : `${candidate.attemptCount} 次（#${first} - #${last}）`;
}

function credentialSnapshotLabel(credential: GatewayRouteCredentialSnapshot): string {
  const state = {
    ready: '可用',
    active: '可用',
    cooling: '冷却中',
    invalid: '已失效',
    exhausted: '已耗尽',
    disabled: '已停用',
  }[credential.runtimeState] || credential.runtimeState || '状态未知';
  const cooling = credential.coolingUntil ? `，至 ${formatDateTime(credential.coolingUntil)}` : '';
  return `#${credential.position + 1} ${credential.name}：${state}${cooling}`;
}

function billingMultiplier(log: GatewayLog): number | null {
  return Number.isFinite(log.billingMultiplier) ? log.billingMultiplier : null;
}

function formatMultiplier(value: number | null): string {
  return value === null ? '—' : `${value.toLocaleString('en-US', { minimumFractionDigits: 1, maximumFractionDigits: 2 })}x`;
}

function CopyableValue({ value, label, code = false }: { value: string; label: string; code?: boolean }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    if (!value || value === '—') return;
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1_500);
    } catch {
      // The original value remains visible and selectable when clipboard access is unavailable.
    }
  };
  const content = code ? <code style={{ minWidth: 0, overflowWrap: 'anywhere', wordBreak: 'break-word', whiteSpace: 'normal' }}>{value || '—'}</code> : <span style={{ minWidth: 0, overflowWrap: 'anywhere', wordBreak: 'break-word' }}>{value || '—'}</span>;
  return <span style={{ minWidth: 0, width: '100%', display: 'flex', alignItems: 'center', gap: 5 }}>{content}{value && value !== '—' && <IconButton label={`复制${label}`} onClick={() => void copy()}>{copied ? <Check size={14} /> : <Clipboard size={14} />}</IconButton>}</span>;
}

function LogDrawer({
  open,
  title,
  description,
  onClose,
  children,
}: {
  open: boolean;
  title: string;
  description?: string;
  onClose: () => void;
  children: ReactNode;
}) {
  const titleId = useId();
  const descriptionId = useId();
  const panelRef = useRef<HTMLElement | null>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    if (!open) return undefined;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const oldOverflow = document.body.style.overflow;
    const keydown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onCloseRef.current();
    };
    document.body.style.overflow = 'hidden';
    document.addEventListener('keydown', keydown);
    const frame = window.requestAnimationFrame(() => panelRef.current?.focus());
    return () => {
      window.cancelAnimationFrame(frame);
      document.body.style.overflow = oldOverflow;
      document.removeEventListener('keydown', keydown);
      previous?.focus();
    };
  }, [open]);

  if (!open) return null;
  return createPortal(
    <div className="overlay log-drawer-overlay" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <aside ref={panelRef} className="dialog log-drawer" role="dialog" aria-modal="true" aria-labelledby={titleId} aria-describedby={description ? descriptionId : undefined} tabIndex={-1}>
        <header className="dialog-header"><div className="log-drawer-title"><h2 id={titleId}>{title}</h2>{description && <p id={descriptionId}>{description}</p>}</div><IconButton label="关闭" onClick={onClose}><X size={18} /></IconButton></header>
        <div className="dialog-body log-drawer-body">{children}</div>
      </aside>
    </div>,
    document.body,
  );
}

function RouteTimeline({ log }: { log: GatewayLog }) {
  const attempts = [...log.attempts].sort((left, right) => left.attemptIndex - right.attemptIndex);
  const attemptsFor = (candidate: GatewayRouteCandidate) => attempts.filter((attempt) => attempt.providerModelTargetId === candidate.providerModelTargetId);

  if (!log.routeCandidates.length && !attempts.length) return <EmptyState title="没有路由时间线" description="请求可能在生成路由候选前已经结束。" />;

  if (!log.routeCandidates.length) {
    return <div className="attempt-list">{attempts.map((attempt) => <div key={attempt.id}>
      <span className={`attempt-node attempt-${attempt.status}`} />
      <header><strong>#{attempt.attemptIndex + 1} {attempt.siteName}</strong><Badge tone={statusTone(attempt.status)}>{statusLabel(attempt.status)}</Badge></header>
      <div className="model-transition"><code>{attempt.sourceModel}</code>{attempt.responseModel && attempt.responseModel !== attempt.sourceModel && <><ArrowRight size={12} /><code>{attempt.responseModel}</code></>}</div>
      <p>API Key：{attempt.credentialName} · 请求入口：{surfacePath(attempt.apiSurface)}</p>
      <section style={{ gridColumn: '1 / -1', display: 'grid', gap: 5, borderTop: '1px solid var(--line)', paddingTop: 8, minWidth: 0 }}><span>开始 {formatDateTime(attempt.startedAt || null)} · 结束 {formatDateTime(attempt.finishedAt || null)}</span><span>首字延迟 {firstOutputLabel(log.stream, attempt.firstOutputMs)} · 总耗时 {formatSeconds(attempt.durationMs)} · {attempt.httpStatus ? `HTTP ${attempt.httpStatus}` : '无 HTTP 状态'}</span>{attempt.switchReason && <span>切换原因：{routeReasonLabel(attempt.switchReason)}</span>}{attempt.errorCode && <CopyableValue label="上游错误" value={`${attempt.failureKind || '上游错误'} · ${attempt.errorCode}`} />}</section>
    </div>)}</div>;
  }

  return <div className="attempt-list">{log.routeCandidates.map((candidate) => {
    const candidateAttempts = attemptsFor(candidate);
    const lastAttempt = candidateAttempts[candidateAttempts.length - 1];
    return <div key={`${candidate.providerModelTargetId}-${candidate.position}`}>
      <span className={`attempt-node ${lastAttempt ? `attempt-${lastAttempt.status}` : ''}`} />
      <header><strong>#{candidate.position + 1} {candidate.siteName}</strong><Badge tone={candidateEligibilityTone(candidate.initialEligibility)}>{candidateEligibilityLabel(candidate.initialEligibility)}</Badge><Badge tone={candidateDispositionTone(candidate.disposition)}>{candidateDispositionLabel(candidate.disposition)}</Badge></header>
      <div className="model-transition"><code>{candidate.sourceModel}</code></div>
      <p>{candidate.wireProtocol} · {surfacePath(candidate.apiSurface)} · {surfaceLabel(candidate.apiSurface)}</p>
      <section style={{ gridColumn: '1 / -1', display: 'grid', gap: 6, borderTop: '1px solid var(--line)', paddingTop: 8, minWidth: 0 }}>
        <small>初始：{routeReasonLabel(candidate.initialReason)} · 最终：{routeReasonLabel(candidate.dispositionReason)} · {candidateAttemptLabel(candidate)}</small>
        {candidateAttempts.length ? candidateAttempts.map((attempt) => <div key={attempt.id} style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) auto', gap: 6, border: '1px solid var(--line)', borderRadius: 'var(--radius-xs)', padding: 7, minWidth: 0 }}><strong>尝试 #{attempt.attemptIndex + 1} · {attempt.credentialName}</strong><Badge tone={statusTone(attempt.status)}>{statusLabel(attempt.status)}</Badge><span style={{ gridColumn: '1 / -1', minWidth: 0 }}>开始 {formatDateTime(attempt.startedAt || null)} · 结束 {formatDateTime(attempt.finishedAt || null)} · 首字 {firstOutputLabel(log.stream, attempt.firstOutputMs)} · 总耗时 {formatSeconds(attempt.durationMs)} · {attempt.httpStatus ? `HTTP ${attempt.httpStatus}` : '无 HTTP 状态'}</span>{attempt.switchReason && <span style={{ gridColumn: '1 / -1' }}>切换原因：{routeReasonLabel(attempt.switchReason)}</span>}{attempt.errorCode && <span style={{ gridColumn: '1 / -1', minWidth: 0 }}><CopyableValue label="上游错误" value={`${attempt.failureKind || '上游错误'} · ${attempt.errorCode}`} /></span>}</div>) : <small>未发起请求：{candidate.credentials.length ? candidate.credentials.map(credentialSnapshotLabel).join('；') : '没有可用凭证快照'}</small>}
      </section>
    </div>;
  })}</div>;
}

export function LogsPage() {
  const [query, setQuery] = useState('');
  const debouncedQuery = useDebouncedValue(query);
  const [status, setStatus] = useState('');
  const [surface, setSurface] = useState<InferenceSurface | ''>('');
  const [siteId, setSiteId] = useState('');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [selected, setSelected] = useState<GatewayLog | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState('');
  const [detailTab, setDetailTab] = useState<'overview' | 'usage' | 'route'>('overview');
  const [loadingMore, setLoadingMore] = useState(false);
  const [loadMoreError, setLoadMoreError] = useState('');
  const detailRequestIdRef = useRef(0);
  const sites = useResource(() => api.listSites(), []);
  const activeFilter = useMemo<GatewayLogFilter>(() => ({
    query: debouncedQuery || undefined,
    status: status || undefined,
    surface: surface || undefined,
    siteId: siteId ? Number(siteId) : undefined,
    from: dateTimeMillis(from),
    to: dateTimeMillis(to),
  }), [debouncedQuery, from, siteId, status, surface, to]);
  const hasAdvancedFilter = Boolean(surface || siteId || from || to);
  const resource = useResource(async (): Promise<LogData> => {
    try {
      const [page, summary] = await Promise.all([
        api.gatewayLogs({ ...activeFilter, limit: 100 }),
        api.gatewayLogSummary(activeFilter),
      ]);
      return { page, summary };
    } catch (error) {
      if (error instanceof ApiUnavailableError) return { page: null, summary: null };
      throw error;
    }
  }, [activeFilter]);

  const rows = resource.data?.page?.items || [];
  const summary = resource.data?.summary;

  const openDetail = async (summary: GatewayLog) => {
    const requestId = ++detailRequestIdRef.current;
    setSelected(summary);
    setDetailTab('overview');
    setDetailLoading(true);
    setDetailError('');
    try {
      const detail = await api.gatewayLogDetail(summary.id);
      if (detailRequestIdRef.current !== requestId) return;
      setSelected(detail);
    } catch (reason) {
      if (detailRequestIdRef.current !== requestId) return;
      setDetailError(reason instanceof Error ? reason.message : '调用详情加载失败');
    } finally {
      if (detailRequestIdRef.current === requestId) setDetailLoading(false);
    }
  };

  const closeDetail = () => {
    detailRequestIdRef.current += 1;
    setSelected(null);
    setDetailError('');
    setDetailLoading(false);
  };

  const loadMore = async () => {
    const page = resource.data?.page;
    if (!page?.hasMore || !page.nextCursor) return;
    setLoadingMore(true);
    setLoadMoreError('');
    try {
      const next = await api.gatewayLogs({
        ...activeFilter,
        cursor: page.nextCursor,
        limit: 100,
      });
      resource.setData((current) => current?.page ? {
        ...current,
        page: {
          ...next,
          items: [...current.page.items, ...next.items.filter((item) => !current.page?.items.some((existing) => existing.id === item.id))],
        },
      } : current);
    } catch (reason) {
      setLoadMoreError(reason instanceof Error ? reason.message : '更多调用加载失败');
    } finally {
      setLoadingMore(false);
    }
  };

  return (
    <div className="page logs-page">
      <PageHeader title="调用日志" description="记录经过 JieShan 的下游请求、官方计费和每一次上游切换；不保存完整提示词与回答。" />

      {!resource.loading && resource.data && !resource.data.page ? <Panel><UnavailableState title="调用日志暂不可用" description="当前实例尚未提供结构化请求日志，请检查服务状态后重试。" /></Panel> : <Panel className="list-panel">
        <FilterBar trailing={<div className="log-summary"><span>{summary?.requests ?? 0} 条请求</span><span>{successRate(summary?.successBasisPoints ?? 0)} 成功</span><span>发生切换 {summary?.requestsWithSwitches ?? 0} 条</span></div>}>
          <SearchField value={query} onChange={setQuery} placeholder="搜索请求 ID、模型、Key 或上游" />
          <div className="segmented" role="group" aria-label="请求状态">{[
            ['', '全部'],
            ['success', '成功'],
            ['failed', '失败'],
            ['running', '进行中'],
              ['cancelled', '已取消'],
          ].map(([value, label]) => <button type="button" className={status === value ? 'is-active' : ''} onClick={() => setStatus(value)} key={value || 'all'}>{label}</button>)}</div>
        </FilterBar>
        <details className="log-advanced-filters">
          <summary><span><SlidersHorizontal size={16} />高级筛选</span>{hasAdvancedFilter && <Badge tone="info">已启用</Badge>}<ChevronDown size={16} /></summary>
          <div className="log-filter-grid">
            <label><span>接口</span><select className="select" value={surface} onChange={(event) => setSurface(event.target.value as InferenceSurface | '')}><option value="">全部接口</option>{endpointProfiles.map((profile) => <option value={profile.surface} key={profile.surface}>{profile.label}</option>)}</select></label>
            <label><span>上游站点</span><select className="select" value={siteId} onChange={(event) => setSiteId(event.target.value)}><option value="">全部站点</option>{(sites.data || []).map((site) => <option value={site.id} key={site.id}>{site.name}</option>)}</select></label>
            <label><span>开始时间</span><input className="input" type="datetime-local" value={from} max={to || undefined} onChange={(event) => setFrom(event.target.value)} /></label>
            <label><span>结束时间</span><input className="input" type="datetime-local" value={to} min={from || undefined} onChange={(event) => setTo(event.target.value)} /></label>
            {hasAdvancedFilter && <Button size="sm" variant="ghost" icon={X} onClick={() => { setSurface(''); setSiteId(''); setFrom(''); setTo(''); }}>清除筛选</Button>}
          </div>
        </details>

        {resource.loading && !resource.data ? <LoadingState label="正在读取调用日志" /> : resource.error && !resource.data ? <ErrorState message={resource.error} onRetry={() => void resource.refresh()} /> : rows.length === 0 ? <EmptyState title="没有匹配的调用" description="调整搜索或状态筛选，或者等待新的下游请求。" /> : <>
          <div className="data-scroller desktop-log-table"><table className="data-table log-table"><thead><tr><th>调用时间 / ID</th><th>状态</th><th>下游 Key / 路由</th><th>模型</th><th>实际上游 / 路由结果</th><th>Reasoning</th><th>Token / 缓存</th><th>首字 / 总耗时</th><th>官方费用</th></tr></thead><tbody>{rows.map((log) => {
            const attempt = finalAttempt(log);
            const outcome = routingOutcome(log);
            return <tr className="log-row-clickable" tabIndex={0} aria-label={`查看调用 ${log.id} 详情`} onClick={() => void openDetail(log)} onKeyDown={(event) => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); void openDetail(log); } }} key={log.id}>
              <td><div className="cell-stack"><strong>{formatDateTime(log.startedAt)}</strong><code>{log.id}</code></div></td>
              <td><div className="cell-stack"><Badge tone={statusTone(log.status)}>{statusLabel(log.status)}</Badge>{log.meteringStatus !== 'metered' && <Badge tone={meteringTone(log.meteringStatus)}>{meteringLabel(log.meteringStatus)}</Badge>}{log.httpStatus && <span className="table-subline">HTTP {log.httpStatus}</span>}</div></td>
              <td><div className="cell-stack"><strong>{log.keyName}</strong><span>{routeLabel(log)}</span></div></td>
              <td><div className="model-transition"><code>{log.publicModel}</code>{log.actualModel && log.actualModel !== log.publicModel && <><ArrowRight size={12} /><code>{log.actualModel}</code></>}</div><span className="table-subline">{surfaceLabel(log.surface)}</span></td>
              <td><div className="cell-stack"><strong>{attempt?.siteName || '未进入上游'}</strong><span>{attempt ? attempt.credentialName : '请求在路由前结束'}</span><Badge tone={outcome.tone}>{outcome.label}</Badge></div></td>
              <td><div className="cell-stack"><strong>{reasoningLabel(log.reasoningEffort)}</strong>{Boolean(log.thinkingBudgetTokens) && <span>{formatTokens(log.thinkingBudgetTokens)} tokens</span>}</div></td>
              <td>{log.meteringStatus === 'metered' ? <div className="token-inline"><span>入 {formatTokens(log.inputTokens)}</span><span>缓存 {formatTokens(log.cacheReadTokens)}</span><span>出 {formatTokens(log.outputTokens)}</span><span>推理 {formatTokens(log.reasoningTokens)}</span></div> : <Badge tone={meteringTone(log.meteringStatus)}>{meteringLabel(log.meteringStatus)}</Badge>}</td>
              <td><div className="latency-pair"><strong>首字 {firstOutputLabel(log.stream, log.firstOutputMs)}</strong><span>总耗时 {formatSeconds(log.durationMs)}</span></div></td>
              <td><strong>{officialCostLabel(log)}</strong>{log.meteringStatus === 'metered' && log.chargedNanoUSD !== log.officialCostNanoUSD && <span className="table-subline">实扣 {formatUSDFromNano(log.chargedNanoUSD)}</span>}</td>
            </tr>;
          })}</tbody></table></div>
          <div className="mobile-log-list">{rows.map((log) => { const attempt = finalAttempt(log); const outcome = routingOutcome(log); return <button type="button" onClick={() => void openDetail(log)} key={log.id}><span><strong>{formatDateTime(log.startedAt)}</strong><Badge tone={statusTone(log.status)}>{statusLabel(log.status)}</Badge>{log.meteringStatus !== 'metered' && <Badge tone={meteringTone(log.meteringStatus)}>{meteringLabel(log.meteringStatus)}</Badge>}</span><code>{log.publicModel}</code><span className="mobile-log-facts"><span><small>实际上游</small><strong>{attempt?.siteName || '未进入上游'}</strong><small>{outcome.label}</small></span><span><small>Reasoning</small><strong>{reasoningLabel(log.reasoningEffort)}</strong></span><span><small>首字 / 总耗时</small><strong>{firstOutputLabel(log.stream, log.firstOutputMs)}</strong><small>{formatSeconds(log.durationMs)}</small></span><span><small>Token / 缓存</small><strong>{formatTokens(log.inputTokens)} / {formatTokens(log.outputTokens)}</strong><small>缓存 {formatTokens(log.cacheReadTokens)}</small></span><span><small>官方费用</small><strong>{officialCostLabel(log)}</strong></span></span></button>; })}</div>
          {loadMoreError && <InlineNotice tone="danger">{loadMoreError}</InlineNotice>}
          {resource.data?.page?.hasMore && <div className="load-more-row"><Button busy={loadingMore} onClick={() => void loadMore()}>加载更多调用</Button></div>}
        </>}
      </Panel>}

      <LogDrawer open={Boolean(selected)} title={selected ? `请求 ${selected.id}` : ''} description={selected ? `${formatDateTime(selected.startedAt)} · ${selected.keyName}` : ''} onClose={closeDetail}>
        {selected && detailLoading ? <LoadingState label="正在读取调用详情" /> : selected && detailError ? <ErrorState message={detailError} onRetry={() => void openDetail(selected)} /> : selected && <div className="log-detail">
          <div className="log-detail-metrics"><div><Timer size={16} /><span>首字延迟</span><strong>{firstOutputLabel(selected.stream, selected.firstOutputMs)}</strong></div><div><Clock3 size={16} /><span>总耗时</span><strong>{formatSeconds(selected.durationMs)}</strong></div><div><BrainCircuit size={16} /><span>Reasoning effort</span><strong>{reasoningLabel(selected.reasoningEffort)}</strong></div><div><Coins size={16} /><span>实际扣款</span><strong>{selected.meteringStatus === 'metered' ? formatUSDFromNano(selected.chargedNanoUSD) : meteringLabel(selected.meteringStatus)}</strong></div></div>
          <div className="log-detail-tabs" role="tablist" aria-label="调用详情"><button type="button" role="tab" aria-selected={detailTab === 'overview'} className={detailTab === 'overview' ? 'is-active' : ''} onClick={() => setDetailTab('overview')}>概览</button><button type="button" role="tab" aria-selected={detailTab === 'usage'} className={detailTab === 'usage' ? 'is-active' : ''} onClick={() => setDetailTab('usage')}>用量与计费</button><button type="button" role="tab" aria-selected={detailTab === 'route'} className={detailTab === 'route' ? 'is-active' : ''} onClick={() => setDetailTab('route')}>路由时间线</button></div>
          {detailTab === 'overview' && <section><h3>请求信息</h3><dl className="detail-grid"><div><dt>请求 ID</dt><dd><CopyableValue label="请求 ID" value={selected.id} code /></dd></div><div><dt>下游 Key</dt><dd>{selected.keyName}</dd></div><div><dt>有效路由</dt><dd>{routeLabel(selected)}</dd></div><div><dt>路由结果</dt><dd><Badge tone={routingOutcome(selected).tone}>{routingOutcome(selected).label}</Badge></dd></div><div><dt>请求模型</dt><dd><CopyableValue label="请求模型" value={selected.publicModel} code /></dd></div><div><dt>实际模型</dt><dd><CopyableValue label="实际模型" value={selected.actualModel || '—'} code /></dd></div><div><dt>请求入口</dt><dd><CopyableValue label="请求入口" value={surfacePath(selected.surface)} code /></dd></div><div><dt>响应类型</dt><dd>{selected.stream ? '流式' : '非流式'}</dd></div><div><dt>Reasoning effort</dt><dd><code>{reasoningLabel(selected.reasoningEffort)}</code></dd></div>{Boolean(selected.thinkingBudgetTokens) && <div><dt>Thinking budget</dt><dd>{formatTokens(selected.thinkingBudgetTokens)} tokens</dd></div>}<div><dt>价格目录</dt><dd><CopyableValue label="价格目录" value={selected.priceCatalogVersion || '—'} /></dd></div><div><dt>计价 SKU</dt><dd><CopyableValue label="计价 SKU" value={selected.priceSKU || '—'} code /></dd></div>{selected.errorCode && <div><dt>请求错误</dt><dd><CopyableValue label="请求错误" value={selected.errorCode} code /></dd></div>}<div><dt>计量状态</dt><dd><Badge tone={meteringTone(selected.meteringStatus)}>{meteringLabel(selected.meteringStatus)}</Badge>{selected.meteringErrorCode && <span className="table-subline">{selected.meteringErrorCode}</span>}</dd></div></dl></section>}
          {detailTab === 'usage' && <><section><h3>费用与扣款</h3><div className="token-detail-grid billing-detail-grid"><div><span>官方费用</span><strong>{officialCostLabel(selected)}</strong><small>官方价格目录</small></div><div><span>扣款倍率</span><strong>{formatMultiplier(billingMultiplier(selected))}</strong><small>官方费用 × 倍率</small></div><div><span>实际扣款</span><strong>{selected.meteringStatus === 'metered' ? formatUSDFromNano(selected.chargedNanoUSD) : meteringLabel(selected.meteringStatus)}</strong><small>{selected.quotaCapped ? '额度封顶' : '后续请求按此扣减'}</small></div></div></section><section><h3>Token 明细</h3>{selected.meteringStatus !== 'metered' && <InlineNotice tone={selected.meteringStatus === 'unavailable' ? 'warning' : 'info'}>{meteringLabel(selected.meteringStatus)}：Token 保持未知，费用不会被误显示为零。</InlineNotice>}<div className="token-detail-grid usage-token-grid"><div><span>输入</span><strong>{formatTokens(selected.inputTokens)}</strong></div><div><span>缓存读取</span><strong>{formatTokens(selected.cacheReadTokens)}</strong></div><div><span>缓存写入</span><strong>{formatTokens(selected.cacheWriteTokens)}</strong></div><div><span>缓存写入 5m</span><strong>{formatTokens(selected.cacheWrite5mTokens)}</strong></div><div><span>缓存写入 1h</span><strong>{formatTokens(selected.cacheWrite1hTokens)}</strong></div><div><span>输出</span><strong>{formatTokens(selected.outputTokens)}</strong></div><div><span>推理</span><strong>{formatTokens(selected.reasoningTokens)}</strong></div></div></section><details className="disclosure"><summary>额度账本 <span className="table-subline">{selected.ledger.length} 条事件</span></summary><div className="disclosure-body">{selected.ledger.length ? <div className="quota-ledger-list">{selected.ledger.map((event) => <div key={event.id}><header><Badge tone={event.eventType === 'settle' ? 'success' : 'info'}>{ledgerEventLabel(event.eventType)}</Badge><span>{formatDateTime(event.createdAt)}</span></header><div><span>预留变化 <strong>{signedUSDDelta(event.reservedDeltaNanoUSD)}</strong></span><span>已用变化 <strong>{signedUSDDelta(event.usedDeltaNanoUSD)}</strong></span></div><small><CopyableValue label="账本计价 SKU" value={`${event.priceSKU || '—'} · ${event.priceCatalogVersion || '—'}`} code /></small></div>)}</div> : <EmptyState title="没有额度账本事件" description="请求可能在额度预留前已被拒绝。" />}</div></details></>}
          {detailTab === 'route' && <section><h3>路由时间线 <span className="table-subline">{routeRevisionLabel(selected.routeRevision)}</span></h3><RouteTimeline log={selected} /></section>}
        </div>}
      </LogDrawer>
    </div>
  );
}
