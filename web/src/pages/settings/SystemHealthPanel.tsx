import { Activity, Clock3, History, RefreshCw, TriangleAlert } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import {
  Badge,
  Button,
  Disclosure,
  EmptyState,
  ErrorState,
  InlineNotice,
  LoadingState,
  Panel,
  SearchField,
  SectionHeader,
  UnavailableState,
} from '../../components/ui';
import { api, ApiUnavailableError } from '../../lib/api';
import { formatDateTime, formatRelativeTime } from '../../lib/format';
import { useResource } from '../../lib/hooks';
import type { BackgroundTaskHealth, SystemHealthOverview } from '../../lib/types';

async function loadSystemHealth(): Promise<SystemHealthOverview | null> {
  try {
    return await api.systemHealth();
  } catch (error) {
    if (error instanceof ApiUnavailableError) return null;
    throw error;
  }
}

function taskPresentation(state: BackgroundTaskHealth['state']): { label: string; tone: 'success' | 'info' | 'warning' | 'danger' | 'neutral' } {
  if (state === 'healthy') return { label: '正常', tone: 'success' };
  if (state === 'running') return { label: '运行中', tone: 'info' };
  if (state === 'delayed') return { label: '已延迟', tone: 'warning' };
  if (state === 'failed') return { label: '失败', tone: 'danger' };
  return { label: '已停用', tone: 'neutral' };
}

function durationLabel(value: number | null): string {
  if (value === null) return '-';
  if (value < 1_000) return `${value} ms`;
  return `${(value / 1_000).toFixed(value >= 10_000 ? 0 : 1)} 秒`;
}

function taskActivity(task: BackgroundTaskHealth): string {
  if (task.state === 'failed' && task.nextRunAt) return `将在 ${formatRelativeTime(task.nextRunAt)}重试`;
  if (task.lastFinishedAt) return `最近结束于 ${formatRelativeTime(task.lastFinishedAt)}`;
  if (task.lastStartedAt) return `启动于 ${formatRelativeTime(task.lastStartedAt)}`;
  return '等待运行时启动';
}

export function SystemHealthPanel() {
  const resource = useResource(loadSystemHealth, []);
  const [taskQuery, setTaskQuery] = useState('');
  const [historyQuery, setHistoryQuery] = useState('');
  const data = resource.data;

  useEffect(() => {
    const timer = window.setInterval(() => void resource.refresh(), 15_000);
    return () => window.clearInterval(timer);
  }, [resource.refresh]);

  const tasks = useMemo(() => {
    const normalized = taskQuery.trim().toLowerCase();
    return (data?.backgroundTasks || []).filter((item) => !normalized || `${item.label} ${item.id} ${item.state} ${item.lastErrorCode}`.toLowerCase().includes(normalized));
  }, [data?.backgroundTasks, taskQuery]);

  const history = useMemo(() => {
    const normalized = historyQuery.trim().toLowerCase();
    return (data?.configHistory || []).filter((item) => !normalized || `${item.revision} ${item.actor} ${item.summary} ${item.changedFields.join(' ')}`.toLowerCase().includes(normalized));
  }, [data?.configHistory, historyQuery]);

  return (
    <Panel className="system-health-panel">
      <SectionHeader
        title="运行状态"
        description="当前进程快照、计量降级、后台任务和配置应用历史。"
        actions={<Button size="sm" icon={RefreshCw} busy={resource.loading} onClick={() => void resource.refresh()}>刷新状态</Button>}
      />
      {resource.loading && !data ? <LoadingState label="正在读取运行状态" />
        : resource.error && !data ? <ErrorState message={resource.error} onRetry={() => void resource.refresh()} />
          : !data ? <UnavailableState title="运行状态暂不可用" description="当前实例尚未提供运行快照与后台任务健康信息。" />
            : <div className="system-health-content">
              <div className="runtime-snapshot-grid">
                <div><span>运行时长</span><strong>{formatRelativeTime(data.runtime.processStartedAt)}</strong><small>进程启动于 {formatDateTime(data.runtime.processStartedAt, true)}</small></div>
                <div><span>配置版本</span><strong>r{data.runtime.configRevision}</strong><small>载入于 {formatDateTime(data.runtime.configLoadedAt, true)}</small></div>
                <div><span>上游容量</span><strong>{data.runtime.inflightRequests} / {data.runtime.maxConcurrency || '-'}</strong><small>全部站点合计 · {data.runtime.queuedRequests} 个请求排队</small></div>
                <div><span>计量模式</span><strong>{data.runtime.meteringMode === 'normal' ? '正常' : '降级'}</strong><small>价格目录 {data.runtime.activePriceCatalogVersion || '未激活'}</small></div>
              </div>

              {data.meteringWarnings.length ? <div className="metering-warning-list">{data.meteringWarnings.map((warning) => <InlineNotice tone={warning.severity === 'critical' ? 'danger' : 'warning'} key={warning.code}><TriangleAlert size={16} /><div><strong>{warning.title}</strong><span>{warning.message}</span><small>{warning.affectedRequests} 个请求受影响 · 最近出现于 {formatDateTime(warning.lastSeenAt, true)}</small></div></InlineNotice>)}</div> : <InlineNotice tone="success"><Activity size={16} />计量字段完整，当前没有降级警告。</InlineNotice>}

              <Disclosure summary={<span className="settings-disclosure-title"><Clock3 size={17} /><span><strong>后台任务健康</strong><small>{data.backgroundTasks.length} 个任务 · {data.backgroundTasks.filter((item) => item.state === 'failed' || item.state === 'delayed').length} 个需要关注</small></span></span>} open>
                <div className="system-list-toolbar"><SearchField value={taskQuery} onChange={setTaskQuery} placeholder="搜索任务或错误码" /><span>{tasks.length} / {data.backgroundTasks.length}</span></div>
                {!tasks.length ? <EmptyState title="没有匹配的后台任务" description="调整搜索词查看任务状态。" /> : <div className="background-task-list">{tasks.map((task) => {
                  const presentation = taskPresentation(task.state);
                  return <div key={task.id}>
                    <span className="settings-policy-icon"><Activity size={16} /></span>
                    <div><strong>{task.label}</strong><span>{task.schedule} · {taskActivity(task)}</span>{task.lastErrorCode && <code>{task.lastErrorCode}</code>}</div>
                    <div><small>耗时</small><strong>{durationLabel(task.lastDurationMs)}</strong></div>
                    <Badge tone={presentation.tone}>{presentation.label}</Badge>
                  </div>;
                })}</div>}
              </Disclosure>

              <Disclosure summary={<span className="settings-disclosure-title"><History size={17} /><span><strong>配置历史</strong><small>{data.configHistory.length} 个版本 · 当前 r{data.runtime.configRevision}</small></span></span>}>
                <div className="system-list-toolbar"><SearchField value={historyQuery} onChange={setHistoryQuery} placeholder="搜索版本、操作者或字段" /><span>{history.length} / {data.configHistory.length}</span></div>
                {!history.length ? <EmptyState title="没有匹配的配置记录" description="调整搜索词查看历史版本。" /> : <div className="config-history-list">{history.map((item) => <div key={item.id}>
                  <span className="config-revision">r{item.revision}</span>
                  <div><strong>{item.summary}</strong><span>{item.actor} · {item.changedFields.join('、')}</span></div>
                  <time dateTime={new Date(item.createdAt).toISOString()}>{formatDateTime(item.createdAt, true)}</time>
                  <Badge tone={item.status === 'applied' ? 'success' : item.status === 'failed' ? 'danger' : 'neutral'}>{item.status === 'applied' ? '当前生效' : item.status === 'failed' ? '应用失败' : '已替代'}</Badge>
                </div>)}</div>}
              </Disclosure>
            </div>}
    </Panel>
  );
}
