import { Activity, Check, ChevronDown, HeartPulse, History, ListChecks, RefreshCw, Settings2, TimerReset } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { useToast } from '../components/Toast';
import {
  Badge,
  Button,
  Dialog,
  EmptyState,
  ErrorState,
  Field,
  FilterBar,
  HealthBadge,
  IconButton,
  InlineNotice,
  LoadingState,
  PageHeader,
  Panel,
  SearchField,
  UnavailableState,
} from '../components/ui';
import { api, ApiUnavailableError } from '../lib/api';
import { formatDateTime, formatRelativeTime, protocolLabel } from '../lib/format';
import { useResource } from '../lib/hooks';
import {
  DEFAULT_PROBE_PROTOTYPE_PREFERENCES,
  loadProbePrototypePreferences,
  saveProbePrototypePreferences,
} from '../lib/probePreferences';
import type { GatewaySettings, ModelRoute, MonitorHealth, MonitorModel, MonitorSetting, MonitorSnapshot, MonitorTarget, MonitorTargetHistory } from '../lib/types';

interface MonitorData {
  snapshot: MonitorSnapshot | null;
  publishedModels: ModelRoute[];
  settings: GatewaySettings;
}

interface ProbePreferences {
  okProbeIntervalMs: number;
  failureThreshold: number;
  cooldownMs: number;
  slowFirstOutputThresholdMs: number;
}

interface BulkProbeState {
  running: boolean;
  completed: number;
  total: number;
}

type MonitorStatusFilter = 'all' | 'ready' | 'attention' | 'unavailable' | 'cooling' | 'unprobed';
type BadgeTone = 'neutral' | 'accent' | 'success' | 'warning' | 'danger' | 'info';

const DEFAULT_PROBE_PREFERENCES: ProbePreferences = {
  okProbeIntervalMs: 5 * 60_000,
  failureThreshold: 2,
  cooldownMs: 5 * 60_000,
  slowFirstOutputThresholdMs: DEFAULT_PROBE_PROTOTYPE_PREFERENCES.slowFirstOutputThresholdMs,
};

async function loadMonitor(): Promise<MonitorData> {
  const [profiles, settings] = await Promise.all([api.listRoutingProfiles(), api.settings()]);
  const defaultProfile = profiles.find((profile) => profile.isDefault);
  const publishedModels = defaultProfile ? await api.listProfileRoutes(defaultProfile.id) : [];
  try {
    return { snapshot: await api.monitorSnapshot(), publishedModels, settings };
  } catch (error) {
    if (error instanceof ApiUnavailableError) {
      return { snapshot: null, publishedModels, settings };
    }
    throw error;
  }
}

function preferencesFromSettings(settings: GatewaySettings): ProbePreferences {
  const prototypePreferences = loadProbePrototypePreferences();
  return {
    okProbeIntervalMs: settings.probeIntervalMs || DEFAULT_PROBE_PREFERENCES.okProbeIntervalMs,
    failureThreshold: settings.failureThreshold || DEFAULT_PROBE_PREFERENCES.failureThreshold,
    cooldownMs: settings.cooldownMs || DEFAULT_PROBE_PREFERENCES.cooldownMs,
    slowFirstOutputThresholdMs: prototypePreferences.slowFirstOutputThresholdMs,
  };
}

function basisPoints(value: number): string {
  const rate = value / 100;
  return `${rate.toFixed(value % 100 ? 1 : 0)}%`;
}

function formatProbeSeconds(value: number | null | undefined): string {
  if (value === null || value === undefined) return '—';
  return `${(value / 1_000).toFixed(2)} 秒`;
}

function isSlowFirstOutput(
  point: MonitorModel['targets'][number]['statusBar'][number] | null | undefined,
  thresholdMs: number,
): boolean {
  return point?.outcome === 'success'
    && point.firstOutputMs !== null
    && point.firstOutputMs > thresholdMs;
}

function pointTitle(
  point: MonitorModel['targets'][number]['statusBar'][number],
  slowFirstOutputThresholdMs: number,
): string {
  const status = point.outcome === 'success' ? '成功' : point.outcome === 'failure' ? '失败' : '跳过';
  const slowFirstOutput = isSlowFirstOutput(point, slowFirstOutputThresholdMs) ? ' · 首字超阈值，触发冷却' : '';
  return `${formatRelativeTime(point.finishedAt)} · ${status} · 首字 ${formatProbeSeconds(point.firstOutputMs)} · 总耗时 ${formatProbeSeconds(point.totalLatencyMs)}${slowFirstOutput}`;
}

function probeIntervalLabel(value: number): string {
  if (value >= 3_600_000 && value % 3_600_000 === 0) return `${value / 3_600_000} 小时`;
  if (value >= 60_000 && value % 60_000 === 0) return `${value / 60_000} 分钟`;
  return `${Math.max(1, Math.round(value / 1_000))} 秒`;
}

function probeOutcomeLabel(value: string): string {
  return { success: '成功', failure: '失败', skipped: '跳过' }[value] || value;
}

function probePermitReasonLabel(value: string): string {
  const labels: Record<string, string> = {
    granted: '',
    scheduled_probe: '',
    manual_probe: '',
    cooling: '上游仍在冷却',
    disabled: '上游已停用',
    no_credentials: '没有可用 API Key',
    busy: '已有探针正在执行',
  };
  return labels[value] ?? value;
}

function monitorCredentialName(target: MonitorTarget): string {
  return ((target as MonitorTarget & { credentialName?: string }).credentialName || '').trim();
}

function targetConnection(target: MonitorTarget): { label: string; tone: 'success' | 'warning' | 'danger' | 'neutral' } {
  if (!target.usableCredentialCount || target.status === 'no_credentials') return { label: '未连接', tone: 'danger' };
  if (target.latest?.httpStatus === 401 || target.latest?.httpStatus === 403) return { label: '鉴权失败', tone: 'danger' };
  const failure = `${target.latest?.failureKind || ''} ${target.latest?.errorCode || ''}`.toLowerCase();
  if (target.latest?.outcome === 'failure' && /(connect|network|dns|tls|timeout)/.test(failure)) return { label: '连接异常', tone: 'warning' };
  if (!target.latest) return { label: '待检测', tone: 'neutral' };
  return { label: '已连接', tone: 'success' };
}

function effectiveTargetStatus(target: MonitorTarget, slowFirstOutputThresholdMs: number): MonitorHealth {
  if (isSlowFirstOutput(target.latest, slowFirstOutputThresholdMs)) return 'cooling';
  return target.status;
}

function targetProbeState(target: MonitorTarget, slowFirstOutputThresholdMs: number): MonitorHealth {
  if (isSlowFirstOutput(target.latest, slowFirstOutputThresholdMs)) return 'cooling';
  if (!target.latest || target.latest.outcome === 'skipped') return 'unprobed';
  return target.latest.outcome === 'success' ? 'healthy' : 'unavailable';
}

function routingLabel(target: MonitorTarget, slowFirstOutputThresholdMs: number): string {
  if (isSlowFirstOutput(target.latest, slowFirstOutputThresholdMs)) return '慢首字冷却';
  const labels: Partial<Record<MonitorHealth, string>> = {
    healthy: '可路由',
    degraded: '部分可用',
    suspect: '观察中',
    recovering: '恢复测试',
    cooling: '冷却中',
    unavailable: '不可路由',
    no_credentials: '无可用 Key',
    unsupported: '协议不支持',
    disabled: '已停用',
    model_disabled: '模型已暂停',
    unprobed: '待探测',
  };
  return labels[target.status] || target.status;
}

function probePresentation(target: MonitorTarget, slowFirstOutputThresholdMs: number): { label: string; tone: BadgeTone } {
  if (!target.latest) return { label: '未探测', tone: 'neutral' };
  if (target.latest.outcome === 'skipped') return { label: '本次跳过', tone: 'neutral' };
  if (target.latest.outcome === 'failure') return { label: '探针失败', tone: 'danger' };
  if (isSlowFirstOutput(target.latest, slowFirstOutputThresholdMs)) return { label: '通过但过慢', tone: 'warning' };
  return { label: '探针通过', tone: 'success' };
}

function routingPresentation(target: MonitorTarget, slowFirstOutputThresholdMs: number): { label: string; tone: BadgeTone } {
  const label = routingLabel(target, slowFirstOutputThresholdMs);
  if (isRouteReady(target, slowFirstOutputThresholdMs)) {
    return { label, tone: target.status === 'healthy' ? 'success' : 'warning' };
  }
  if (isSlowFirstOutput(target.latest, slowFirstOutputThresholdMs) || target.status === 'cooling') return { label, tone: 'warning' };
  if (['unprobed', 'skipped'].includes(target.status)) return { label, tone: 'neutral' };
  return { label, tone: 'danger' };
}

function isRouteReady(target: MonitorTarget, slowFirstOutputThresholdMs: number): boolean {
  return ['healthy', 'degraded', 'suspect', 'recovering'].includes(
    effectiveTargetStatus(target, slowFirstOutputThresholdMs),
  );
}

function modelMatchesFilter(model: MonitorModel, filter: MonitorStatusFilter, slowFirstOutputThresholdMs: number): boolean {
  if (filter === 'all') return true;
  const targetCount = model.targets.length;
  const routeReadyCount = model.targets.filter((target) => isRouteReady(target, slowFirstOutputThresholdMs)).length;
  if (filter === 'ready') return targetCount > 0 && routeReadyCount === targetCount;
  if (filter === 'attention') return routeReadyCount > 0 && routeReadyCount < targetCount;
  if (filter === 'unavailable') return targetCount > 0 && routeReadyCount === 0;
  if (filter === 'cooling') return model.targets.some((target) => effectiveTargetStatus(target, slowFirstOutputThresholdMs) === 'cooling');
  return model.targets.some((target) => !target.latest || ['unprobed', 'skipped'].includes(target.status));
}

async function keepPrototypeProgressVisible(): Promise<void> {
  if (document.documentElement.dataset.prototype !== 'true') return;
  await new Promise((resolve) => window.setTimeout(resolve, 220));
}

export function MonitorPage() {
  const toast = useToast();
  const resource = useResource(loadMonitor, []);
  const snapshot = resource.data?.snapshot;
  const publishedModels = resource.data?.publishedModels || [];
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState<MonitorStatusFilter>('all');
  const [selectionOpen, setSelectionOpen] = useState(false);
  const [selection, setSelection] = useState<Set<number>>(new Set());
  const [selectionQuery, setSelectionQuery] = useState('');
  const [savingSelection, setSavingSelection] = useState(false);
  const [probingModel, setProbingModel] = useState<number | null>(null);
  const [expandedModelIds, setExpandedModelIds] = useState<Set<number>>(() => new Set());
  const [bulkProbe, setBulkProbe] = useState<BulkProbeState>({ running: false, completed: 0, total: 0 });
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [probePreferences, setProbePreferences] = useState<ProbePreferences>(DEFAULT_PROBE_PREFERENCES);
  const [draftPreferences, setDraftPreferences] = useState<ProbePreferences>(DEFAULT_PROBE_PREFERENCES);
  const [savingProbeSettings, setSavingProbeSettings] = useState(false);
  const [historySelection, setHistorySelection] = useState<{ model: MonitorModel; target: MonitorTarget } | null>(null);
  const [targetHistory, setTargetHistory] = useState<MonitorTargetHistory | null>(null);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [historyError, setHistoryError] = useState('');

  useEffect(() => {
    if (!snapshot) return;
    setSelection(new Set(
      snapshot.items.filter((item) => item.monitor.enabled).map((item) => item.publishedModelId),
    ));
  }, [snapshot]);

  useEffect(() => {
    if (!resource.data?.settings) return;
    const preferences = preferencesFromSettings(resource.data.settings);
    setProbePreferences(preferences);
    if (!settingsOpen) setDraftPreferences(preferences);
  }, [resource.data?.settings, settingsOpen]);

  useEffect(() => {
    if (selectionOpen) setSelectionQuery('');
  }, [selectionOpen]);

  useEffect(() => {
    const timer = window.setInterval(() => {
      if (!document.hidden && !selectionOpen && !historySelection && !bulkProbe.running) void resource.refresh();
    }, 20_000);
    return () => window.clearInterval(timer);
  }, [bulkProbe.running, historySelection, resource.refresh, selectionOpen]);

  const monitored = useMemo(
    () => (snapshot?.items || []).filter((item) => item.monitor.enabled),
    [snapshot],
  );

  const visible = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return monitored.filter((item) => {
      if (!modelMatchesFilter(item, status, probePreferences.slowFirstOutputThresholdMs)) return false;
      return !normalized || `${item.publicModel} ${item.targets.map((target) => `${target.siteName} ${target.wireProtocol} ${target.sourceModel}`).join(' ')}`.toLowerCase().includes(normalized);
    });
  }, [monitored, probePreferences.slowFirstOutputThresholdMs, query, status]);

  const monitorOverview = useMemo(() => {
    const targets = monitored.flatMap((model) => model.targets);
    const routeReady = targets.filter((target) => isRouteReady(target, probePreferences.slowFirstOutputThresholdMs)).length;
    const cooling = targets.filter((target) => effectiveTargetStatus(target, probePreferences.slowFirstOutputThresholdMs) === 'cooling').length;
    const failed = targets.filter((target) => ['unavailable', 'no_credentials', 'unsupported'].includes(effectiveTargetStatus(target, probePreferences.slowFirstOutputThresholdMs))).length;
    const lastProbeAt = monitored.reduce((latest, model) => Math.max(latest, model.monitor.lastProbeFinishedAt || 0), 0);
    return { targetCount: targets.length, routeReady, cooling, failed, lastProbeAt };
  }, [monitored, probePreferences.slowFirstOutputThresholdMs]);

  const statusCounts = useMemo(() => {
    const filters: Array<Exclude<MonitorStatusFilter, 'all'>> = ['ready', 'attention', 'unavailable', 'cooling', 'unprobed'];
    return filters.reduce<Record<Exclude<MonitorStatusFilter, 'all'>, number>>((counts, filter) => {
      counts[filter] = monitored.filter((model) => modelMatchesFilter(model, filter, probePreferences.slowFirstOutputThresholdMs)).length;
      return counts;
    }, { ready: 0, attention: 0, unavailable: 0, cooling: 0, unprobed: 0 });
  }, [monitored, probePreferences.slowFirstOutputThresholdMs]);

  const selectableModels = useMemo(() => {
    const normalized = selectionQuery.trim().toLowerCase();
    return publishedModels.filter((item) => !normalized || item.publicName.toLowerCase().includes(normalized));
  }, [publishedModels, selectionQuery]);

  const toggleModelDetails = (publishedModelId: number) => {
    setExpandedModelIds((current) => {
      const next = new Set(current);
      if (next.has(publishedModelId)) next.delete(publishedModelId);
      else next.add(publishedModelId);
      return next;
    });
  };

  const probe = async (publishedModelId: number) => {
    setProbingModel(publishedModelId);
    try {
      await api.probeModel(publishedModelId);
      toast.show('这个模型的全部上游已探测', 'success');
      await resource.refresh();
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '探针失败', 'error');
      await resource.refresh();
    } finally {
      setProbingModel(null);
    }
  };

  const probeAll = async () => {
    if (!monitored.length || bulkProbe.running) return;
    setBulkProbe({ running: true, completed: 0, total: monitored.length });
    let failed = 0;
    try {
      await keepPrototypeProgressVisible();
      for (const model of monitored) {
        try {
          await api.probeModel(model.publishedModelId);
        } catch {
          failed += 1;
        } finally {
          setBulkProbe((current) => ({ ...current, completed: current.completed + 1 }));
          await keepPrototypeProgressVisible();
        }
      }
      await resource.refresh();
      toast.show(failed ? `探测完成，${failed} 个模型存在失败` : '所有监控模型已探测', failed ? 'error' : 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '探测结果刷新失败', 'error');
    } finally {
      setBulkProbe({ running: false, completed: monitored.length, total: monitored.length });
    }
  };

  const openProbeSettings = () => {
    setDraftPreferences(probePreferences);
    setSettingsOpen(true);
  };

  const saveProbeSettings = async () => {
    const settings = resource.data?.settings;
    if (!settings) return;
    if (draftPreferences.slowFirstOutputThresholdMs >= settings.firstOutputTimeoutMs) {
      toast.show('首字冷却阈值需要短于首字超时，才能在请求超时前生效。', 'error');
      return;
    }
    setSavingProbeSettings(true);
    try {
      const { revision, ...current } = settings;
      const updated = await api.updateSettings(revision, {
        ...current,
        probeIntervalMs: draftPreferences.okProbeIntervalMs,
        failureThreshold: draftPreferences.failureThreshold,
        cooldownMs: draftPreferences.cooldownMs,
      });
      const savedPrototypePreferences = saveProbePrototypePreferences({
        slowFirstOutputThresholdMs: draftPreferences.slowFirstOutputThresholdMs,
      });
      setProbePreferences({
        ...preferencesFromSettings(updated),
        slowFirstOutputThresholdMs: savedPrototypePreferences.slowFirstOutputThresholdMs,
      });
      resource.setData((value) => value ? { ...value, settings: updated } : value);
      setSettingsOpen(false);
      toast.show('探针设置已保存，并与全局设置同步', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '探针设置保存失败', 'error');
      await resource.refresh();
    } finally {
      setSavingProbeSettings(false);
    }
  };

  const openHistory = async (model: MonitorModel, target: MonitorTarget) => {
    setHistorySelection({ model, target });
    setTargetHistory(null);
    setHistoryError('');
    setHistoryLoading(true);
    try {
      setTargetHistory(await api.monitorTargetHistory(model.publishedModelId, target.providerModelTargetId));
    } catch (reason) {
      setHistoryError(reason instanceof Error ? reason.message : '探针历史加载失败');
    } finally {
      setHistoryLoading(false);
    }
  };

  const saveSelection = async () => {
    if (!snapshot) return;
    setSavingSelection(true);
    try {
      const current = new Map(snapshot.items.map((item) => [item.publishedModelId, item]));
      const changes: Array<Promise<MonitorSetting>> = [];
      for (const model of publishedModels) {
        const selected = selection.has(model.publishedModelId);
        const existing = current.get(model.publishedModelId);
        if (selected && !existing) {
          changes.push(api.createMonitorModel(model.publishedModelId, {
            enabled: true,
            historyLimit: 24,
          }));
        } else if (existing && selected !== existing.monitor.enabled) {
          changes.push(api.updateMonitorModel(model.publishedModelId, existing.monitor.revision, { enabled: selected }));
        }
      }
      await Promise.all(changes);
      await resource.refresh();
      setSelectionOpen(false);
      toast.show('监控模型已更新', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '监控选择保存失败', 'error');
      await resource.refresh();
    } finally {
      setSavingSelection(false);
    }
  };

  if (resource.loading && !resource.data) return <div className="page"><LoadingState label="正在读取模型监控" /></div>;
  if (resource.error && !resource.data) return <div className="page"><ErrorState message={resource.error} onRetry={() => void resource.refresh()} /></div>;

  const bulkProbeLabel = bulkProbe.running
    ? `探测中 ${bulkProbe.completed} / ${bulkProbe.total}`
    : '立即探测全部';
  const latestHistoryPoint = targetHistory?.items[targetHistory.items.length - 1];

  return (
    <div className="page monitor-page">
      <PageHeader
        title="模型监控"
        description="只探测选中的模型；每个上游分别展示连接、探针、路由资格和真实响应速度。"
        actions={<>
          <Button icon={ListChecks} onClick={() => setSelectionOpen(true)} disabled={!snapshot}>选择监控模型</Button>
          <Button icon={Settings2} onClick={openProbeSettings}>探针设置</Button>
          <Button variant="primary" icon={RefreshCw} busy={bulkProbe.running} onClick={() => void probeAll()} disabled={!monitored.length}>{bulkProbeLabel}</Button>
        </>}
      />

      <div className="routing-policy-strip" role="status">
        <Activity size={15} />
        <span><strong>自动 OK 探针</strong> · 每 {probeIntervalLabel(probePreferences.okProbeIntervalMs)}检测一次 · 首字超过 {probeIntervalLabel(probePreferences.slowFirstOutputThresholdMs)}单次冷却 · 普通失败连续 {probePreferences.failureThreshold} 次后冷却 {probeIntervalLabel(probePreferences.cooldownMs)}</span>
      </div>

      {snapshot && <div className="monitor-overview-strip">
        <div><span>监控模型</span><strong>{monitored.length}</strong><small>仅计算已选择模型</small></div>
        <div><span>上游探测目标</span><strong>{monitorOverview.targetCount}</strong><small>按站点与模型分别检测</small></div>
        <div><span>当前可路由</span><strong>{monitorOverview.routeReady} / {monitorOverview.targetCount}</strong><small>{monitorOverview.cooling} 冷却 · {monitorOverview.failed} 失败</small></div>
        <div><span>最近自动探测</span><strong>{monitorOverview.lastProbeAt ? formatRelativeTime(monitorOverview.lastProbeAt) : '尚未探测'}</strong><small>页面每 20 秒刷新状态</small></div>
      </div>}

      {!snapshot ? <Panel><UnavailableState title="模型监控暂不可用" description="当前实例尚未提供监控数据，请检查服务状态后重试。" /></Panel> : <Panel className="list-panel">
        <FilterBar trailing={<span className="result-count">自动刷新 · {visible.length} / {monitored.length} 个模型</span>}>
          <SearchField value={query} onChange={setQuery} placeholder="搜索模型、站点或 API 类型" />
          <div className="segmented" role="group" aria-label="监控状态">
            {([
              { value: 'all', label: '全部', count: monitored.length },
              { value: 'ready', label: '全部可路由', count: statusCounts.ready },
              { value: 'attention', label: '部分可路由', count: statusCounts.attention },
              { value: 'unavailable', label: '无可用路由', count: statusCounts.unavailable },
              { value: 'cooling', label: '冷却中', count: statusCounts.cooling },
              { value: 'unprobed', label: '尚未探测', count: statusCounts.unprobed },
            ] as const).map((item) => <button type="button" className={status === item.value ? 'is-active' : ''} onClick={() => setStatus(item.value)} key={item.value}>{item.label}<span className="monitor-filter-count">{item.count}</span></button>)}
          </div>
        </FilterBar>

        {!monitored.length ? <EmptyState title="还没有选择监控模型" description={`选择需要保障的模型后，每 ${probeIntervalLabel(probePreferences.okProbeIntervalMs)}发送一次低 Token 的 OK 探针。`} action={<Button variant="primary" icon={ListChecks} onClick={() => setSelectionOpen(true)}>选择模型</Button>} /> : visible.length === 0 ? <EmptyState title="没有匹配的监控模型" description="调整搜索词或状态筛选。" /> : <div className="monitor-model-list">{visible.map((model) => {
          const expanded = expandedModelIds.has(model.publishedModelId);
          const connected = model.targets.filter((target) => targetConnection(target).tone === 'success').length;
          const probeHealthy = model.targets.filter((target) => targetProbeState(target, probePreferences.slowFirstOutputThresholdMs) === 'healthy').length;
          const routeReady = model.targets.filter((target) => isRouteReady(target, probePreferences.slowFirstOutputThresholdMs)).length;
          return <article className="monitor-model" key={model.publishedModelId}>
            <header className="monitor-model-header">
              <button
                type="button"
                className="monitor-model-summary"
                aria-expanded={expanded}
                title={expanded ? '点击收起监控明细' : '点击展开监控明细'}
                onClick={() => toggleModelDetails(model.publishedModelId)}
              >
                <span className="monitor-model-icon"><HeartPulse size={17} /></span>
                <span className="monitor-model-name"><span><code>{model.publicModel}</code><Badge tone={routeReady === model.targets.length && model.targets.length ? 'success' : routeReady ? 'warning' : 'danger'}>{routeReady === model.targets.length && model.targets.length ? '全部可路由' : routeReady ? `${routeReady} / ${model.targets.length} 可路由` : '无可用路由'}</Badge></span><small>最近探测 {formatRelativeTime(model.monitor.lastProbeFinishedAt)}</small></span>
                <span className="monitor-model-stats"><span><strong>{connected} / {model.targets.length}</strong>连接正常</span><span><strong>{probeHealthy} / {model.targets.length}</strong>探针通过</span><span><strong>{routeReady} / {model.targets.length}</strong>参与路由</span></span>
              </button>
              <div className="model-route-actions"><Button size="sm" icon={RefreshCw} busy={probingModel === model.publishedModelId || model.monitor.busy} disabled={bulkProbe.running} onClick={() => void probe(model.publishedModelId)}>探测全部上游</Button><IconButton className={`model-route-disclosure ${expanded ? 'is-open' : ''}`} label={`${expanded ? '收起' : '展开'} ${model.publicModel} 的监控明细`} aria-expanded={expanded} onClick={() => toggleModelDetails(model.publishedModelId)}><ChevronDown size={17} /></IconButton></div>
            </header>
            {expanded && <div className="monitor-targets">{model.targets.map((target) => {
              const connection = targetConnection(target);
              const slowFirstOutput = isSlowFirstOutput(target.latest, probePreferences.slowFirstOutputThresholdMs);
              const effectiveStatus = effectiveTargetStatus(target, probePreferences.slowFirstOutputThresholdMs);
              const probeState = probePresentation(target, probePreferences.slowFirstOutputThresholdMs);
              const routeState = routingPresentation(target, probePreferences.slowFirstOutputThresholdMs);
              return <div className="monitor-target" key={target.publishedModelTargetId}>
                <span className="monitor-target-rank" title={`路由优先级 ${target.position + 1}`}>{target.position + 1}</span>
                <div className="monitor-target-identity"><strong>{target.siteName}</strong><span>{monitorCredentialName(target) || `${protocolLabel(target.wireProtocol)} · ${target.usableCredentialCount} 个 API Key`}</span><code>{target.sourceModel}</code></div>
                <div className="monitor-target-states">
                  <span><small>连接</small><Badge tone={connection.tone}>{connection.label}</Badge></span>
                  <span><small>探针</small><Badge tone={probeState.tone}>{probeState.label}</Badge></span>
                  <span><small>路由资格</small><Badge tone={routeState.tone}>{routeState.label}</Badge></span>
                </div>
                <div className="monitor-target-metrics">
                  <span><small>成功率</small><strong>{basisPoints(target.successBasisPoints)}</strong></span>
                  <span><small>首字</small><strong>{formatProbeSeconds(target.latest?.firstOutputMs)}</strong></span>
                  <span><small>总耗时</small><strong>{formatProbeSeconds(target.latest?.totalLatencyMs)}</strong></span>
                </div>
                <div className="monitor-target-observation">
                  <button type="button" className="status-history" aria-label={`查看 ${target.siteName} 的完整探针历史`} title="查看完整探针历史" onClick={() => void openHistory(model, target)}>{target.statusBar.slice(-16).map((point) => <span className={`history-${isSlowFirstOutput(point, probePreferences.slowFirstOutputThresholdMs) ? 'slow' : point.outcome}`} title={pointTitle(point, probePreferences.slowFirstOutputThresholdMs)} key={`${point.runId}-${point.finishedAt}`} />)}<History size={14} /></button>
                  <div className="target-next-state">{slowFirstOutput ? <><TimerReset size={14} /><span title={`首字超过 ${probeIntervalLabel(probePreferences.slowFirstOutputThresholdMs)}，冷却 ${probeIntervalLabel(probePreferences.cooldownMs)}`}>慢首字 · 冷却 {probeIntervalLabel(probePreferences.cooldownMs)}</span></> : effectiveStatus === 'cooling' ? <><TimerReset size={14} /><span>{formatRelativeTime(target.health?.cooldownUntil)}恢复测试</span></> : target.latest?.errorCode ? <span title={target.latest.errorCode}>{target.latest.errorCode}</span> : <><History size={14} /><span>{formatRelativeTime(target.latest?.finishedAt)}检测</span></>}</div>
                </div>
              </div>;
            })}</div>}
          </article>;
        })}</div>}
      </Panel>}

      <Dialog open={selectionOpen && Boolean(snapshot)} title="选择监控模型" description="只有勾选的模型会发送定时 OK 探针。" onClose={() => setSelectionOpen(false)} footer={<><Button onClick={() => setSelectionOpen(false)}>取消</Button><Button variant="primary" busy={savingSelection} onClick={() => void saveSelection()}>保存选择</Button></>}>
        <div className="monitor-selection-toolbar"><SearchField value={selectionQuery} onChange={setSelectionQuery} placeholder="搜索已发布模型" /><div className="monitor-selection-actions"><Button size="sm" onClick={() => setSelection(new Set(publishedModels.filter((item) => item.enabled).map((item) => item.publishedModelId)))}>全选可用</Button><Button size="sm" onClick={() => setSelection(new Set())}>清空</Button><span>{selection.size} 个已选择</span></div></div>
        <div className="monitor-selection-list">{selectableModels.map((item) => {
          const disabled = !item.enabled;
          return <label className={disabled ? 'is-disabled' : ''} key={item.publishedModelId}><input type="checkbox" disabled={disabled} checked={selection.has(item.publishedModelId)} onChange={() => setSelection((current) => { const next = new Set(current); if (next.has(item.publishedModelId)) next.delete(item.publishedModelId); else next.add(item.publishedModelId); return next; })} /><span className="check-visual"><Check size={13} /></span><code>{item.publicName}</code><Badge tone={disabled ? 'neutral' : 'info'}>{disabled ? '模型已暂停' : `${probeIntervalLabel(probePreferences.okProbeIntervalMs)}一次`}</Badge></label>;
        })}{!selectableModels.length && <EmptyState title="没有匹配的模型" description="调整搜索词后再选择。" />}</div>
      </Dialog>

      <Dialog open={settingsOpen} title="探针设置" description="低 Token 的 OK 探针同时测量首字延迟和总耗时；只有选中的模型会定时检测。" onClose={() => setSettingsOpen(false)} footer={<><Button onClick={() => setSettingsOpen(false)}>取消</Button><Button variant="primary" busy={savingProbeSettings} onClick={() => void saveProbeSettings()}>保存设置</Button></>}>
        <div className="form-stack">
          <div className="form-grid two-columns">
            <Field label="OK 模型探针"><select className="select" value={draftPreferences.okProbeIntervalMs} onChange={(event) => setDraftPreferences((current) => ({ ...current, okProbeIntervalMs: Number(event.target.value) }))}><option value={60_000}>1 分钟</option><option value={300_000}>5 分钟</option><option value={600_000}>10 分钟</option><option value={900_000}>15 分钟</option></select></Field>
            <Field label="连续失败阈值"><select className="select" value={draftPreferences.failureThreshold} onChange={(event) => setDraftPreferences((current) => ({ ...current, failureThreshold: Number(event.target.value) }))}><option value={2}>2 次</option><option value={3}>3 次</option><option value={4}>4 次</option></select></Field>
            <Field label="冷却时间"><select className="select" value={draftPreferences.cooldownMs} onChange={(event) => setDraftPreferences((current) => ({ ...current, cooldownMs: Number(event.target.value) }))}><option value={60_000}>1 分钟</option><option value={300_000}>5 分钟</option><option value={600_000}>10 分钟</option><option value={1_800_000}>30 分钟</option></select></Field>
            <Field label="首字冷却阈值" hint="单次超过阈值即进入冷却。"><div className="unit-input"><input className="input" type="number" min={1} max={120} value={Math.round(draftPreferences.slowFirstOutputThresholdMs / 1_000)} onChange={(event) => setDraftPreferences((current) => ({ ...current, slowFirstOutputThresholdMs: Math.max(1, Number(event.target.value)) * 1_000 }))} /><span>秒</span></div></Field>
          </div>
          <div className="probe-policy-preview"><span><small>探测频率</small><strong>{probeIntervalLabel(draftPreferences.okProbeIntervalMs)}</strong></span><span><small>慢首字阈值</small><strong>{probeIntervalLabel(draftPreferences.slowFirstOutputThresholdMs)}</strong></span><span><small>失败冷却</small><strong>{draftPreferences.failureThreshold} 次后</strong></span><span><small>冷却时长</small><strong>{probeIntervalLabel(draftPreferences.cooldownMs)}</strong></span></div>
          <InlineNotice tone="info">固定提示词：<code>Reply exactly OK</code>，最多 4 Token。慢首字单次冷却，普通失败累计到阈值后冷却。</InlineNotice>
        </div>
      </Dialog>

      <Dialog open={Boolean(historySelection)} title={historySelection ? `${historySelection.model.publicModel} · ${historySelection.target.siteName}` : ''} description={historySelection ? `${monitorCredentialName(historySelection.target) || protocolLabel(historySelection.target.wireProtocol)} · ${historySelection.target.sourceModel}` : ''} onClose={() => { setHistorySelection(null); setTargetHistory(null); setHistoryError(''); }} width="lg">
        {historyLoading ? <LoadingState label="正在读取完整探针历史" /> : historyError ? <ErrorState message={historyError} onRetry={() => historySelection && void openHistory(historySelection.model, historySelection.target)} /> : targetHistory && <div className="probe-history-detail">
          <div className="probe-history-summary"><div><span>当前状态</span><HealthBadge state={isSlowFirstOutput(latestHistoryPoint, probePreferences.slowFirstOutputThresholdMs) ? 'cooling' : targetHistory.status} /></div><div><span>成功率</span><strong>{basisPoints(targetHistory.successBasisPoints)}</strong></div><div><span>最近首字</span><strong>{formatProbeSeconds(latestHistoryPoint?.firstOutputMs)}</strong></div><div><span>最近总耗时</span><strong>{formatProbeSeconds(latestHistoryPoint?.totalLatencyMs)}</strong></div><div><span>成功 / 失败</span><strong>{targetHistory.successes} / {targetHistory.failures}</strong></div></div>
          <div className="probe-history-list">{[...targetHistory.items].reverse().map((point) => {
            const slowFirstOutput = isSlowFirstOutput(point, probePreferences.slowFirstOutputThresholdMs);
            const permitReason = probePermitReasonLabel(point.permitReason);
            return <div key={`${point.runId}-${point.id || point.finishedAt}`}><span className={`history-mark history-${slowFirstOutput ? 'slow' : point.outcome}`} /><div><header><strong>{formatDateTime(point.finishedAt)}</strong><Badge tone={slowFirstOutput ? 'warning' : point.outcome === 'success' ? 'success' : point.outcome === 'failure' ? 'danger' : 'neutral'}>{slowFirstOutput ? '首字过慢' : probeOutcomeLabel(point.outcome)}</Badge></header><p>首字延迟 {formatProbeSeconds(point.firstOutputMs)} · 总耗时 {formatProbeSeconds(point.totalLatencyMs)}{point.httpStatus ? ` · HTTP ${point.httpStatus}` : ''}</p>{slowFirstOutput && <small>超过 {probeIntervalLabel(probePreferences.slowFirstOutputThresholdMs)}阈值，本次探针触发冷却。</small>}{point.errorCode && <small>{point.failureKind || '探针错误'} · {point.errorCode}</small>}{permitReason && <small>调度结果：{permitReason}</small>}</div></div>;
          })}</div>
        </div>}
      </Dialog>
    </div>
  );
}
