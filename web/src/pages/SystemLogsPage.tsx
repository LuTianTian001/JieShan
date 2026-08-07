import {
  AlertTriangle,
  Bug,
  Check,
  ChevronDown,
  Clipboard,
  Info,
  RefreshCw,
  ServerCog,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  FilterBar,
  IconButton,
  LoadingState,
  PageHeader,
  Panel,
  SearchField,
  SegmentedControl,
  UnavailableState,
} from '../components/ui';
import { api, ApiUnavailableError, type SystemLogFilter } from '../lib/api';
import { formatDateTime, formatRelativeTime } from '../lib/format';
import { useDebouncedValue } from '../lib/hooks';
import type { SystemLogEntry } from '../lib/types';

type LevelFilter = 'all' | 'error' | 'warn' | 'info' | 'debug';

const levelItems: Array<{ value: LevelFilter; label: string }> = [
  { value: 'all', label: '全部' },
  { value: 'error', label: '错误' },
  { value: 'warn', label: '警告' },
  { value: 'info', label: '信息' },
  { value: 'debug', label: '调试' },
];

function levelPresentation(level: string): {
  label: string;
  tone: 'danger' | 'warning' | 'info' | 'neutral';
  icon: typeof Info;
} {
  if (level === 'error') return { label: '错误', tone: 'danger', icon: AlertTriangle };
  if (level === 'warn') return { label: '警告', tone: 'warning', icon: AlertTriangle };
  if (level === 'debug') return { label: '调试', tone: 'neutral', icon: Bug };
  return { label: '信息', tone: 'info', icon: Info };
}

function CopyValue({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1_500);
    } catch {
      // The value remains visible and selectable when clipboard permission is unavailable.
    }
  };
  return <span className="system-log-copy"><code>{value}</code><IconButton size="sm" label={`复制${label}`} onClick={(event) => { event.preventDefault(); event.stopPropagation(); void copy(); }}>{copied ? <Check size={14} /> : <Clipboard size={14} />}</IconButton></span>;
}

export function SystemLogsPage() {
  const [items, setItems] = useState<SystemLogEntry[]>([]);
  const [level, setLevel] = useState<LevelFilter>('all');
  const [search, setSearch] = useState('');
  const [module, setModule] = useState('');
  const [requestId, setRequestId] = useState('');
  const [taskId, setTaskId] = useState('');
  const [hasMore, setHasMore] = useState(false);
  const [nextBefore, setNextBefore] = useState(0);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [unavailable, setUnavailable] = useState(false);
  const [error, setError] = useState('');
  const requestSequence = useRef(0);
  const debouncedSearch = useDebouncedValue(search);
  const debouncedModule = useDebouncedValue(module);
  const debouncedRequestId = useDebouncedValue(requestId);
  const debouncedTaskId = useDebouncedValue(taskId);

  const filter = useMemo<SystemLogFilter>(() => ({
    limit: 50,
    level: level === 'all' ? undefined : level,
    search: debouncedSearch,
    module: debouncedModule,
    requestId: debouncedRequestId,
    taskId: debouncedTaskId,
  }), [debouncedModule, debouncedRequestId, debouncedSearch, debouncedTaskId, level]);

  const load = useCallback(async (append = false) => {
    const sequence = ++requestSequence.current;
    if (append) setLoadingMore(true);
    else setLoading(true);
    setError('');
    setUnavailable(false);
    try {
      const page = await api.systemLogs({ ...filter, before: append ? nextBefore : undefined });
      if (sequence !== requestSequence.current) return;
      setItems((current) => append ? [...current, ...page.items] : page.items);
      setHasMore(page.hasMore);
      setNextBefore(page.nextBefore || 0);
    } catch (reason) {
      if (sequence !== requestSequence.current) return;
      if (reason instanceof ApiUnavailableError) setUnavailable(true);
      else setError(reason instanceof Error ? reason.message : '系统日志加载失败');
      if (!append) setItems([]);
    } finally {
      if (sequence === requestSequence.current) {
        setLoading(false);
        setLoadingMore(false);
      }
    }
  }, [filter, nextBefore]);

  useEffect(() => {
    void load(false);
    return () => { requestSequence.current += 1; };
  }, [filter]);

  const counts = useMemo(() => items.reduce((result, item) => {
    result[item.level] = (result[item.level] || 0) + 1;
    return result;
  }, {} as Record<string, number>), [items]);
  const activeAdvancedFilters = [module, requestId, taskId].filter((value) => value.trim()).length;

  return (
    <div className="page system-logs-page">
      <PageHeader
        title="系统日志"
        description="查看网关、探针、后台任务和配置加载事件，定位调用日志之外的系统问题。"
        actions={<Button icon={RefreshCw} busy={loading && items.length > 0} onClick={() => void load(false)}>刷新日志</Button>}
      />

      <Panel className="system-log-panel">
        <FilterBar trailing={<span className="system-log-loaded">已加载 {items.length} 条</span>}>
          <SearchField value={search} onChange={setSearch} placeholder="搜索消息、错误码或关联字段" />
          <SegmentedControl value={level} items={levelItems.map((item) => ({ ...item, count: item.value === 'all' ? items.length : counts[item.value] || 0 }))} onChange={setLevel} label="日志级别" />
        </FilterBar>

        <details className="system-log-advanced">
          <summary><span>精确筛选{activeAdvancedFilters > 0 ? ` · ${activeAdvancedFilters} 项` : ''}</span><ChevronDown size={16} /></summary>
          <div className="system-log-filter-grid">
            <label><span>模块</span><input className="input" value={module} onChange={(event) => setModule(event.target.value)} placeholder="例如 gateway 或 monitor" /></label>
            <label><span>请求 ID</span><input className="input" value={requestId} onChange={(event) => setRequestId(event.target.value)} placeholder="完整请求 ID" /></label>
            <label><span>任务 ID</span><input className="input" value={taskId} onChange={(event) => setTaskId(event.target.value)} placeholder="完整后台任务 ID" /></label>
            <Button size="sm" disabled={!activeAdvancedFilters} onClick={() => { setModule(''); setRequestId(''); setTaskId(''); }}>清除筛选</Button>
          </div>
        </details>

        {loading && !items.length ? <LoadingState label="正在读取系统日志" />
          : unavailable ? <UnavailableState title="系统日志暂不可用" description="当前后端实例尚未启用系统日志接口。" />
            : error && !items.length ? <ErrorState message={error} onRetry={() => void load(false)} />
              : !items.length ? <EmptyState title="没有匹配的系统日志" description="调整级别、搜索词或精确筛选条件。" />
                : <div className="system-log-list">
                  {error && <div className="system-log-inline-error"><AlertTriangle size={15} /><span>{error}</span></div>}
                  {items.map((item) => {
                    const presentation = levelPresentation(item.level);
                    const LevelIcon = presentation.icon;
                    const fields = Object.entries(item.fields || {});
                    return <details className={`system-log-entry level-${item.level}`} key={item.id}>
                      <summary>
                        <span className="system-log-level-icon"><LevelIcon size={16} /></span>
                        <span className="system-log-time"><strong>{formatDateTime(item.timestamp, true)}</strong><small>{formatRelativeTime(item.timestamp)}</small></span>
                        <span className="system-log-source"><strong>{item.module || 'system'}</strong><code>{item.code || 'event'}</code></span>
                        <span className="system-log-message">{item.message}</span>
                        <Badge tone={presentation.tone}>{presentation.label}</Badge>
                        <ChevronDown className="system-log-chevron" size={16} />
                      </summary>
                      <div className="system-log-detail">
                        <div className="system-log-identifiers">
                          <div><span>日志 ID</span><CopyValue value={item.id} label="日志 ID" /></div>
                          {item.requestId && <div><span>请求 ID</span><CopyValue value={item.requestId} label="请求 ID" /></div>}
                          {item.taskId && <div><span>任务 ID</span><CopyValue value={item.taskId} label="任务 ID" /></div>}
                        </div>
                        <div className="system-log-fields">
                          <span>关联字段</span>
                          {fields.length ? <dl>{fields.map(([key, value]) => <div key={key}><dt>{key}</dt><dd>{typeof value === 'string' ? value : JSON.stringify(value)}</dd></div>)}</dl> : <small>这个事件没有附加字段。</small>}
                        </div>
                      </div>
                    </details>;
                  })}
                  {hasMore && <div className="system-log-load-more"><Button busy={loadingMore} onClick={() => void load(true)}>加载更早日志</Button></div>}
                </div>}
      </Panel>
    </div>
  );
}
