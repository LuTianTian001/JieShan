import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from '@dnd-kit/core';
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import { ArrowDown, ArrowUp, GripVertical, LoaderCircle } from 'lucide-react';
import { useMemo } from 'react';
import { Badge, IconButton } from '../../components/ui';
import { formatDateTime } from '../../lib/format';
import type { ModelTarget, MonitorTarget, RouteTarget } from '../../lib/types';
import {
  flattenUpstreamGroups,
  groupUpstreamTargets,
  upstreamChannelSummary,
  type UpstreamGroup,
} from '../../lib/upstreamGroups';

type BadgeTone = 'neutral' | 'accent' | 'success' | 'warning' | 'danger' | 'info';

interface StatePresentation {
  label: string;
  tone: BadgeTone;
}

function successRate(basisPoints: number): string {
  return `${(basisPoints / 100).toFixed(basisPoints % 100 ? 1 : 0)}%`;
}

function formatProbeSeconds(value: number | null | undefined): string {
  if (value === null || value === undefined) return '—';
  return `${(value / 1_000).toFixed(2)} 秒`;
}

function cooldownLabel(targets: readonly MonitorTarget[]): string {
  const until = Math.max(0, ...targets.map((target) => target.health?.cooldownUntil || 0));
  if (!until || until <= Date.now()) return '';
  const seconds = Math.max(1, Math.ceil((until - Date.now()) / 1000));
  return seconds >= 60 ? `${Math.ceil(seconds / 60)} 分钟后恢复` : `${seconds} 秒后恢复`;
}

function isSlowFirstOutput(target: MonitorTarget | undefined, thresholdMs: number): boolean {
  const firstOutputMs = target?.latest?.firstOutputMs;
  return target?.latest?.outcome === 'success'
    && firstOutputMs !== null
    && firstOutputMs !== undefined
    && firstOutputMs > thresholdMs;
}

function thresholdLabel(value: number): string {
  if (value >= 60_000 && value % 60_000 === 0) return `${value / 60_000} 分钟`;
  return `${Math.max(1, Math.round(value / 1_000))} 秒`;
}

function credentialSummary(
  catalogs: readonly ModelTarget[],
  monitors: readonly MonitorTarget[],
): string {
  const monitorNames = monitors.map((monitor) => ((monitor as MonitorTarget & { credentialName?: string }).credentialName || '').trim()).filter(Boolean);
  const catalogNames = catalogs.flatMap((catalog) => catalog.credentialNames || []).filter(Boolean);
  const names = [...new Set([...monitorNames, ...catalogNames])];
  if (names.length <= 2 && names.length) return names.join('、');
  if (names.length > 2) return `${names.slice(0, 2).join('、')} +${names.length - 2}`;
  const usableCredentialCount = Math.max(0, ...catalogs.map((catalog) => catalog.usableCredentialCount));
  return `${usableCredentialCount} 个 API Key`;
}

function connectionPresentation(catalog?: ModelTarget, monitor?: MonitorTarget): StatePresentation {
  if (!catalog) return { label: '等待目录', tone: 'neutral' };
  if (!catalog.enabled || !catalog.siteEnabled || !catalog.endpointEnabled) return { label: '配置停用', tone: 'neutral' };
  if (!catalog.usableCredentialCount) return { label: '无可用 Key', tone: 'danger' };
  if (monitor?.latest?.httpStatus === 401 || monitor?.latest?.httpStatus === 403) return { label: '鉴权失败', tone: 'danger' };
  const failure = `${monitor?.latest?.failureKind || ''} ${monitor?.latest?.errorCode || ''}`.toLowerCase();
  if (monitor?.latest?.outcome === 'failure' && /(connect|network|dns|tls|timeout)/.test(failure)) {
    return { label: '连接异常', tone: 'warning' };
  }
  if (!monitor?.latest) return { label: 'Key 已就绪', tone: 'info' };
  return { label: '已连接', tone: 'success' };
}

function probePresentation(
  monitor: MonitorTarget | undefined,
  slowFirstOutputThresholdMs: number,
): StatePresentation {
  if (!monitor?.latest) return { label: '未探测', tone: 'neutral' };
  if (monitor.latest.outcome === 'skipped') return { label: '本次跳过', tone: 'neutral' };
  if (monitor.latest.outcome === 'failure') return { label: '探针失败', tone: 'danger' };
  if (isSlowFirstOutput(monitor, slowFirstOutputThresholdMs)) return { label: '通过但过慢', tone: 'warning' };
  return { label: '探针通过', tone: 'success' };
}

function routingPresentation(
  catalog: ModelTarget | undefined,
  monitor: MonitorTarget | undefined,
  slowFirstOutputThresholdMs: number,
): StatePresentation {
  if (!catalog?.routable || !catalog.usableCredentialCount || !catalog.enabled || !catalog.siteEnabled || !catalog.endpointEnabled) {
    return { label: '不可路由', tone: 'danger' };
  }
  if (isSlowFirstOutput(monitor, slowFirstOutputThresholdMs) || monitor?.status === 'cooling') {
    return { label: '冷却中', tone: 'warning' };
  }
  if (!monitor || ['unprobed', 'skipped'].includes(monitor.status)) return { label: '待探测', tone: 'neutral' };
  if (['unavailable', 'disabled', 'model_disabled', 'no_credentials', 'unsupported'].includes(monitor.status)) {
    return { label: '不可路由', tone: 'danger' };
  }
  if (['degraded', 'suspect', 'recovering'].includes(monitor.status)) return { label: '可路由 · 观察', tone: 'warning' };
  return { label: '可路由', tone: 'success' };
}

function allSame(presentations: readonly StatePresentation[]): boolean {
  const first = presentations[0];
  return Boolean(first) && presentations.every((presentation) => presentation.label === first.label && presentation.tone === first.tone);
}

function aggregateConnection(presentations: readonly StatePresentation[]): StatePresentation {
  if (!presentations.length) return { label: '等待目录', tone: 'neutral' };
  if (allSame(presentations)) return presentations[0];
  if (presentations.some((item) => item.tone === 'danger')) {
    return presentations.every((item) => item.tone === 'danger')
      ? { label: '连接异常', tone: 'danger' }
      : { label: '部分连接异常', tone: 'warning' };
  }
  if (presentations.some((item) => item.tone === 'warning')) return { label: '部分连接异常', tone: 'warning' };
  if (presentations.some((item) => item.tone === 'success')) return { label: '部分待确认', tone: 'info' };
  if (presentations.some((item) => item.tone === 'info')) return { label: 'Key 已就绪', tone: 'info' };
  return { label: '等待目录', tone: 'neutral' };
}

function aggregateProbe(presentations: readonly StatePresentation[]): StatePresentation {
  if (!presentations.length) return { label: '未探测', tone: 'neutral' };
  if (allSame(presentations)) return presentations[0];
  if (presentations.some((item) => item.tone === 'danger')) {
    return presentations.every((item) => item.tone === 'danger')
      ? { label: '探针失败', tone: 'danger' }
      : { label: '部分探针失败', tone: 'warning' };
  }
  if (presentations.some((item) => item.tone === 'warning')) return { label: '部分通道过慢', tone: 'warning' };
  if (presentations.some((item) => item.tone === 'success')) return { label: '部分未探测', tone: 'info' };
  return { label: '未完全探测', tone: 'neutral' };
}

function aggregateRouting(presentations: readonly StatePresentation[]): StatePresentation {
  if (!presentations.length) return { label: '不可路由', tone: 'danger' };
  if (allSame(presentations)) return presentations[0];
  if (presentations.some((item) => item.tone === 'danger')) {
    return presentations.every((item) => item.tone === 'danger')
      ? { label: '不可路由', tone: 'danger' }
      : { label: '部分可路由', tone: 'warning' };
  }
  if (presentations.some((item) => item.tone === 'warning')) return { label: '可路由 · 部分观察', tone: 'warning' };
  if (presentations.some((item) => item.tone === 'success')) return { label: '可路由 · 待探测', tone: 'info' };
  return { label: '待探测', tone: 'neutral' };
}

function maximumMetric(
  monitors: readonly MonitorTarget[],
  selectValue: (monitor: MonitorTarget) => number | null | undefined,
): number | null {
  const values = monitors.map(selectValue).filter((value): value is number => value !== null && value !== undefined);
  return values.length ? Math.max(...values) : null;
}

function SortableTarget({
  group,
  position,
  catalogById,
  monitorById,
  slowFirstOutputThresholdMs,
  disabled,
  total,
  onMove,
}: {
  group: UpstreamGroup<RouteTarget>;
  position: number;
  catalogById: ReadonlyMap<number, ModelTarget>;
  monitorById: ReadonlyMap<number, MonitorTarget>;
  slowFirstOutputThresholdMs: number;
  disabled: boolean;
  total: number;
  onMove: (direction: -1 | 1) => void;
}) {
  const sortable = useSortable({ id: group.key, disabled });
  const style = {
    transform: CSS.Transform.toString(sortable.transform),
    transition: sortable.transition,
  };
  const members = group.targets.map((target) => ({
    catalog: catalogById.get(target.providerModelTargetId),
    monitor: monitorById.get(target.providerModelTargetId),
  }));
  const catalogs = members.map((member) => member.catalog).filter((catalog): catalog is ModelTarget => Boolean(catalog));
  const monitors = members.map((member) => member.monitor).filter((monitor): monitor is MonitorTarget => Boolean(monitor));
  const connection = aggregateConnection(members.map((member) => connectionPresentation(member.catalog, member.monitor)));
  const probe = aggregateProbe(members.map((member) => probePresentation(member.monitor, slowFirstOutputThresholdMs)));
  const routing = aggregateRouting(members.map((member) => routingPresentation(member.catalog, member.monitor, slowFirstOutputThresholdMs)));
  const slowFirstOutput = monitors.some((monitor) => isSlowFirstOutput(monitor, slowFirstOutputThresholdMs));
  const recovery = cooldownLabel(monitors);
  const firstOutputMs = maximumMetric(monitors, (monitor) => monitor.latest?.firstOutputMs);
  const totalLatencyMs = maximumMetric(monitors, (monitor) => monitor.latest?.totalLatencyMs);
  const observedMonitors = monitors.filter((monitor) => Boolean(monitor.latest));
  const lowestSuccessBasisPoints = observedMonitors.length ? Math.min(...observedMonitors.map((monitor) => monitor.successBasisPoints)) : null;
  const latestFinishedAt = maximumMetric(observedMonitors, (monitor) => monitor.latest?.finishedAt);

  return (
    <div
      ref={sortable.setNodeRef}
      style={style}
      className={`route-target-row ${sortable.isDragging ? 'is-dragging' : ''}`}
    >
      <div className="route-priority-cell">
        <span className="route-position" title={position === 0 ? '首选上游' : `第 ${position + 1} 顺位`}>{position + 1}</span>
        <IconButton
          label={`拖动 ${group.siteName} 调整优先级`}
          className="drag-handle"
          disabled={disabled}
          {...sortable.attributes}
          {...sortable.listeners}
        >
          <GripVertical size={17} />
        </IconButton>
      </div>

      <div className="route-target-name">
        <span className="route-target-title"><strong>{group.siteName}</strong>{position === 0 && <Badge tone="accent">首选</Badge>}</span>
        <span>{credentialSummary(catalogs, monitors)}</span>
        <span className="route-target-channels" title={upstreamChannelSummary(group.targets)}>{upstreamChannelSummary(group.targets)}</span>
        <code>{group.sourceModel}</code>
      </div>

      <div className="route-target-runtime">
        <span><small>连接</small><Badge tone={connection.tone}>{connection.label}</Badge></span>
        <span><small>探针</small><Badge tone={probe.tone}>{probe.label}</Badge></span>
        <span><small>路由资格</small><Badge tone={routing.tone}>{routing.label}</Badge></span>
      </div>

      <div className="route-target-latency">
        <span><small>最慢首字</small><strong>{formatProbeSeconds(firstOutputMs)}</strong></span>
        <span><small>最慢总耗时</small><strong>{formatProbeSeconds(totalLatencyMs)}</strong></span>
        <small>
          {lowestSuccessBasisPoints !== null && latestFinishedAt !== null
            ? `${observedMonitors.length > 1 ? '最低 ' : ''}${successRate(lowestSuccessBasisPoints)} 成功 · ${formatDateTime(latestFinishedAt, true)}`
            : '等待首次探针'}
        </small>
        {slowFirstOutput && <em>有通道超过 {thresholdLabel(slowFirstOutputThresholdMs)}</em>}
        {!slowFirstOutput && recovery && <em>{recovery}</em>}
      </div>

      <div className="route-target-controls">
        <div className="route-move-actions">
          <IconButton label={`上移 ${group.siteName}`} disabled={disabled || position === 0} onClick={() => onMove(-1)}><ArrowUp size={15} /></IconButton>
          <IconButton label={`下移 ${group.siteName}`} disabled={disabled || position === total - 1} onClick={() => onMove(1)}><ArrowDown size={15} /></IconButton>
        </div>
      </div>
    </div>
  );
}

export function RouteTargetOrder({
  targets,
  catalog,
  monitorTargets = [],
  slowFirstOutputThresholdMs,
  saving,
  readOnly = false,
  onReorder,
}: {
  targets: RouteTarget[];
  catalog: ModelTarget[];
  monitorTargets?: MonitorTarget[];
  slowFirstOutputThresholdMs: number;
  saving: boolean;
  readOnly?: boolean;
  onReorder: (targetIds: number[]) => Promise<void>;
}) {
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  const groups = useMemo(() => groupUpstreamTargets(targets), [targets]);
  const catalogById = useMemo(() => new Map(catalog.map((item) => [item.id, item])), [catalog]);
  const monitorById = useMemo(() => new Map(monitorTargets.map((item) => [item.providerModelTargetId, item])), [monitorTargets]);

  const saveGroups = (nextGroups: readonly UpstreamGroup<RouteTarget>[]) => {
    void onReorder(flattenUpstreamGroups(nextGroups, (target) => target.providerModelTargetId));
  };

  const move = (index: number, direction: -1 | 1) => {
    const nextIndex = index + direction;
    if (nextIndex < 0 || nextIndex >= groups.length) return;
    saveGroups(arrayMove(groups, index, nextIndex));
  };

  const dragEnd = (event: DragEndEvent) => {
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    const oldIndex = groups.findIndex((group) => group.key === active.id);
    const newIndex = groups.findIndex((group) => group.key === over.id);
    if (oldIndex < 0 || newIndex < 0) return;
    saveGroups(arrayMove(groups, oldIndex, newIndex));
  };

  return (
    <div className="route-order-wrap">
      {saving && <span className="route-saving"><LoaderCircle className="spin" size={13} />正在保存顺序</span>}
      <div className="route-target-table-head" aria-hidden="true">
        <span>优先级</span><span>上游与通道</span><span>运行状态</span><span>响应速度</span><span>排序操作</span>
      </div>
      <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={dragEnd}>
        <SortableContext items={groups.map((group) => group.key)} strategy={verticalListSortingStrategy}>
          <div className="route-target-list">
            {groups.map((group, index) => (
              <SortableTarget
                key={group.key}
                group={group}
                position={index}
                catalogById={catalogById}
                monitorById={monitorById}
                slowFirstOutputThresholdMs={slowFirstOutputThresholdMs}
                disabled={saving || readOnly}
                total={groups.length}
                onMove={(direction) => move(index, direction)}
              />
            ))}
          </div>
        </SortableContext>
      </DndContext>
    </div>
  );
}
