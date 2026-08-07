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
import { Badge, IconButton } from '../../components/ui';
import { formatDateTime, protocolLabel } from '../../lib/format';
import type { ModelTarget, MonitorTarget, RouteTarget } from '../../lib/types';

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

function cooldownLabel(target?: MonitorTarget): string {
  const until = target?.health?.cooldownUntil;
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

function credentialSummary(target: RouteTarget, catalog?: ModelTarget, monitor?: MonitorTarget): string {
  const monitorName = ((monitor as (MonitorTarget & { credentialName?: string }) | undefined)?.credentialName || '').trim();
  if (monitorName) return monitorName;
  const names = ((catalog as (ModelTarget & { credentialNames?: string[] }) | undefined)?.credentialNames || []).filter(Boolean);
  if (names.length <= 2 && names.length) return names.join('、');
  if (names.length > 2) return `${names.slice(0, 2).join('、')} +${names.length - 2}`;
  return `${protocolLabel(target.wireProtocol)} · ${catalog?.usableCredentialCount || 0} 个 API Key`;
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

function SortableTarget({
  target,
  position,
  catalog,
  monitor,
  slowFirstOutputThresholdMs,
  disabled,
  total,
  onMove,
}: {
  target: RouteTarget;
  position: number;
  catalog?: ModelTarget;
  monitor?: MonitorTarget;
  slowFirstOutputThresholdMs: number;
  disabled: boolean;
  total: number;
  onMove: (direction: -1 | 1) => void;
}) {
  const sortable = useSortable({ id: target.providerModelTargetId, disabled });
  const style = {
    transform: CSS.Transform.toString(sortable.transform),
    transition: sortable.transition,
  };
  const connection = connectionPresentation(catalog, monitor);
  const probe = probePresentation(monitor, slowFirstOutputThresholdMs);
  const routing = routingPresentation(catalog, monitor, slowFirstOutputThresholdMs);
  const slowFirstOutput = isSlowFirstOutput(monitor, slowFirstOutputThresholdMs);
  const recovery = cooldownLabel(monitor);

  return (
    <div
      ref={sortable.setNodeRef}
      style={style}
      className={`route-target-row ${sortable.isDragging ? 'is-dragging' : ''}`}
    >
      <div className="route-priority-cell">
        <span className="route-position" title={position === 0 ? '首选上游' : `第 ${position + 1} 顺位`}>{position + 1}</span>
        <IconButton
          label={`拖动 ${target.siteName} 调整优先级`}
          className="drag-handle"
          disabled={disabled}
          {...sortable.attributes}
          {...sortable.listeners}
        >
          <GripVertical size={17} />
        </IconButton>
      </div>

      <div className="route-target-name">
        <span className="route-target-title"><strong>{target.siteName}</strong>{position === 0 && <Badge tone="accent">首选</Badge>}</span>
        <span>{credentialSummary(target, catalog, monitor)}</span>
        <code>{target.sourceModel}</code>
      </div>

      <div className="route-target-runtime">
        <span><small>连接</small><Badge tone={connection.tone}>{connection.label}</Badge></span>
        <span><small>探针</small><Badge tone={probe.tone}>{probe.label}</Badge></span>
        <span><small>路由资格</small><Badge tone={routing.tone}>{routing.label}</Badge></span>
      </div>

      <div className="route-target-latency">
        <span><small>首字</small><strong>{formatProbeSeconds(monitor?.latest?.firstOutputMs)}</strong></span>
        <span><small>总耗时</small><strong>{formatProbeSeconds(monitor?.latest?.totalLatencyMs)}</strong></span>
        <small>
          {monitor?.latest
            ? `${successRate(monitor.successBasisPoints)} 成功 · ${formatDateTime(monitor.latest.finishedAt, true)}`
            : '等待首次探针'}
        </small>
        {slowFirstOutput && <em>超过 {thresholdLabel(slowFirstOutputThresholdMs)}</em>}
        {!slowFirstOutput && recovery && <em>{recovery}</em>}
      </div>

      <div className="route-target-controls">
        <div className="route-move-actions">
          <IconButton label={`上移 ${target.siteName}`} disabled={disabled || position === 0} onClick={() => onMove(-1)}><ArrowUp size={15} /></IconButton>
          <IconButton label={`下移 ${target.siteName}`} disabled={disabled || position === total - 1} onClick={() => onMove(1)}><ArrowDown size={15} /></IconButton>
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
  const catalogById = new Map(catalog.map((item) => [item.id, item]));
  const monitorById = new Map(monitorTargets.map((item) => [item.providerModelTargetId, item]));

  const move = (index: number, direction: -1 | 1) => {
    const nextIndex = index + direction;
    if (nextIndex < 0 || nextIndex >= targets.length) return;
    void onReorder(arrayMove(targets, index, nextIndex).map((item) => item.providerModelTargetId));
  };

  const dragEnd = (event: DragEndEvent) => {
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    const oldIndex = targets.findIndex((item) => item.providerModelTargetId === active.id);
    const newIndex = targets.findIndex((item) => item.providerModelTargetId === over.id);
    if (oldIndex < 0 || newIndex < 0) return;
    const next = arrayMove(targets, oldIndex, newIndex).map((item) => item.providerModelTargetId);
    void onReorder(next);
  };

  return (
    <div className="route-order-wrap">
      {saving && <span className="route-saving"><LoaderCircle className="spin" size={13} />正在保存顺序</span>}
      <div className="route-target-table-head" aria-hidden="true">
        <span>优先级</span><span>上游与 API Key</span><span>运行状态</span><span>响应速度</span><span>排序操作</span>
      </div>
      <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={dragEnd}>
        <SortableContext items={targets.map((item) => item.providerModelTargetId)} strategy={verticalListSortingStrategy}>
          <div className="route-target-list">
            {targets.map((target, index) => (
              <SortableTarget
                key={target.providerModelTargetId}
                target={target}
                position={index}
                catalog={catalogById.get(target.providerModelTargetId)}
                monitor={monitorById.get(target.providerModelTargetId)}
                slowFirstOutputThresholdMs={slowFirstOutputThresholdMs}
                disabled={saving || readOnly}
                total={targets.length}
                onMove={(direction) => move(index, direction)}
              />
            ))}
          </div>
        </SortableContext>
      </DndContext>
    </div>
  );
}
