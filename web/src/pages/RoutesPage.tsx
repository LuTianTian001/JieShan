import { DndContext, KeyboardSensor, PointerSensor, closestCenter, useSensor, useSensors, type DragEndEvent } from '@dnd-kit/core';
import { SortableContext, arrayMove, sortableKeyboardCoordinates, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import { Check, GripVertical, ListFilter, Network, Plus, RefreshCw, Save, SlidersHorizontal, TimerReset, Trash2 } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Badge, Button, Dialog, EmptyState, ErrorState, Field, LoadingState, PageHeader, StatusBadge, Surface, Switch } from '../components/ui';
import { useToast } from '../components/Toast';
import { api } from '../lib/api';
import { formatLatency, formatRelativeTime } from '../lib/format';
import { useAsyncData } from '../lib/hooks';
import type { CreateRouteInput, Route, RouteTarget, Upstream, UpdateRouteInput } from '../lib/types';

type TargetSelections = Record<number, string>;

function SortableTarget({ target, index }: { target: RouteTarget; index: number }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: target.id });
  return (
    <div ref={setNodeRef} className={`sortable-target ${isDragging ? 'is-dragging' : ''}`} style={{ transform: CSS.Transform.toString(transform), transition }}>
      <button type="button" className="drag-handle" aria-label={`拖动 ${target.upstreamName}`} {...attributes} {...listeners}><GripVertical size={17} /></button>
      <span className="route-rank">#{index + 1}</span>
      <div className="target-identity"><strong>{target.upstreamName}</strong><span>{target.credentialName}</span></div>
      <code className="source-model">{target.sourceModel}</code>
      <StatusBadge state={target.state} />
      <span className="route-latency">{formatLatency(target.latencyMs)}</span>
      <span className="route-cooldown">{target.cooldownUntil ? formatRelativeTime(target.cooldownUntil) : target.lastFailure || '可立即调用'}</span>
    </div>
  );
}

function UpstreamTargetPicker({ upstreams, selections, onChange }: { upstreams: Upstream[]; selections: TargetSelections; onChange: (upstreamId: number, modelId: string) => void }) {
  const available = upstreams.filter((upstream) => upstream.models?.some((model) => model.enabled));
  if (available.length === 0) return <EmptyState title="没有可用上游模型" description="先在上游页获取并应用模型，再创建路由。" />;
  return (
    <div className="route-target-picker">
      {available.map((upstream) => (
        <div className={selections[upstream.id] ? 'is-selected' : ''} key={upstream.id}>
          <div className="route-target-upstream"><strong>{upstream.name}</strong><code>{upstream.baseUrl}</code></div>
          <select className="select" aria-label={`${upstream.name} 的上游模型`} value={selections[upstream.id] || ''} onChange={(event) => onChange(upstream.id, event.target.value)}>
            <option value="">不参与此路由</option>
            {upstream.models?.filter((model) => model.enabled).map((model) => <option value={model.id} key={model.id}>{model.name}</option>)}
          </select>
          <StatusBadge state={upstream.enabled ? upstream.state : 'disabled'} compact />
        </div>
      ))}
    </div>
  );
}

function resolveSelections(upstreams: Upstream[], route?: Route): TargetSelections {
  const result: TargetSelections = {};
  if (!route) {
    for (const upstream of upstreams) {
      const first = upstream.enabled ? upstream.models?.find((model) => model.enabled) : undefined;
      if (first) result[upstream.id] = first.id;
    }
    return result;
  }
  for (const target of route.targets) {
    const model = upstreams.find((upstream) => upstream.id === target.upstreamId)?.models?.find((candidate) => candidate.enabled && candidate.name === target.sourceModel);
    if (model) result[target.upstreamId] = model.id;
  }
  return result;
}

export function RoutesPage() {
  const toast = useToast();
  const [searchParams] = useSearchParams();
  const state = useAsyncData(() => api.routes(), []);
  const upstreamState = useAsyncData(() => api.upstreams(), []);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [query, setQuery] = useState('');
  const [saving, setSaving] = useState(false);
  const [publishOpen, setPublishOpen] = useState(false);
  const [targetsOpen, setTargetsOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [publicModel, setPublicModel] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [monitored, setMonitored] = useState(true);
  const [targetSelections, setTargetSelections] = useState<TargetSelections>({});
  const [deletingRoute, setDeletingRoute] = useState<Route | null>(null);
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 6 } }), useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }));
  const routes = state.data ?? [];
  const upstreams = upstreamState.data ?? [];

  useEffect(() => {
    if (!routes.length || selectedId != null) return;
    const model = searchParams.get('model');
    setSelectedId(routes.find((route) => route.model === model)?.id ?? routes[0].id);
  }, [routes, searchParams, selectedId]);

  const filtered = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return routes.filter((route) => !normalized || `${route.model} ${route.displayName || ''}`.toLowerCase().includes(normalized));
  }, [query, routes]);
  const selected = routes.find((route) => route.id === selectedId) ?? null;
  const selectedModelIds = Object.values(targetSelections).filter(Boolean);

  const replaceRoute = (updated: Route) => state.setData((current) => current?.map((route) => route.id === updated.id ? updated : route) ?? [updated]);

  const dragEnd = async (event: DragEndEvent) => {
    if (!selected || !event.over || event.active.id === event.over.id) return;
    const oldIndex = selected.targets.findIndex((target) => target.id === event.active.id);
    const newIndex = selected.targets.findIndex((target) => target.id === event.over!.id);
    if (oldIndex < 0 || newIndex < 0) return;
    const previous = selected;
    const optimistic = { ...selected, targets: arrayMove(selected.targets, oldIndex, newIndex) };
    replaceRoute(optimistic);
    setSaving(true);
    try {
      replaceRoute(await api.reorderRoute(selected.id, optimistic.targets.map((target) => target.id)));
      toast.show('路由顺序已保存', 'success');
    } catch (reason) {
      replaceRoute(previous);
      toast.show(reason instanceof Error ? reason.message : '顺序保存失败，已恢复', 'error');
    } finally {
      setSaving(false);
    }
  };

  const updateRoute = async (route: Route, patch: UpdateRouteInput) => {
    const previous = route;
    replaceRoute({ ...route, ...patch });
    try {
      replaceRoute(await api.updateRoute(route.id, patch));
    } catch (reason) {
      replaceRoute(previous);
      toast.show(reason instanceof Error ? reason.message : '保存失败', 'error');
    }
  };

  const openPublish = () => {
    const selections = resolveSelections(upstreams);
    const firstId = Object.values(selections)[0];
    const firstModel = upstreams.flatMap((upstream) => upstream.models ?? []).find((model) => model.id === firstId)?.name || '';
    setPublicModel(firstModel);
    setDisplayName('');
    setMonitored(true);
    setTargetSelections(selections);
    setPublishOpen(true);
  };

  const createRoute = async () => {
    if (!publicModel.trim() || selectedModelIds.length === 0) return;
    const targets: CreateRouteInput['targets'] = upstreams.flatMap((upstream) => {
      const modelId = targetSelections[upstream.id];
      const sourceModel = upstream.models?.find((model) => model.id === modelId)?.name;
      return sourceModel ? [{ upstreamId: upstream.id, sourceModel }] : [];
    });
    setCreating(true);
    try {
      const created = await api.createRoute({ model: publicModel.trim(), displayName: displayName.trim() || undefined, monitored, targets });
      state.setData((current) => [created, ...(current ?? [])]);
      setSelectedId(created.id);
      setPublishOpen(false);
      toast.show(`${created.displayName || created.model} 已发布`, 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '发布模型失败', 'error');
    } finally {
      setCreating(false);
    }
  };

  const openTargetEditor = (route: Route) => {
    setTargetSelections(resolveSelections(upstreams, route));
    setTargetsOpen(true);
  };

  const saveTargets = async () => {
    if (!selected || selectedModelIds.length === 0) return;
    setCreating(true);
    try {
      const updated = await api.updateRoute(selected.id, { targetModelIds: upstreams.flatMap((upstream) => targetSelections[upstream.id] ? [Number(targetSelections[upstream.id])] : []) });
      replaceRoute(updated);
      setTargetsOpen(false);
      toast.show('路由目标已替换，可继续拖动调整顺序', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '路由目标保存失败', 'error');
    } finally {
      setCreating(false);
    }
  };

  const deleteRoute = async () => {
    if (!deletingRoute) return;
    setCreating(true);
    try {
      await api.deleteRoute(deletingRoute.id);
      state.setData((current) => current?.filter((route) => route.id !== deletingRoute.id) ?? []);
      setSelectedId(routes.find((route) => route.id !== deletingRoute.id)?.id ?? null);
      setDeletingRoute(null);
      toast.show('路由已删除', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '路由删除失败', 'error');
    } finally {
      setCreating(false);
    }
  };

  if (state.loading && !state.data) return <div className="page"><LoadingState label="正在读取模型路由" /></div>;
  if (state.error && !state.data) return <div className="page"><ErrorState message={state.error} onRetry={() => void state.refresh()} /></div>;

  return (
    <div className="page route-page">
      <PageHeader title="模型路由" description="每个上游可独立映射不同源模型，调用时严格按照拖动顺序依次尝试。" actions={<><Button icon={RefreshCw} busy={state.loading} onClick={() => void state.refresh()}>刷新路由</Button><Button variant="primary" icon={Plus} onClick={openPublish} disabled={!upstreams.some((upstream) => upstream.models?.some((model) => model.enabled))}>发布模型</Button></>} />
      <div className="route-workbench">
        <Surface className="route-index">
          <div className="toolbar"><div className="search-box"><ListFilter size={15} /><input className="input" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索模型" /></div></div>
          <div className="route-index-list">{filtered.map((route) => {
            const healthy = route.targets.filter((target) => target.state === 'healthy').length;
            return <button type="button" className={route.id === selectedId ? 'is-active' : ''} onClick={() => setSelectedId(route.id)} key={route.id}><span className="route-index-icon"><Network size={16} /></span><span><strong>{route.displayName || route.model}</strong><code>{route.model}</code></span><Badge tone={healthy ? 'success' : 'danger'}>{healthy}/{route.targets.length}</Badge></button>;
          })}</div>
        </Surface>

        <Surface className="route-editor">
          {!selected ? <EmptyState title="选择一个模型" description="从左侧选择模型后编辑严格路由顺序。" /> : <>
            <div className="route-editor-header">
              <div><div className="route-editor-title"><h2>{selected.displayName || selected.model}</h2><Badge tone={selected.enabled ? 'success' : 'neutral'}>{selected.enabled ? '已发布' : '已停用'}</Badge></div><code>{selected.model}</code></div>
              <div className="route-editor-actions"><Button size="sm" icon={SlidersHorizontal} onClick={() => openTargetEditor(selected)}>编辑目标</Button><Button size="sm" variant="ghost" icon={Trash2} onClick={() => setDeletingRoute(selected)}>删除</Button></div>
              <div className="route-editor-switches"><Switch checked={selected.monitored} onChange={(checked) => void updateRoute(selected, { monitored: checked })} label="自动监控" /><Switch checked={selected.enabled} onChange={(checked) => void updateRoute(selected, { enabled: checked })} label="允许调用" /></div>
            </div>
            <div className="route-order-note"><div><GripVertical size={16} /><span>拖动手柄调整顺序，#1 始终优先。这里没有权重。</span></div><span className={saving ? 'save-state is-saving' : 'save-state'}>{saving ? <><Save className="spin" size={14} />保存中</> : <><Check size={14} />已保存 · 修订 {selected.revision}</>}</span></div>
            <div className="sortable-target-header"><span /><span>顺序</span><span>上游</span><span>实际模型</span><span>状态</span><span>延迟</span><span>冷却 / 最近错误</span></div>
            {selected.targets.length === 0 ? <EmptyState title="路由没有目标" description="编辑目标并选择至少一个已应用的上游模型。" action={<Button icon={SlidersHorizontal} onClick={() => openTargetEditor(selected)}>编辑目标</Button>} /> : <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={(event) => void dragEnd(event)}>
              <SortableContext items={selected.targets.map((target) => target.id)} strategy={verticalListSortingStrategy}>
                <div className="sortable-target-list">{selected.targets.map((target, index) => <SortableTarget key={target.id} target={target} index={index} />)}</div>
              </SortableContext>
            </DndContext>}
            <div className="route-policy-strip"><div><TimerReset size={16} /><span>首次可重试失败：当前请求立即切换，目标进入观察</span></div><div><TimerReset size={16} /><span>连续失败达到阈值：进入冷却，恢复时仅放行一个半开请求</span></div></div>
          </>}
        </Surface>
      </div>

      <Dialog open={publishOpen} title="发布模型" description="公开模型名与每个上游的实际模型相互独立，可聚合异名模型。" onClose={() => setPublishOpen(false)} width="lg" footer={<><Button onClick={() => setPublishOpen(false)}>取消</Button><Button variant="primary" busy={creating} disabled={!publicModel.trim() || selectedModelIds.length === 0} onClick={() => void createRoute()}>发布模型</Button></>}>
        <div className="publish-route-form">
          <div className="form-grid">
            <Field label="公开模型名称"><input className="input" value={publicModel} onChange={(event) => setPublicModel(event.target.value)} placeholder="下游请求使用的 model" /></Field>
            <Field label="显示名称" hint="仅用于管理面板，可留空。"><input className="input" value={displayName} onChange={(event) => setDisplayName(event.target.value)} placeholder="例如 Claude Sonnet" /></Field>
            <div className="field-span-2 publish-monitor-switch"><Switch checked={monitored} onChange={setMonitored} label="发布后自动监控" /></div>
          </div>
          <div className="publish-target-section"><div className="publish-target-heading"><div><strong>上游模型映射</strong><span>每个上游独立选择实际 sourceModel；不参与的上游保持未选择。</span></div><Badge tone={selectedModelIds.length ? 'success' : 'warning'}>{selectedModelIds.length} 个目标</Badge></div><UpstreamTargetPicker upstreams={upstreams} selections={targetSelections} onChange={(upstreamId, modelId) => setTargetSelections((current) => ({ ...current, [upstreamId]: modelId }))} /></div>
        </div>
      </Dialog>

      <Dialog open={targetsOpen} title="编辑路由目标" description="选择、移除或替换上游模型。保存后会生成新的路由修订。" onClose={() => setTargetsOpen(false)} width="lg" footer={<><Button onClick={() => setTargetsOpen(false)}>取消</Button><Button variant="primary" busy={creating} disabled={selectedModelIds.length === 0} onClick={() => void saveTargets()}>保存目标</Button></>}>
        <UpstreamTargetPicker upstreams={upstreams} selections={targetSelections} onChange={(upstreamId, modelId) => setTargetSelections((current) => ({ ...current, [upstreamId]: modelId }))} />
      </Dialog>

      <Dialog open={Boolean(deletingRoute)} title="删除模型路由" description="删除后下游将无法再调用该公开模型。" onClose={() => setDeletingRoute(null)} width="sm" footer={<><Button onClick={() => setDeletingRoute(null)}>取消</Button><Button variant="danger" icon={Trash2} busy={creating} onClick={() => void deleteRoute()}>确认删除</Button></>}>
        {deletingRoute && <div className="delete-summary"><strong>{deletingRoute.displayName || deletingRoute.model}</strong><code>{deletingRoute.model}</code><span>{deletingRoute.targets.length} 个路由目标</span></div>}
      </Dialog>
    </div>
  );
}
