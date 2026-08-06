import {
  Activity,
  ArrowRight,
  BrainCircuit,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  CircleDollarSign,
  Clock3,
  FileClock,
  FilterX,
  Gauge,
  ListFilter,
  RefreshCw,
  Route as RouteIcon,
  Search,
  Timer,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { Badge, Button, Drawer, EmptyState, ErrorState, IconButton, LoadingState, PageHeader, Surface } from '../components/ui';
import { api } from '../lib/api';
import { formatLatency, formatTokens, formatUsd } from '../lib/format';
import { useAsyncData } from '../lib/hooks';
import type {
  DownstreamKey,
  RequestLogAttemptDetail,
  RequestLogCursor,
  RequestLogDetail,
  RequestLogFilter,
  RequestLogListItem,
  RequestLogPage,
  RequestLogSummary,
  Upstream,
  V2SiteSummary,
} from '../lib/types';

type TriState = 'all' | 'true' | 'false';
type LogStatus = 'all' | 'success' | 'failed' | 'running';

interface LogFilterDraft {
  model: string;
  status: LogStatus;
  sourceRef: string;
  downstreamKeyId: string;
  stream: TriState;
  switched: TriState;
}

interface LogFilterOptions {
  sites: V2SiteSummary[];
  upstreams: Upstream[];
  keys: DownstreamKey[];
}

const emptyFilters: LogFilterDraft = {
  model: '',
  status: 'all',
  sourceRef: '',
  downstreamKeyId: '',
  stream: 'all',
  switched: 'all',
};

function preciseDateTime(value: string | number | null | undefined): string {
  if (value == null || value === '') return '-';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '-';
  const pad = (part: number, length = 2) => String(part).padStart(length, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}.${pad(date.getMilliseconds(), 3)}`;
}

function optionalTokens(value: number | null | undefined): string {
  return value == null ? '-' : formatTokens(value);
}

function officialCost(microUSD: number): string {
  return formatUsd(microUSD / 1_000_000, 6);
}

function percent(value: number | null | undefined): string {
  return value == null ? '-' : `${value.toFixed(value === Math.round(value) ? 0 : 1)}%`;
}

function reasoningLabel(value?: string): string {
  if (!value) return '默认';
  const labels: Record<string, string> = {
    none: '关闭', minimal: '最低', low: '低', medium: '中', high: '高', xhigh: '极高',
  };
  const normalized = value.toLowerCase();
  return labels[normalized] ? `${labels[normalized]} · ${value}` : value;
}

function surfaceLabel(surface: string): string {
  if (surface === 'responses') return 'Responses';
  if (surface === 'chat_completions') return 'Chat Completions';
  return surface || '未知接口';
}

function routingProfileLabel(value: string): string {
  const normalized = value.trim().toLowerCase();
  return normalized === 'default route' || !normalized ? '默认路由' : value;
}

function actualSourceName(log: RequestLogListItem): string {
  return log.actualSiteName?.trim()
    || log.actualUpstreamName?.trim()
    || (log.actualSiteId ? `站点 #${log.actualSiteId}` : '')
    || (log.actualUpstreamId ? `旧上游 #${log.actualUpstreamId}` : '')
    || '未选定';
}

function finishedAt(startedAt: number, durationMs?: number | null): number | null {
  return durationMs == null ? null : startedAt + durationMs;
}

function statusLabel(status: string): string {
  if (status === 'success') return '成功';
  if (status === 'failed') return '失败';
  if (status === 'running') return '进行中';
  return status || '未知';
}

function statusTone(status: string): 'success' | 'danger' | 'warning' | 'neutral' {
  if (status === 'success') return 'success';
  if (status === 'failed') return 'danger';
  if (status === 'running') return 'warning';
  return 'neutral';
}

function triState(value: TriState): boolean | undefined {
  if (value === 'all') return undefined;
  return value === 'true';
}

function priceSnapshot(value: string | null | undefined): string {
  if (!value) return '';
  try {
    return JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    return value;
  }
}

function attemptSourceName(attempt: RequestLogAttemptDetail): string {
  const name = attempt.siteName?.trim() || attempt.upstreamName?.trim();
  if (name) return name;
  if (attempt.siteId) return `站点 #${attempt.siteId}`;
  if (attempt.upstreamId) return `旧上游 #${attempt.upstreamId}`;
  return '未知上游';
}

function attemptResourceName(name: string | undefined, id: number | null | undefined): string | null {
  const normalized = name?.trim();
  if (normalized && id) return `${normalized} · #${id}`;
  if (normalized) return normalized;
  return id ? `#${id}` : null;
}

function LogStatusBadge({ status }: { status: string }) {
  return <Badge tone={statusTone(status)}>{statusLabel(status)}</Badge>;
}

function TokenBreakdown({ input, cacheRead, cacheWrite, cacheWrite1h, output, reasoning }: {
  input?: number | null;
  cacheRead?: number | null;
  cacheWrite?: number | null;
  cacheWrite1h?: number | null;
  output?: number | null;
  reasoning?: number | null;
}) {
  return (
    <div className="log-token-breakdown">
      <span><small>入</small><strong>{optionalTokens(input)}</strong></span>
      <span><small>读</small><strong>{optionalTokens(cacheRead)}</strong></span>
      {cacheWrite != null && <span><small>写</small><strong>{optionalTokens(cacheWrite)}</strong></span>}
      {cacheWrite1h != null && <span><small>写1h</small><strong>{optionalTokens(cacheWrite1h)}</strong></span>}
      <span><small>出</small><strong>{optionalTokens(output)}</strong></span>
      <span><small>推</small><strong>{optionalTokens(reasoning)}</strong></span>
    </div>
  );
}

export function LogsPage() {
  const optionsState = useAsyncData<LogFilterOptions>(async () => {
    const [sites, upstreams, keys] = await Promise.allSettled([api.v2Sites(), api.upstreams(), api.keys()]);
    return {
      sites: sites.status === 'fulfilled' ? sites.value : [],
      upstreams: upstreams.status === 'fulfilled' ? upstreams.value : [],
      keys: keys.status === 'fulfilled' ? keys.value : [],
    };
  }, []);
  const [draft, setDraft] = useState<LogFilterDraft>(emptyFilters);
  const [filters, setFilters] = useState<RequestLogFilter>({});
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [limit, setLimit] = useState(50);
  const [cursorStack, setCursorStack] = useState<RequestLogCursor[]>([]);
  const [reloadVersion, setReloadVersion] = useState(0);
  const [page, setPage] = useState<RequestLogPage | null>(null);
  const [summary, setSummary] = useState<RequestLogSummary | null>(null);
  const [pageLoading, setPageLoading] = useState(true);
  const [summaryLoading, setSummaryLoading] = useState(true);
  const [pageError, setPageError] = useState<string | null>(null);
  const [summaryError, setSummaryError] = useState<string | null>(null);
  const [detailAnchor, setDetailAnchor] = useState<RequestLogListItem | null>(null);
  const [detail, setDetail] = useState<RequestLogDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState<string | null>(null);
  const pageRequest = useRef(0);
  const summaryRequest = useRef(0);
  const detailRequest = useRef(0);
  const cursor = cursorStack[cursorStack.length - 1] ?? null;

  const loadPage = useCallback(async () => {
    const requestID = ++pageRequest.current;
    setPageLoading(true);
    setPageError(null);
    try {
      const result = await api.requestLogs(filters, cursor, limit);
      if (requestID === pageRequest.current) setPage(result);
    } catch (reason) {
      if (requestID === pageRequest.current) setPageError(reason instanceof Error ? reason.message : '日志加载失败');
    } finally {
      if (requestID === pageRequest.current) setPageLoading(false);
    }
  }, [cursor, filters, limit, reloadVersion]);

  const loadSummary = useCallback(async () => {
    const requestID = ++summaryRequest.current;
    setSummaryLoading(true);
    setSummaryError(null);
    try {
      const result = await api.requestLogSummary(filters);
      if (requestID === summaryRequest.current) setSummary(result);
    } catch (reason) {
      if (requestID === summaryRequest.current) setSummaryError(reason instanceof Error ? reason.message : '汇总加载失败');
    } finally {
      if (requestID === summaryRequest.current) setSummaryLoading(false);
    }
  }, [filters, reloadVersion]);

  useEffect(() => { void loadPage(); }, [loadPage]);
  useEffect(() => { void loadSummary(); }, [loadSummary]);

  const appliedFilterCount = useMemo(() => Object.values(filters).filter((value) => value !== undefined && value !== '').length, [filters]);
  const pageNumber = cursorStack.length + 1;
  const pageCount = summary?.count ? Math.max(1, Math.ceil(summary.count / limit)) : 1;
  const rows = page?.items ?? [];

  const applyFilters = (event: FormEvent) => {
    event.preventDefault();
    const next: RequestLogFilter = {};
    if (draft.status !== 'all') next.status = draft.status;
    if (draft.model.trim()) next.model = draft.model.trim();
    const [sourceKind, sourceID] = draft.sourceRef.split(':', 2);
    const parsedSourceID = Number(sourceID);
    if (Number.isInteger(parsedSourceID) && parsedSourceID > 0) {
      if (sourceKind === 'site') next.siteId = parsedSourceID;
      if (sourceKind === 'legacy') next.upstreamId = parsedSourceID;
    }
    if (draft.downstreamKeyId) next.downstreamKeyId = Number(draft.downstreamKeyId);
    const stream = triState(draft.stream);
    const switched = triState(draft.switched);
    if (stream !== undefined) next.stream = stream;
    if (switched !== undefined) next.switched = switched;
    setCursorStack([]);
    setFilters(next);
    setFiltersOpen(false);
  };

  const resetFilters = () => {
    setDraft(emptyFilters);
    setCursorStack([]);
    setFilters({});
    setFiltersOpen(false);
  };

  const refreshLatest = () => {
    setCursorStack([]);
    setReloadVersion((value) => value + 1);
  };

  const changeLimit = (next: number) => {
    setCursorStack([]);
    setLimit(next);
  };

  const fetchDetail = async (id: string) => {
    const requestID = ++detailRequest.current;
    setDetailLoading(true);
    setDetailError(null);
    try {
      const result = await api.log(id);
      if (requestID === detailRequest.current) setDetail(result);
    } catch (reason) {
      if (requestID === detailRequest.current) setDetailError(reason instanceof Error ? reason.message : '详情加载失败');
    } finally {
      if (requestID === detailRequest.current) setDetailLoading(false);
    }
  };

  const openDetail = (item: RequestLogListItem) => {
    setDetailAnchor(item);
    setDetail(null);
    void fetchDetail(item.id);
  };

  const closeDetail = () => {
    detailRequest.current += 1;
    setDetailAnchor(null);
    setDetail(null);
    setDetailError(null);
    setDetailLoading(false);
  };

  if (pageLoading && !page) return <div className="page"><LoadingState label="正在读取请求日志" /></div>;
  if (pageError && !page) return <div className="page"><ErrorState message={pageError} onRetry={refreshLatest} /></div>;

  return (
    <div className="page logs-page">
      <PageHeader
        title="请求日志"
        description="查看每次下游调用的模型、思考参数、Token、官方计费和上游切换过程。"
        actions={<Button icon={RefreshCw} busy={pageLoading || summaryLoading} onClick={refreshLatest}>刷新</Button>}
      />

      <section className="log-summary-strip" aria-label="日志汇总">
        <div className="log-summary-item"><span><Activity size={14} />请求数</span><strong>{summaryLoading && !summary ? '-' : (summary?.count ?? 0).toLocaleString('zh-CN')}</strong><small>当前筛选范围</small></div>
        <div className="log-summary-item"><span><Gauge size={14} />成功率</span><strong>{summaryLoading && !summary ? '-' : percent(summary?.successRate)}</strong><small>成功请求 / 全部请求</small></div>
        <div className="log-summary-item"><span><CircleDollarSign size={14} />官方费用</span><strong>{summaryLoading && !summary ? '-' : officialCost(summary?.costMicroUsd ?? 0)}</strong><small>按官方美元价格累计</small></div>
        <div className="log-summary-item"><span><Timer size={14} />TTFT</span><strong>{formatLatency(summary?.p50TtftMs ?? null)}</strong><small>P95 {formatLatency(summary?.p95TtftMs ?? null)}</small></div>
        <div className="log-summary-item"><span><RouteIcon size={14} />切换率</span><strong>{summaryLoading && !summary ? '-' : percent(summary?.switchRate)}</strong><small>发生过上游切换的请求</small></div>
      </section>
      {summaryError && <div className="log-inline-error"><span>{summaryError}</span><Button size="sm" onClick={() => void loadSummary()}>重试汇总</Button></div>}

      <Surface className={`log-filter-surface${filtersOpen ? ' is-open' : ''}`}>
        <div className="log-filter-toolbar">
          <Button
            type="button"
            variant="ghost"
            icon={ListFilter}
            className="log-filter-toggle"
            aria-expanded={filtersOpen}
            aria-controls="log-filter-fields"
            onClick={() => setFiltersOpen((current) => !current)}
          >
            <span>筛选</span>
            <span className="log-filter-toggle-state">{appliedFilterCount ? `已启用 ${appliedFilterCount} 项` : '全部记录'}</span>
            <ChevronDown className="log-filter-toggle-chevron" size={16} aria-hidden="true" />
          </Button>
        </div>
        <form id="log-filter-fields" className="log-filter-grid" onSubmit={applyFilters}>
          <label className="field log-model-filter"><span className="field-label">模型</span><span className="log-input-icon"><Search size={14} /><input className="input" value={draft.model} onChange={(event) => setDraft({ ...draft, model: event.target.value })} placeholder="精确匹配请求或实际模型" /></span></label>
          <label className="field"><span className="field-label">状态</span><select className="select" value={draft.status} onChange={(event) => setDraft({ ...draft, status: event.target.value as LogStatus })}><option value="all">全部</option><option value="success">成功</option><option value="failed">失败</option><option value="running">进行中</option></select></label>
          <label className="field"><span className="field-label">站点 / 上游</span><select className="select" value={draft.sourceRef} disabled={optionsState.loading} onChange={(event) => setDraft({ ...draft, sourceRef: event.target.value })}><option value="">全部</option>{Boolean(optionsState.data?.sites.length) && <optgroup label="站点">{optionsState.data?.sites.map((item) => <option value={`site:${item.id}`} key={`site:${item.id}`}>{item.name}</option>)}</optgroup>}{Boolean(optionsState.data?.upstreams.length) && <optgroup label="兼容旧上游">{optionsState.data?.upstreams.map((item) => <option value={`legacy:${item.id}`} key={`legacy:${item.id}`}>{item.name}</option>)}</optgroup>}</select></label>
          <label className="field"><span className="field-label">下游 Key</span><select className="select" value={draft.downstreamKeyId} disabled={optionsState.loading} onChange={(event) => setDraft({ ...draft, downstreamKeyId: event.target.value })}><option value="">全部</option>{optionsState.data?.keys.map((item) => <option value={item.id} key={item.id}>{item.name}</option>)}</select></label>
          <label className="field"><span className="field-label">调用模式</span><select className="select" value={draft.stream} onChange={(event) => setDraft({ ...draft, stream: event.target.value as TriState })}><option value="all">全部</option><option value="true">流式</option><option value="false">非流式</option></select></label>
          <label className="field"><span className="field-label">路由切换</span><select className="select" value={draft.switched} onChange={(event) => setDraft({ ...draft, switched: event.target.value as TriState })}><option value="all">全部</option><option value="true">发生切换</option><option value="false">未切换</option></select></label>
          <div className="log-filter-actions">
            <Button type="button" icon={FilterX} disabled={!appliedFilterCount && draft === emptyFilters} onClick={resetFilters}>重置</Button>
            <Button type="submit" variant="primary" icon={ListFilter}>应用筛选{appliedFilterCount ? ` (${appliedFilterCount})` : ''}</Button>
          </div>
        </form>
      </Surface>

      <Surface className="log-list-surface">
        <div className="section-header log-list-header">
          <div><h2>调用记录</h2><p>第 {pageNumber} / {pageCount} 页，共 {summary?.count ?? 0} 条</p></div>
          <div className="log-list-actions"><span>每页</span><select className="select compact-select" value={limit} onChange={(event) => changeLimit(Number(event.target.value))}><option value={25}>25</option><option value={50}>50</option><option value={100}>100</option></select></div>
        </div>
        {pageError && <div className="log-inline-error"><span>{pageError}</span><Button size="sm" onClick={() => void loadPage()}>重试</Button></div>}
        {pageLoading && page && <div className="log-loading-line"><RefreshCw className="spin" size={13} />正在更新当前页</div>}
        {rows.length === 0 ? (
          <EmptyState title="没有匹配的请求" description="调整筛选条件，或等待新的下游调用进入。" />
        ) : (
          <>
            <div className="data-scroller log-desktop-table"><table className="data-table log-table">
              <thead><tr><th>调用时间 / ID</th><th>状态</th><th>下游 Key / 路由方案</th><th>请求模型 → 实际模型</th><th>实际上游</th><th>思考</th><th>接口 / 模式</th><th>Token 明细</th><th>首输出 / 总耗时</th><th>官方费用</th><th>切换</th><th aria-label="查看详情" /></tr></thead>
              <tbody>{rows.map((log) => <tr key={log.id}>
                <td><div className="cell-main log-time-cell"><button type="button" className="table-primary-link" onClick={() => openDetail(log)}>{preciseDateTime(log.startedAt)}</button><code title={log.id}>{log.id}</code></div></td>
                <td><div className="cell-main"><LogStatusBadge status={log.status} /><span>{log.httpStatus != null ? `HTTP ${log.httpStatus}` : '无 HTTP 状态'}</span></div></td>
                <td><div className="cell-main"><strong>{log.keyName}</strong><span>{routingProfileLabel(log.routingProfileName)}</span></div></td>
                <td><div className="model-transition"><code title={log.requestedModel}>{log.requestedModel}</code><ArrowRight size={12} /><code title={log.actualModel || ''}>{log.actualModel || '未返回'}</code></div></td>
                <td><div className="cell-main log-upstream-cell"><strong>{actualSourceName(log)}</strong><span>{log.actualEndpointName || '无 Endpoint'} · {log.actualCredentialName || '无 API Key'}</span></div></td>
                <td><div className="cell-main log-reasoning-cell"><strong>{reasoningLabel(log.reasoningEffort)}</strong><span>{log.thinkingBudget != null ? `${formatTokens(log.thinkingBudget)} 预算` : '无独立预算'}</span></div></td>
                <td><div className="cell-main log-surface-cell"><strong>{surfaceLabel(log.surface)}</strong><Badge tone={log.stream ? 'info' : 'neutral'}>{log.stream ? '流式' : '非流式'}</Badge></div></td>
                <td><TokenBreakdown input={log.inputTokens} cacheRead={log.cacheReadTokens} cacheWrite={log.cacheWriteTokens} cacheWrite1h={log.cacheWrite1hTokens} output={log.outputTokens} reasoning={log.reasoningTokens} /></td>
                <td className="numeric"><div className="log-latency-cell"><strong>{formatLatency(log.firstTokenMs ?? null)}</strong><span>{formatLatency(log.durationMs ?? null)}</span></div></td>
                <td className="numeric log-cost-cell">{officialCost(log.costMicroUsd)}</td>
                <td>{log.switchCount > 0 ? <Badge tone="warning">{log.switchCount} 次</Badge> : <span className="log-direct-route">直连</span>}</td>
                <td><IconButton label={`查看请求 ${log.id}`} onClick={() => openDetail(log)}><ChevronRight size={16} /></IconButton></td>
              </tr>)}</tbody>
            </table></div>
            <div className="log-mobile-list">{rows.map((log) => <button type="button" className="log-mobile-card" onClick={() => openDetail(log)} key={log.id}>
              <span className="log-mobile-heading"><span><strong>{preciseDateTime(log.startedAt)}</strong><code>{log.id}</code></span><LogStatusBadge status={log.status} /></span>
              <span className="log-mobile-models"><code>{log.requestedModel}</code><ArrowRight size={12} /><code>{log.actualModel || '未返回'}</code></span>
              <span className="log-mobile-facts"><span><small>采用站点</small><strong>{actualSourceName(log)}</strong></span><span><small>路由方案</small><strong>{routingProfileLabel(log.routingProfileName)}</strong></span><span><small>首输出</small><strong>{formatLatency(log.firstTokenMs ?? null)}</strong></span><span><small>费用</small><strong>{officialCost(log.costMicroUsd)}</strong></span></span>
              <TokenBreakdown input={log.inputTokens} cacheRead={log.cacheReadTokens} cacheWrite={log.cacheWriteTokens} cacheWrite1h={log.cacheWrite1hTokens} output={log.outputTokens} reasoning={log.reasoningTokens} />
            </button>)}</div>
          </>
        )}
        <footer className="log-pagination"><span>本页 {rows.length} 条 · 第 {pageNumber} 页</span><div><Button size="sm" icon={ChevronLeft} disabled={cursorStack.length === 0 || pageLoading} onClick={() => setCursorStack((current) => current.slice(0, -1))}>上一页</Button><Button size="sm" icon={ChevronRight} disabled={!page?.hasMore || !page.nextCursor || pageLoading} onClick={() => page?.nextCursor && setCursorStack((current) => [...current, page.nextCursor!])}>下一页</Button></div></footer>
      </Surface>

      <Drawer open={Boolean(detailAnchor)} title={detailAnchor?.id || ''} description={detailAnchor ? `${preciseDateTime(detailAnchor.startedAt)} · ${detailAnchor.keyName}` : ''} onClose={closeDetail}>
        {detailLoading && !detail ? <LoadingState label="正在读取完整尝试记录" /> : detailError && !detail ? <ErrorState message={detailError} onRetry={() => detailAnchor && void fetchDetail(detailAnchor.id)} /> : detail && <div className="log-detail">
          <div className="log-detail-status"><LogStatusBadge status={detail.status} /><Badge tone={detail.stream ? 'info' : 'neutral'}>{detail.stream ? '流式' : '非流式'}</Badge><Badge tone="neutral">{surfaceLabel(detail.surface)}</Badge><span>{detail.httpStatus != null ? `HTTP ${detail.httpStatus}` : '无 HTTP 状态'}</span></div>
          <div className="log-detail-grid">
            <div><Clock3 size={16} /><span>总耗时</span><strong>{formatLatency(detail.durationMs ?? null)}</strong></div>
            <div><Timer size={16} /><span>首输出</span><strong>{formatLatency(detail.firstTokenMs ?? null)}</strong></div>
            <div><CircleDollarSign size={16} /><span>官方费用</span><strong>{officialCost(detail.costMicroUsd)}</strong></div>
            <div><RouteIcon size={16} /><span>路由切换</span><strong>{detail.switchCount} 次</strong></div>
          </div>
          <section className="detail-section"><h3><FileClock size={16} />请求信息</h3><dl className="detail-definition"><div><dt>开始时间</dt><dd>{preciseDateTime(detail.startedAt)}</dd></div><div><dt>完成时间</dt><dd>{preciseDateTime(detail.finishedAt)}</dd></div><div><dt>下游 Key</dt><dd>{detail.keyName}{detail.downstreamKeyId ? ` · #${detail.downstreamKeyId}` : ''}</dd></div><div><dt>路由方案</dt><dd>{routingProfileLabel(detail.routingProfileName)}{detail.routingProfileId ? ` · #${detail.routingProfileId}` : ''}</dd></div><div><dt>接口表面</dt><dd>{surfaceLabel(detail.surface)}</dd></div><div><dt>配置版本</dt><dd>{detail.publishedModelRevision ? `模型 #${detail.publishedModelRevision}` : detail.routeRevision ? `旧路由 #${detail.routeRevision}` : '-'}</dd></div></dl></section>
          <section className="detail-section"><h3><RouteIcon size={16} />实际采用上游</h3><dl className="detail-definition"><div><dt>Site</dt><dd>{attemptResourceName(detail.actualSiteName || detail.actualUpstreamName, detail.actualSiteId || detail.actualUpstreamId) || '未选定'}</dd></div><div><dt>Endpoint</dt><dd>{attemptResourceName(detail.actualEndpointName, detail.actualEndpointId) || '-'}</dd></div><div><dt>API Key</dt><dd>{attemptResourceName(detail.actualCredentialName, detail.actualCredentialId) || '-'}</dd></div><div><dt>路由引擎</dt><dd>{detail.routingGeneration === 'v3' ? '站点路由' : '历史路由'}</dd></div></dl></section>
          <section className="detail-section"><h3><BrainCircuit size={16} />模型与思考</h3><dl className="detail-definition"><div><dt>请求模型</dt><dd><code>{detail.requestedModel}</code></dd></div><div><dt>实际模型</dt><dd><code>{detail.actualModel || '未返回'}</code></dd></div><div><dt>思考等级</dt><dd>{reasoningLabel(detail.reasoningEffort)}</dd></div><div><dt>思考预算</dt><dd>{detail.thinkingBudget != null ? `${formatTokens(detail.thinkingBudget)} tokens` : '未设置'}</dd></div></dl></section>
          <section className="detail-section"><h3><Activity size={16} />Token 与计费</h3><div className="token-grid"><div><span>输入</span><strong>{optionalTokens(detail.inputTokens)}</strong></div><div><span>缓存读取</span><strong>{optionalTokens(detail.cacheReadTokens)}</strong></div>{detail.cacheWriteTokens != null && <div><span>缓存写入</span><strong>{optionalTokens(detail.cacheWriteTokens)}</strong></div>}{detail.cacheWrite1hTokens != null && <div><span>缓存写入 1h</span><strong>{optionalTokens(detail.cacheWrite1hTokens)}</strong></div>}<div><span>输出</span><strong>{optionalTokens(detail.outputTokens)}</strong></div><div><span>推理</span><strong>{optionalTokens(detail.reasoningTokens)}</strong></div></div>{detail.priceSnapshot ? <details className="log-price-snapshot"><summary>本次官方价格快照</summary><pre>{priceSnapshot(detail.priceSnapshot)}</pre></details> : <div className="log-price-missing">本次请求没有可用的官方价格快照。</div>}</section>
          {detail.errorMessage && <section className="log-request-error"><strong>请求错误</strong><code>{detail.errorMessage}</code></section>}
          <section className="detail-section"><h3><RouteIcon size={16} />故障切换时间线</h3>{detail.attempts.length ? <div className="attempt-timeline">{detail.attempts.map((attempt) => {
            const endpoint = attemptResourceName(attempt.endpointName, attempt.endpointId);
            const credential = attemptResourceName(attempt.credentialName, attempt.inferenceCredentialId);
            return <div className={`attempt attempt-${attempt.status}`} key={attempt.id}><span className="attempt-node" /><div className="attempt-heading"><strong>#{attempt.attemptIndex + 1} {attemptSourceName(attempt)}</strong><LogStatusBadge status={attempt.status} /></div><code>{attempt.upstreamModel || '未记录模型'}</code>{(endpoint || credential) && <div className="attempt-resources">{endpoint && <span><small>Endpoint</small>{endpoint}</span>}{credential && <span><small>API Key</small>{credential}</span>}</div>}<div className="attempt-started"><span>开始 {preciseDateTime(attempt.createdAt)}</span><span>结束 {preciseDateTime(finishedAt(attempt.createdAt, attempt.latencyMs))}</span></div><div className="attempt-metrics"><span>耗时 {formatLatency(attempt.latencyMs ?? null)}</span><span>首输出 {formatLatency(attempt.firstTokenMs ?? null)}</span><span>{attempt.httpStatus != null ? `HTTP ${attempt.httpStatus}` : '无 HTTP 状态'}</span>{attempt.errorClass && <span>{attempt.errorClass}</span>}</div>{attempt.switchReason && <p>切换原因：{attempt.switchReason}</p>}{attempt.errorMessage && <p className="attempt-error">{attempt.errorMessage}</p>}</div>;
          })}</div> : <div className="log-no-attempts">没有可用的上游尝试记录。</div>}</section>
        </div>}
      </Drawer>
    </div>
  );
}
