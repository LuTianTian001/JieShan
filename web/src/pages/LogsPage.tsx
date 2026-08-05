import { ArrowRight, BrainCircuit, Clock3, Coins, FileClock, ListFilter, RefreshCw, Route as RouteIcon, Timer } from 'lucide-react';
import { useMemo, useState } from 'react';
import { Badge, Button, Drawer, EmptyState, ErrorState, LoadingState, Metric, PageHeader, Surface } from '../components/ui';
import { useToast } from '../components/Toast';
import { api } from '../lib/api';
import { formatDateTime, formatLatency, formatTokens, formatUsd } from '../lib/format';
import { useAsyncData } from '../lib/hooks';
import type { RequestLog } from '../lib/types';

export function LogsPage() {
  const toast = useToast();
  const state = useAsyncData(() => api.logs(), []);
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState<'all' | 'success' | 'failed'>('all');
  const [selected, setSelected] = useState<RequestLog | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const logs = state.data ?? [];

  const filtered = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return logs.filter((log) => (status === 'all' || log.status === status) && (!normalized || `${log.id} ${log.keyName} ${log.requestedModel} ${log.actualModel}`.toLowerCase().includes(normalized)));
  }, [logs, query, status]);
  const successCount = logs.filter((log) => log.status === 'success').length;
  const totalCost = logs.reduce((sum, log) => sum + log.costUsd, 0);
  const averageTtft = logs.filter((log) => log.ttftMs != null).reduce((sum, log, _, array) => sum + (log.ttftMs ?? 0) / array.length, 0);
  const switched = logs.filter((log) => log.switchCount > 0).length;

  const openDetail = async (log: RequestLog) => {
    setSelected(log);
    setDetailLoading(true);
    try {
      setSelected(await api.log(log.id));
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '详情加载失败', 'error');
    } finally {
      setDetailLoading(false);
    }
  };

  if (state.loading && !state.data) return <div className="page"><LoadingState label="正在读取请求日志" /></div>;
  if (state.error && !state.data) return <div className="page"><ErrorState message={state.error} onRetry={() => void state.refresh()} /></div>;

  return (
    <div className="page">
      <PageHeader title="请求日志" description="记录计费、模型思考参数与每一次上游尝试。默认不保存提示词正文。" actions={<Button icon={RefreshCw} busy={state.loading} onClick={() => void state.refresh()}>刷新日志</Button>} />
      <div className="metric-grid log-metrics">
        <Metric label="成功率" value={logs.length ? `${(successCount / logs.length * 100).toFixed(1)}%` : '-'} hint={`${successCount}/${logs.length} 次`} tone="success" />
        <Metric label="官方成本" value={formatUsd(totalCost)} hint="当前列表" />
        <Metric label="平均 TTFT" value={averageTtft ? formatLatency(averageTtft) : '-'} hint="首个语义输出" />
        <Metric label="发生切换" value={switched} hint="含失败后重试" tone={switched ? 'warning' : 'neutral'} />
      </div>
      <Surface>
        <div className="toolbar">
          <div className="search-box"><ListFilter size={15} /><input className="input" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="请求 ID、密钥或模型" /></div>
          <select className="select compact-select" value={status} onChange={(event) => setStatus(event.target.value as typeof status)}><option value="all">全部状态</option><option value="success">成功</option><option value="failed">失败</option></select>
        </div>
        {filtered.length === 0 ? <EmptyState title="没有匹配日志" description="调整筛选条件，或等待新的下游请求。" /> : <div className="data-scroller"><table className="data-table log-table">
          <thead><tr><th>时间 / 请求</th><th>密钥</th><th>请求模型 → 实际模型</th><th>状态</th><th>思考等级</th><th>TTFT / 总耗时</th><th>Token</th><th>成本</th><th>切换</th></tr></thead>
          <tbody>{filtered.map((log) => <tr key={log.id}>
            <td><div className="cell-main"><button type="button" className="table-primary-link" onClick={() => void openDetail(log)}>{formatDateTime(log.startedAt)}</button><code>{log.id}</code></div></td>
            <td>{log.keyName}</td>
            <td><div className="model-transition"><code>{log.requestedModel}</code>{log.requestedModel !== log.actualModel && <><ArrowRight size={12} /><code>{log.actualModel}</code></>}</div></td>
            <td><Badge tone={log.status === 'success' ? 'success' : 'danger'}>{log.status === 'success' ? '成功' : '失败'}</Badge></td>
            <td><Badge tone={log.reasoningEffort ? 'info' : 'neutral'}>{log.reasoningEffort || '默认'}</Badge></td>
            <td className="numeric">{formatLatency(log.ttftMs)} / {formatLatency(log.durationMs)}</td>
            <td className="numeric">{formatTokens(log.inputTokens + log.outputTokens + log.reasoningTokens)}</td>
            <td className="numeric">{formatUsd(log.costUsd)}</td>
            <td>{log.switchCount ? <Badge tone="warning">{log.switchCount} 次</Badge> : '-'}</td>
          </tr>)}</tbody>
        </table></div>}
      </Surface>

      <Drawer open={Boolean(selected)} title={selected?.id || ''} description={selected ? `${formatDateTime(selected.startedAt)} · ${selected.keyName}` : ''} onClose={() => setSelected(null)}>
        {selected && <div className="log-detail">
          <div className="log-detail-grid">
            <div><Clock3 size={16} /><span>总耗时</span><strong>{formatLatency(selected.durationMs)}</strong></div>
            <div><Timer size={16} /><span>TTFT</span><strong>{formatLatency(selected.ttftMs)}</strong></div>
            <div><Coins size={16} /><span>官方成本</span><strong>{formatUsd(selected.costUsd)}</strong></div>
            <div><RouteIcon size={16} /><span>路由切换</span><strong>{selected.switchCount} 次</strong></div>
          </div>
          <section className="detail-section"><h3><BrainCircuit size={16} />模型与思考</h3><dl className="detail-definition"><div><dt>请求模型</dt><dd><code>{selected.requestedModel}</code></dd></div><div><dt>实际模型</dt><dd><code>{selected.actualModel}</code></dd></div><div><dt>Reasoning effort</dt><dd>{selected.reasoningEffort || '默认'}</dd></div><div><dt>Thinking budget</dt><dd>{selected.thinkingBudget ? `${selected.thinkingBudget} tokens` : '未设置'}</dd></div></dl></section>
          <section className="detail-section"><h3><FileClock size={16} />Token 与计费</h3><div className="token-grid"><div><span>输入</span><strong>{formatTokens(selected.inputTokens)}</strong></div><div><span>缓存</span><strong>{formatTokens(selected.cacheTokens)}</strong></div><div><span>输出</span><strong>{formatTokens(selected.outputTokens)}</strong></div><div><span>推理</span><strong>{formatTokens(selected.reasoningTokens)}</strong></div></div></section>
          <section className="detail-section"><h3><RouteIcon size={16} />尝试时间线</h3>{detailLoading ? <LoadingState label="正在读取尝试详情" /> : <div className="attempt-timeline">{selected.attempts?.map((attempt) => <div className={`attempt attempt-${attempt.state}`} key={attempt.id}><span className="attempt-node" /><div className="attempt-heading"><strong>#{attempt.sequence} {attempt.upstreamName}</strong><Badge tone={attempt.state === 'success' ? 'success' : attempt.state === 'failed' ? 'danger' : 'neutral'}>{attempt.state === 'success' ? '成功' : attempt.state === 'failed' ? '失败' : '取消'}</Badge></div><code>{attempt.model}</code><div className="attempt-metrics"><span>{formatLatency(attempt.durationMs)}</span><span>TTFT {formatLatency(attempt.ttftMs)}</span><span>{attempt.statusCode || '无 HTTP 状态'}</span></div>{attempt.switchReason && <p>{attempt.switchReason}</p>}{attempt.error && <p className="attempt-error">{attempt.error}</p>}</div>)}</div>}</section>
        </div>}
      </Drawer>
    </div>
  );
}
