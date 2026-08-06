import { DndContext, KeyboardSensor, PointerSensor, closestCenter, useSensor, useSensors, type DragEndEvent } from '@dnd-kit/core';
import { SortableContext, arrayMove, sortableKeyboardCoordinates, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import { Check, GripVertical, Layers3, ListFilter, Network, Pencil, Plus, RefreshCw, RotateCcw, Save, SlidersHorizontal, Trash2 } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Badge, Button, Dialog, EmptyState, ErrorState, Field, LoadingState, PageHeader, Surface, Switch } from '../components/ui';
import { useToast } from '../components/Toast';
import { api } from '../lib/api';
import { useAsyncData } from '../lib/hooks';
import { inferenceProtocolLabel } from '../lib/inferenceProtocols';
import type { CreateV2PublishedModelInput, RoutingProfile, RoutingProfileModelRoute, UpdateV2PublishedModelInput, V2PublishedModel, V2RouteSiteTarget } from '../lib/types';
import type { UpstreamSiteView } from './upstreams/siteAdapter';
import { useUpstreamSites } from './upstreams/useUpstreamSites';

type PolicyDraft = Pick<V2PublishedModel,
  | 'monitorIntervalSeconds'
  | 'failureThreshold'
  | 'failureWindowSeconds'
  | 'cooldownSeconds'
  | 'firstOutputTimeoutSeconds'
  | 'streamIdleTimeoutSeconds'
  | 'requestDeadlineSeconds'
  | 'maxAttempts'
>;

interface TargetSelection {
  enabled: boolean;
  endpointId: number | null;
  siteModelId: number | null;
}

type TargetSelections = Record<number, TargetSelection>;

const defaultPolicy: PolicyDraft = {
  monitorIntervalSeconds: 300,
  failureThreshold: 2,
  failureWindowSeconds: 300,
  cooldownSeconds: 300,
  firstOutputTimeoutSeconds: 30,
  streamIdleTimeoutSeconds: 60,
  requestDeadlineSeconds: 120,
  maxAttempts: 3,
};

const policyFields: Array<{ field: keyof PolicyDraft; label: string; hint: string; unit: string; min: number; max: number; step?: number }> = [
  { field: 'monitorIntervalSeconds', label: '探针周期', hint: '仅监控已选模型', unit: '秒', min: 30, max: 86400, step: 30 },
  { field: 'failureThreshold', label: '失败阈值', hint: '窗口内达到后冷却', unit: '次', min: 2, max: 10 },
  { field: 'failureWindowSeconds', label: '失败窗口', hint: '累计失败的时间范围', unit: '秒', min: 1, max: 86400, step: 30 },
  { field: 'cooldownSeconds', label: '冷却时间', hint: '到期后重新参与路由', unit: '秒', min: 1, max: 86400, step: 30 },
  { field: 'firstOutputTimeoutSeconds', label: '首输出超时', hint: '未开始返回即切下一站', unit: '秒', min: 1, max: 600 },
  { field: 'streamIdleTimeoutSeconds', label: '流空闲超时', hint: '流中断后切换或终止', unit: '秒', min: 1, max: 3600 },
  { field: 'requestDeadlineSeconds', label: '请求总时限', hint: '整次聚合调用的上限', unit: '秒', min: 1, max: 3600 },
  { field: 'maxAttempts', label: '最多尝试站点', hint: '严格按排序依次尝试', unit: '站', min: 1, max: 20 },
];

function policyFrom(model: V2PublishedModel): PolicyDraft {
  return {
    monitorIntervalSeconds: model.monitorIntervalSeconds,
    failureThreshold: model.failureThreshold,
    failureWindowSeconds: model.failureWindowSeconds,
    cooldownSeconds: model.cooldownSeconds,
    firstOutputTimeoutSeconds: model.firstOutputTimeoutSeconds,
    streamIdleTimeoutSeconds: model.streamIdleTimeoutSeconds,
    requestDeadlineSeconds: model.requestDeadlineSeconds,
    maxAttempts: model.maxAttempts,
  };
}

function v2Sites(items: UpstreamSiteView[]): UpstreamSiteView[] {
  return items.filter((site) => site.sourceVersion === 'v2' && site.siteId != null);
}

function availableEndpoints(site: UpstreamSiteView, selectedId?: number | null) {
  return site.endpoints.filter((endpoint) => endpoint.endpointId != null
    && (endpoint.capabilities.routeEligible || endpoint.endpointId === selectedId)
    && (endpoint.enabled || endpoint.endpointId === selectedId));
}

function availableModels(site: UpstreamSiteView, endpointId: number | null, selectedId?: number | null) {
  return site.models.filter((model) => model.endpointId === endpointId && ((model.enabled && !model.stale) || Number(model.id) === selectedId));
}

function blankSelections(sites: UpstreamSiteView[]): TargetSelections {
  return Object.fromEntries(sites.flatMap((site) => site.siteId == null ? [] : [[site.siteId, { enabled: false, endpointId: null, siteModelId: null }]]));
}

function selectionsFrom(model: V2PublishedModel | null, sites: UpstreamSiteView[]): TargetSelections {
  const result = blankSelections(sites);
  if (!model) return result;
  for (const target of model.targets) {
    result[target.siteId] = { enabled: true, endpointId: target.endpointId, siteModelId: target.siteModelId };
  }
  return result;
}

function activeSelections(selections: TargetSelections) {
  return Object.entries(selections).flatMap(([siteId, selection]) => selection.enabled && selection.endpointId && selection.siteModelId
    ? [{ siteId: Number(siteId), endpointId: selection.endpointId, siteModelId: selection.siteModelId }]
    : []);
}

function siteHasRoutableModel(site: UpstreamSiteView): boolean {
  const endpointIds = new Set(site.endpoints.filter((endpoint) => endpoint.enabled && endpoint.capabilities.routeEligible).map((endpoint) => endpoint.endpointId));
  return site.models.some((model) => model.enabled && !model.stale && model.endpointId != null && endpointIds.has(model.endpointId));
}

function SortableTarget({ target, index, busy, onToggle }: { target: V2RouteSiteTarget; index: number; busy: boolean; onToggle: (target: V2RouteSiteTarget, enabled: boolean) => void }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: target.id });
  return (
    <div ref={setNodeRef} className={`sortable-target v2-route-target ${isDragging ? 'is-dragging' : ''}`} style={{ transform: CSS.Transform.toString(transform), transition }}>
      <button type="button" className="drag-handle" aria-label={`拖动 ${target.siteName}`} title="调整优先顺序" {...attributes} {...listeners}><GripVertical size={17} /></button>
      <span className="route-rank">#{index + 1}</span>
      <div className="target-identity"><strong>{target.siteName}</strong><span>网站级故障切换</span></div>
      <div className="target-endpoint"><strong>{target.endpointName}</strong><Badge tone={target.wireProtocol === 'anthropic' || target.wireProtocol === 'gemini' ? 'warning' : 'neutral'}>{inferenceProtocolLabel(target.wireProtocol)}</Badge></div>
      <code className="source-model">{target.sourceModel}</code>
      <Switch checked={target.enabled} disabled={busy} showLabel={false} label={`${target.siteName} 参与路由`} onChange={(enabled) => onToggle(target, enabled)} />
    </div>
  );
}

function SortableProfileTarget({ target, index, busy, canRemove, onRemove }: { target: V2RouteSiteTarget; index: number; busy: boolean; canRemove: boolean; onRemove: (target: V2RouteSiteTarget) => void }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: target.id });
  return (
    <div ref={setNodeRef} className={`sortable-target v2-route-target profile-route-target ${isDragging ? 'is-dragging' : ''}`} style={{ transform: CSS.Transform.toString(transform), transition }}>
      <button type="button" className="drag-handle" aria-label={`拖动 ${target.siteName}`} title="调整方案顺序" {...attributes} {...listeners}><GripVertical size={17} /></button>
      <span className="route-rank">#{index + 1}</span>
      <div className="target-identity"><strong>{target.siteName}</strong><span>{target.enabled ? '参与默认候选' : '默认目标已停用'}</span></div>
      <div className="target-endpoint"><strong>{target.endpointName}</strong><Badge tone={target.wireProtocol === 'anthropic' || target.wireProtocol === 'gemini' ? 'warning' : 'neutral'}>{inferenceProtocolLabel(target.wireProtocol)}</Badge></div>
      <code className="source-model">{target.sourceModel}</code>
      <Button size="sm" variant="ghost" icon={Trash2} disabled={busy || !canRemove} title={canRemove ? '从当前方案移除' : '方案至少保留一个网站'} onClick={() => onRemove(target)}>移除</Button>
    </div>
  );
}

function TargetPicker({ sites, selections, onChange }: { sites: UpstreamSiteView[]; selections: TargetSelections; onChange: (siteId: number, next: TargetSelection) => void }) {
  if (sites.length === 0) return <EmptyState title="还没有可路由网站" description="先添加网站、Endpoint 和 API Key，再获取该 Endpoint 的模型列表。" />;
  return (
    <div className="v2-target-picker">
      <div className="v2-target-picker-head"><span>网站</span><span>Endpoint</span><span>实际模型</span><span>参与</span></div>
      {sites.map((site) => {
        const siteId = site.siteId!;
        const selection = selections[siteId] ?? { enabled: false, endpointId: null, siteModelId: null };
        const endpoints = availableEndpoints(site, selection.endpointId);
        const models = availableModels(site, selection.endpointId, selection.siteModelId);
        const canEnable = site.enabled && endpoints.some((endpoint) => endpoint.capabilities.routeEligible && availableModels(site, endpoint.endpointId).length > 0);
        const toggle = (enabled: boolean) => {
          if (!enabled) {
            onChange(siteId, { ...selection, enabled: false });
            return;
          }
          const endpoint = endpoints.find((item) => item.capabilities.routeEligible && item.enabled && availableModels(site, item.endpointId).length > 0)
            ?? endpoints.find((item) => item.capabilities.routeEligible && availableModels(site, item.endpointId).length > 0)
            ?? null;
          const model = endpoint ? availableModels(site, endpoint.endpointId)[0] : null;
          onChange(siteId, { enabled: true, endpointId: endpoint?.endpointId ?? null, siteModelId: model ? Number(model.id) : null });
        };
        return (
          <div className={`v2-target-picker-row ${selection.enabled ? 'is-selected' : ''}`} key={siteId}>
            <div className="route-target-upstream"><strong>{site.name}</strong><code>{site.origin || site.dashboardUrl || '未设置站点地址'}</code></div>
            <select className="select" disabled={!selection.enabled} value={selection.endpointId ?? ''} aria-label={`${site.name} Endpoint`} onChange={(event) => {
              const endpointId = Number(event.target.value) || null;
              const firstModel = availableModels(site, endpointId)[0];
              onChange(siteId, { ...selection, endpointId, siteModelId: firstModel ? Number(firstModel.id) : null });
            }}>
              <option value="">选择 Endpoint</option>
              {endpoints.map((endpoint) => <option key={endpoint.id} value={endpoint.endpointId!} disabled={!endpoint.capabilities.routeEligible}>{endpoint.name} · {inferenceProtocolLabel(endpoint.protocol)}{endpoint.capabilities.routeEligible ? '' : '（仅获取模型）'}{endpoint.enabled ? '' : '（已停用）'}</option>)}
            </select>
            <select className="select" disabled={!selection.enabled || !selection.endpointId} value={selection.siteModelId ?? ''} aria-label={`${site.name} 实际模型`} onChange={(event) => onChange(siteId, { ...selection, siteModelId: Number(event.target.value) || null })}>
              <option value="">选择站点模型</option>
              {models.map((model) => <option key={model.id} value={model.id}>{model.displayName || model.name}{model.stale ? '（已过期）' : ''}</option>)}
            </select>
            <Switch checked={selection.enabled} disabled={!selection.enabled && !canEnable} showLabel={false} label={`${site.name} 参与路由`} onChange={toggle} />
          </div>
        );
      })}
    </div>
  );
}

export function RoutesPage() {
  const toast = useToast();
  const [searchParams] = useSearchParams();
  const state = useAsyncData(() => api.v2PublishedModels(), []);
  const profilesState = useAsyncData(() => api.routingProfiles(), []);
  const siteState = useUpstreamSites();
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [query, setQuery] = useState('');
  const [saving, setSaving] = useState(false);
  const [busyTargets, setBusyTargets] = useState<Set<number>>(new Set());
  const [publishOpen, setPublishOpen] = useState(false);
  const [metadataOpen, setMetadataOpen] = useState(false);
  const [targetsOpen, setTargetsOpen] = useState(false);
  const [deletingModel, setDeletingModel] = useState<V2PublishedModel | null>(null);
  const [publicName, setPublicName] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [officialPriceSku, setOfficialPriceSku] = useState('');
  const [monitorEnabled, setMonitorEnabled] = useState(true);
  const [targetSelections, setTargetSelections] = useState<TargetSelections>({});
  const [policy, setPolicy] = useState<PolicyDraft>(defaultPolicy);
  const [selectedProfileId, setSelectedProfileId] = useState<number | null>(null);
  const [profileRoute, setProfileRoute] = useState<RoutingProfileModelRoute | null>(null);
  const [profileBusy, setProfileBusy] = useState(false);
  const [profileDialog, setProfileDialog] = useState<'create' | 'rename' | null>(null);
  const [profileName, setProfileName] = useState('');
  const [deletingProfile, setDeletingProfile] = useState<RoutingProfile | null>(null);
  const [addProfileTargetId, setAddProfileTargetId] = useState<number | null>(null);
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 6 } }), useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }));
  const models = state.data ?? [];
  const profiles = profilesState.data ?? [];
  const sites = useMemo(() => v2Sites(siteState.data ?? []), [siteState.data]);

  useEffect(() => {
    if (!models.length) {
      setSelectedId(null);
      return;
    }
    if (selectedId != null && models.some((model) => model.id === selectedId)) return;
    const requested = searchParams.get('model');
    setSelectedId(models.find((model) => model.publicName === requested)?.id ?? models[0].id);
  }, [models, searchParams, selectedId]);

  const selected = models.find((model) => model.id === selectedId) ?? null;
  const selectedProfile = profiles.find((profile) => profile.id === selectedProfileId) ?? null;

  useEffect(() => {
    if (selected) setPolicy(policyFrom(selected));
  }, [selected]);

  useEffect(() => {
    if (!profiles.length) {
      setSelectedProfileId(null);
      return;
    }
    if (selectedProfileId != null && profiles.some((profile) => profile.id === selectedProfileId)) return;
    setSelectedProfileId(profiles[0].id);
  }, [profiles, selectedProfileId]);

  useEffect(() => {
    let cancelled = false;
    if (!selected || !selectedProfile) {
      setProfileRoute(null);
      return () => { cancelled = true; };
    }
    setProfileBusy(true);
    api.routingProfileModel(selectedProfile.id, selected.id)
      .then((route) => {
        if (!cancelled) {
          setProfileRoute(route);
          setAddProfileTargetId(null);
        }
      })
      .catch((reason) => {
        if (!cancelled) {
          setProfileRoute(null);
          toast.show(reason instanceof Error ? reason.message : '读取路由方案失败', 'error');
        }
      })
      .finally(() => {
        if (!cancelled) setProfileBusy(false);
      });
    return () => { cancelled = true; };
  }, [selected?.id, selected?.revision, selectedProfile?.id]);

  const filtered = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return models.filter((model) => !normalized || `${model.publicName} ${model.displayName ?? ''}`.toLowerCase().includes(normalized));
  }, [models, query]);

  const replaceModel = (updated: V2PublishedModel) => {
    state.setData((current) => current?.map((model) => model.id === updated.id ? updated : model) ?? [updated]);
  };

  const refreshModel = async (id: number) => {
    const updated = await api.v2PublishedModel(id);
    replaceModel(updated);
    return updated;
  };

  const updateModel = async (model: V2PublishedModel, patch: UpdateV2PublishedModelInput, successMessage?: string) => {
    setSaving(true);
    try {
      const updated = await api.updateV2PublishedModel(model.id, { ...patch, revision: model.revision });
      replaceModel(updated);
      if (successMessage) toast.show(successMessage, 'success');
      return updated;
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '保存失败', 'error');
      return null;
    } finally {
      setSaving(false);
    }
  };

  const dragEnd = async (event: DragEndEvent) => {
    if (!selected || !event.over || event.active.id === event.over.id) return;
    const oldIndex = selected.targets.findIndex((target) => target.id === event.active.id);
    const newIndex = selected.targets.findIndex((target) => target.id === event.over!.id);
    if (oldIndex < 0 || newIndex < 0) return;
    const previous = selected;
    const ordered = arrayMove(selected.targets, oldIndex, newIndex).map((target, position) => ({ ...target, position }));
    replaceModel({ ...selected, targets: ordered });
    setSaving(true);
    try {
      replaceModel(await api.reorderV2RouteTargets(selected.id, ordered.map((target) => target.id), selected.revision));
      toast.show('网站优先顺序已保存', 'success');
    } catch (reason) {
      replaceModel(previous);
      toast.show(reason instanceof Error ? reason.message : '顺序保存失败，已恢复', 'error');
    } finally {
      setSaving(false);
    }
  };

  const applyProfileRoute = (updated: RoutingProfileModelRoute, previousInherited: boolean) => {
    setProfileRoute(updated);
    profilesState.setData((current) => current?.map((profile) => profile.id === updated.routingProfileId ? {
      ...profile,
      revision: updated.profileRevision,
      modelOverrideCount: Math.max(0, profile.modelOverrideCount + (previousInherited === updated.inheritsDefault ? 0 : updated.inheritsDefault ? -1 : 1)),
    } : profile) ?? []);
  };

  const saveProfileTargets = async (targetIds: number[], successMessage: string) => {
    if (!selected || !selectedProfile || !profileRoute || targetIds.length === 0) return;
    const previous = profileRoute;
    setProfileBusy(true);
    try {
      const updated = await api.setRoutingProfileModel(selectedProfile.id, selected.id, targetIds, profileRoute.profileRevision);
      applyProfileRoute(updated, previous.inheritsDefault);
      setAddProfileTargetId(null);
      toast.show(successMessage, 'success');
    } catch (reason) {
      setProfileRoute(previous);
      toast.show(reason instanceof Error ? reason.message : '路由方案保存失败', 'error');
    } finally {
      setProfileBusy(false);
    }
  };

  const profileDragEnd = async (event: DragEndEvent) => {
    if (!profileRoute || !event.over || event.active.id === event.over.id) return;
    const oldIndex = profileRoute.targets.findIndex((target) => target.id === event.active.id);
    const newIndex = profileRoute.targets.findIndex((target) => target.id === event.over!.id);
    if (oldIndex < 0 || newIndex < 0) return;
    const ordered = arrayMove(profileRoute.targets, oldIndex, newIndex).map((target, position) => ({ ...target, position }));
    setProfileRoute({ ...profileRoute, targets: ordered });
    await saveProfileTargets(ordered.map((target) => target.id), '方案优先顺序已保存');
  };

  const removeProfileTarget = async (target: V2RouteSiteTarget) => {
    if (!profileRoute || profileRoute.targets.length <= 1) return;
    await saveProfileTargets(profileRoute.targets.filter((item) => item.id !== target.id).map((item) => item.id), `${target.siteName} 已从方案移除`);
  };

  const addProfileTarget = async () => {
    if (!profileRoute || !addProfileTargetId) return;
    await saveProfileTargets([...profileRoute.targets.map((target) => target.id), addProfileTargetId], '网站已加入方案末尾');
  };

  const clearProfileOverride = async () => {
    if (!selected || !selectedProfile || !profileRoute || profileRoute.inheritsDefault) return;
    const previous = profileRoute;
    setProfileBusy(true);
    try {
      const updated = await api.clearRoutingProfileModel(selectedProfile.id, selected.id, profileRoute.profileRevision);
      applyProfileRoute(updated, previous.inheritsDefault);
      toast.show('当前模型已恢复继承默认路由', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '恢复默认路由失败', 'error');
    } finally {
      setProfileBusy(false);
    }
  };

  const openCreateProfile = () => {
    setProfileName('');
    setProfileDialog('create');
  };

  const openRenameProfile = () => {
    if (!selectedProfile) return;
    setProfileName(selectedProfile.name);
    setProfileDialog('rename');
  };

  const saveProfile = async () => {
    const name = profileName.trim();
    if (!name) return;
    setProfileBusy(true);
    try {
      if (profileDialog === 'create') {
        const created = await api.createRoutingProfile(name);
        profilesState.setData((current) => [...(current ?? []), created].sort((left, right) => left.name.localeCompare(right.name)));
        setSelectedProfileId(created.id);
        toast.show('路由方案已创建', 'success');
      } else if (profileDialog === 'rename' && selectedProfile) {
        const updated = await api.updateRoutingProfile(selectedProfile.id, name, selectedProfile.revision);
        profilesState.setData((current) => current?.map((profile) => profile.id === updated.id ? updated : profile) ?? [updated]);
        setProfileRoute((current) => current && current.routingProfileId === updated.id ? { ...current, profileRevision: updated.revision } : current);
        toast.show('路由方案已重命名', 'success');
      }
      setProfileDialog(null);
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '路由方案保存失败', 'error');
    } finally {
      setProfileBusy(false);
    }
  };

  const deleteProfile = async () => {
    if (!deletingProfile) return;
    setProfileBusy(true);
    try {
      await api.deleteRoutingProfile(deletingProfile.id, deletingProfile.revision);
      profilesState.setData((current) => current?.filter((profile) => profile.id !== deletingProfile.id) ?? []);
      setSelectedProfileId(null);
      setDeletingProfile(null);
      toast.show('路由方案已删除，绑定密钥已回到默认路由', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '删除路由方案失败', 'error');
    } finally {
      setProfileBusy(false);
    }
  };

  const toggleTarget = async (target: V2RouteSiteTarget, enabled: boolean) => {
    if (!selected) return;
    setBusyTargets((current) => new Set(current).add(target.id));
    try {
      await api.updateV2RouteTarget(target.id, {
        siteId: target.siteId,
        endpointId: target.endpointId,
        siteModelId: target.siteModelId,
        enabled,
        revision: target.revision,
      });
      await refreshModel(selected.id);
      toast.show(enabled ? `${target.siteName} 已恢复路由` : `${target.siteName} 已暂停路由`, 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '目标状态保存失败', 'error');
    } finally {
      setBusyTargets((current) => {
        const next = new Set(current);
        next.delete(target.id);
        return next;
      });
    }
  };

  const openPublish = () => {
    setPublicName('');
    setDisplayName('');
    setOfficialPriceSku('');
    setMonitorEnabled(true);
    setTargetSelections(blankSelections(sites));
    setPublishOpen(true);
  };

  const openMetadata = (model: V2PublishedModel) => {
    setPublicName(model.publicName);
    setDisplayName(model.displayName ?? '');
    setOfficialPriceSku(model.officialPriceSku ?? '');
    setMetadataOpen(true);
  };

  const autoMatch = () => {
    const wanted = publicName.trim().toLowerCase();
    if (!wanted) return;
    setTargetSelections((current) => {
      const next = { ...current };
      for (const site of sites) {
        if (!site.siteId || !site.enabled) continue;
        const routableEndpointIDs = new Set(site.endpoints.filter((endpoint) => endpoint.enabled && endpoint.capabilities.routeEligible).map((endpoint) => endpoint.endpointId));
        const model = site.models.find((item) => item.enabled && !item.stale && item.endpointId != null && routableEndpointIDs.has(item.endpointId) && item.name.toLowerCase() === wanted);
        if (model?.endpointId) next[site.siteId] = { enabled: true, endpointId: model.endpointId, siteModelId: Number(model.id) };
      }
      return next;
    });
  };

  const createModel = async () => {
    const targets = activeSelections(targetSelections);
    if (!publicName.trim() || targets.length === 0) return;
    setSaving(true);
    let created: V2PublishedModel | null = null;
    try {
      const input: CreateV2PublishedModelInput = {
        publicName: publicName.trim(),
        displayName: displayName.trim() || undefined,
        officialPriceSku: officialPriceSku.trim() || undefined,
        enabled: true,
        monitorEnabled,
        ...defaultPolicy,
      };
      created = await api.createV2PublishedModel(input);
      for (const target of targets) await api.createV2RouteTarget(created.id, target);
      const complete = await api.v2PublishedModel(created.id);
      state.setData((current) => [complete, ...(current ?? [])]);
      setSelectedId(complete.id);
      setPublishOpen(false);
      toast.show(`${complete.displayName || complete.publicName} 已发布`, 'success');
    } catch (reason) {
      if (created) await api.deleteV2PublishedModel(created.id).catch(() => undefined);
      toast.show(reason instanceof Error ? reason.message : '发布模型失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  const saveMetadata = async () => {
    if (!selected || !publicName.trim()) return;
    const updated = await updateModel(selected, {
      publicName: publicName.trim(),
      displayName: displayName.trim() || undefined,
      officialPriceSku: officialPriceSku.trim() || undefined,
    }, '模型信息已保存');
    if (updated) setMetadataOpen(false);
  };

  const openTargets = (model: V2PublishedModel) => {
    setTargetSelections(selectionsFrom(model, sites));
    setTargetsOpen(true);
  };

  const saveTargets = async () => {
    if (!selected) return;
    const wanted = new Map(activeSelections(targetSelections).map((item) => [item.siteId, item]));
    const existing = new Map(selected.targets.map((target) => [target.siteId, target]));
    if (wanted.size === 0) return;
    setSaving(true);
    try {
      for (const target of selected.targets) {
        if (!wanted.has(target.siteId)) await api.deleteV2RouteTarget(target.id, target.revision);
      }
      for (const [siteId, choice] of wanted) {
        const target = existing.get(siteId);
        if (!target) {
          await api.createV2RouteTarget(selected.id, choice);
        } else if (target.endpointId !== choice.endpointId || target.siteModelId !== choice.siteModelId || !target.enabled) {
          await api.updateV2RouteTarget(target.id, { ...choice, enabled: true, revision: target.revision });
        }
      }
      await refreshModel(selected.id);
      setTargetsOpen(false);
      toast.show('路由网站已保存，可继续拖动调整顺序', 'success');
    } catch (reason) {
      await refreshModel(selected.id).catch(() => undefined);
      toast.show(reason instanceof Error ? reason.message : '路由网站保存失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  const savePolicy = async () => {
    if (!selected) return;
    if (policy.requestDeadlineSeconds < Math.max(policy.firstOutputTimeoutSeconds, policy.streamIdleTimeoutSeconds)) {
      toast.show('请求总时限不能短于首输出或流空闲超时', 'error');
      return;
    }
    await updateModel(selected, policy, '模型策略已保存');
  };

  const deleteModel = async () => {
    if (!deletingModel) return;
    setSaving(true);
    try {
      await api.deleteV2PublishedModel(deletingModel.id, deletingModel.revision);
      state.setData((current) => current?.filter((model) => model.id !== deletingModel.id) ?? []);
      setSelectedId(models.find((model) => model.id !== deletingModel.id)?.id ?? null);
      setDeletingModel(null);
      toast.show('发布模型已删除', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '删除失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  if (state.loading && !state.data) return <div className="page"><LoadingState label="正在读取发布模型" /></div>;
  if (state.error && !state.data) return <div className="page"><ErrorState message={state.error} onRetry={() => void state.refresh()} /></div>;

  const selectedCount = activeSelections(targetSelections).length;
  const profileTargetIDs = new Set(profileRoute?.targets.map((target) => target.id) ?? []);
  const profileTargetCandidates = selected?.targets.filter((target) => !profileTargetIDs.has(target.id)) ?? [];

  return (
    <div className="page route-page v2-route-page">
      <PageHeader title="模型路由" description="每个网站只占一个优先位，调用严格按照拖动顺序依次切换。" actions={<><Button icon={RefreshCw} busy={state.loading || siteState.loading || profilesState.loading} onClick={() => { void state.refresh(); void siteState.refresh(); void profilesState.refresh(); }}>刷新</Button><Button variant="primary" icon={Plus} onClick={openPublish} disabled={!sites.some(siteHasRoutableModel)}>发布模型</Button></>} />
      <Surface className="routing-profile-manager">
        <div className="routing-profile-toolbar">
          <div className="routing-profile-heading"><span><Layers3 size={17} /></span><div><strong>下游路由方案</strong><small>密钥可绑定命名方案；未覆盖当前模型时自动继承默认路由。</small></div></div>
          <div className="routing-profile-controls">
            <select className="select" value={selectedProfileId ?? ''} disabled={profilesState.loading} onChange={(event) => setSelectedProfileId(event.target.value ? Number(event.target.value) : null)}>
              <option value="">默认路由</option>
              {profiles.map((profile) => <option value={profile.id} key={profile.id}>{profile.name} · {profile.modelOverrideCount} 个覆盖</option>)}
            </select>
            <Button size="sm" icon={Plus} onClick={openCreateProfile}>新建</Button>
            <Button size="sm" variant="ghost" icon={Pencil} disabled={!selectedProfile} onClick={openRenameProfile}>重命名</Button>
            <Button size="sm" variant="ghost" icon={Trash2} disabled={!selectedProfile} onClick={() => setDeletingProfile(selectedProfile)}>删除</Button>
          </div>
        </div>
        {!selectedProfile ? <div className="routing-profile-placeholder"><Network size={17} /><div><strong>当前是默认路由</strong><span>下面的模型顺序就是所有未绑定方案、或方案未覆盖该模型时使用的顺序。</span></div></div> : !selected ? <div className="routing-profile-placeholder"><Layers3 size={17} /><div><strong>先选择一个发布模型</strong><span>方案覆盖按模型单独保存，不会改变监控候选集合。</span></div></div> : profileBusy && !profileRoute ? <LoadingState label="正在读取方案覆盖" /> : profileRoute ? <>
          <div className="routing-profile-status">
            <div><Badge tone={profileRoute.inheritsDefault ? 'neutral' : 'accent'}>{profileRoute.inheritsDefault ? '继承默认' : '自定义覆盖'}</Badge><span>{selectedProfile.name} · {selected.displayName || selected.publicName}</span></div>
            <div><small>方案修订 {profileRoute.profileRevision}</small>{!profileRoute.inheritsDefault && <Button size="sm" variant="ghost" icon={RotateCcw} busy={profileBusy} onClick={() => void clearProfileOverride()}>恢复默认</Button>}</div>
          </div>
          <div className="sortable-target-header v2-route-target-header profile-route-target-header"><span /><span>顺序</span><span>网站</span><span>Endpoint / 协议</span><span>实际模型</span><span>方案操作</span></div>
          {profileRoute.targets.length === 0 ? <EmptyState title="默认路由没有网站" description="先在下面为当前发布模型配置默认路由网站。" /> : <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={(event) => void profileDragEnd(event)}>
            <SortableContext items={profileRoute.targets.map((target) => target.id)} strategy={verticalListSortingStrategy}>
              <div className="sortable-target-list">{profileRoute.targets.map((target, index) => <SortableProfileTarget key={target.id} target={target} index={index} busy={profileBusy} canRemove={profileRoute.targets.length > 1} onRemove={(item) => void removeProfileTarget(item)} />)}</div>
            </SortableContext>
          </DndContext>}
          {!profileRoute.inheritsDefault && profileTargetCandidates.length > 0 && <div className="routing-profile-add-target"><select className="select" value={addProfileTargetId ?? ''} onChange={(event) => setAddProfileTargetId(event.target.value ? Number(event.target.value) : null)}><option value="">添加默认候选网站</option>{profileTargetCandidates.map((target) => <option value={target.id} key={target.id}>{target.siteName} · {target.sourceModel}</option>)}</select><Button size="sm" icon={Plus} disabled={!addProfileTargetId || profileBusy} onClick={() => void addProfileTarget()}>加入末尾</Button></div>}
          <div className="routing-profile-footnote"><span>拖动或移除会为当前模型创建覆盖；健康状态与自动探针仍复用默认候选，不会复制监控数据。</span></div>
        </> : <ErrorState message="无法读取当前模型的方案覆盖" onRetry={() => { if (selectedProfile && selected) void api.routingProfileModel(selectedProfile.id, selected.id).then(setProfileRoute); }} />}
      </Surface>
      <div className="route-workbench">
        <Surface className="route-index">
          <div className="toolbar"><div className="search-box"><ListFilter size={15} /><input className="input" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索模型" /></div></div>
          <div className="route-index-list">
            {filtered.map((model) => <button type="button" className={model.id === selectedId ? 'is-active' : ''} onClick={() => setSelectedId(model.id)} key={model.id}><span className="route-index-icon"><Network size={16} /></span><span><strong>{model.displayName || model.publicName}</strong><code>{model.publicName}</code></span><Badge tone={model.enabled && model.targets.some((target) => target.enabled) ? 'success' : 'neutral'}>{model.targets.filter((target) => target.enabled).length}/{model.targets.length}</Badge></button>)}
            {filtered.length === 0 && <EmptyState title="没有匹配模型" description="调整搜索词，或发布一个新的下游模型。" />}
          </div>
        </Surface>

        <Surface className="route-editor">
          {!selected ? <EmptyState title="还没有发布模型" description="选择站点模型并发布后，可在这里配置严格故障切换顺序。" action={<Button icon={Plus} onClick={openPublish} disabled={sites.length === 0}>发布模型</Button>} /> : <>
            <div className="route-editor-header">
              <div><div className="route-editor-title"><h2>{selected.displayName || selected.publicName}</h2><Badge tone={selected.enabled ? 'success' : 'neutral'}>{selected.enabled ? '可调用' : '已停用'}</Badge></div><code>{selected.publicName}</code></div>
              <div className="route-editor-actions"><Button size="sm" icon={Pencil} onClick={() => openMetadata(selected)}>模型信息</Button><Button size="sm" icon={SlidersHorizontal} onClick={() => openTargets(selected)}>路由网站</Button><Button size="sm" variant="ghost" icon={Trash2} onClick={() => setDeletingModel(selected)}>删除</Button></div>
              <div className="route-editor-switches"><Switch checked={selected.monitorEnabled} disabled={saving} onChange={(enabled) => void updateModel(selected, { monitorEnabled: enabled })} label="自动监控" /><Switch checked={selected.enabled} disabled={saving} onChange={(enabled) => void updateModel(selected, { enabled })} label="允许调用" /></div>
            </div>

            <div className="route-order-note"><div><GripVertical size={16} /><span>严格顺序模式：上一站失败或超时后才尝试下一站。</span></div><span className={saving ? 'save-state is-saving' : 'save-state'}>{saving ? <><Save className="spin" size={14} />保存中</> : <><Check size={14} />修订 {selected.revision}</>}</span></div>
            <div className="sortable-target-header v2-route-target-header"><span /><span>顺序</span><span>网站</span><span>Endpoint / 协议</span><span>实际模型</span><span>参与</span></div>
            {selected.targets.length === 0 ? <EmptyState title="没有路由网站" description="至少选择一个已获取模型的网站。" action={<Button icon={SlidersHorizontal} onClick={() => openTargets(selected)}>选择网站</Button>} /> : <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={(event) => void dragEnd(event)}>
              <SortableContext items={selected.targets.map((target) => target.id)} strategy={verticalListSortingStrategy}>
                <div className="sortable-target-list">{selected.targets.map((target, index) => <SortableTarget key={target.id} target={target} index={index} busy={busyTargets.has(target.id)} onToggle={(item, enabled) => void toggleTarget(item, enabled)} />)}</div>
              </SortableContext>
            </DndContext>}

            <div className="route-policy-heading"><div><strong>模型级故障策略</strong><span>只影响当前发布模型，可覆盖系统默认值。</span></div><Button size="sm" icon={Save} busy={saving} onClick={() => void savePolicy()}>保存策略</Button></div>
            <div className="route-policy-editor">
              {policyFields.map((item) => <Field key={item.field} label={item.label} hint={item.hint}><div className="number-with-unit"><input className="input" type="number" min={item.min} max={item.max} step={item.step ?? 1} value={policy[item.field]} onChange={(event) => {
                const value = Number(event.target.value);
                if (Number.isFinite(value)) setPolicy((current) => ({ ...current, [item.field]: value }));
              }} /><span>{item.unit}</span></div></Field>)}
            </div>
          </>}
        </Surface>
      </div>

      <Dialog open={publishOpen} title="发布模型" description="下游模型名与各网站的实际模型相互独立。" onClose={() => setPublishOpen(false)} width="lg" footer={<><Button onClick={() => setPublishOpen(false)}>取消</Button><Button variant="primary" busy={saving} disabled={!publicName.trim() || selectedCount === 0} onClick={() => void createModel()}>发布模型</Button></>}>
        <div className="publish-route-form">
          <div className="form-grid">
            <Field label="下游模型名" hint="客户端请求中的 model"><input className="input" value={publicName} onChange={(event) => setPublicName(event.target.value)} placeholder="例如 claude-sonnet-4-5" /></Field>
            <Field label="显示名称" hint="仅用于管理界面"><input className="input" value={displayName} onChange={(event) => setDisplayName(event.target.value)} placeholder="例如 Claude Sonnet" /></Field>
            <Field label="官方计价 SKU" hint="留空时按下游模型名匹配"><input className="input" value={officialPriceSku} onChange={(event) => setOfficialPriceSku(event.target.value)} placeholder="官方价格目录中的模型名" /></Field>
            <div className="publish-monitor-switch"><Switch checked={monitorEnabled} onChange={setMonitorEnabled} label="发布后自动监控" /></div>
          </div>
          <div className="publish-target-heading"><div><strong>网站映射</strong><span>每个网站最多选择一个 Endpoint 和一个实际模型。</span></div><div><Badge tone={selectedCount ? 'success' : 'warning'}>{selectedCount} 个网站</Badge><Button size="sm" variant="ghost" onClick={autoMatch} disabled={!publicName.trim()}>匹配同名模型</Button></div></div>
          <TargetPicker sites={sites} selections={targetSelections} onChange={(siteId, next) => setTargetSelections((current) => ({ ...current, [siteId]: next }))} />
        </div>
      </Dialog>

      <Dialog open={metadataOpen} title="模型信息" onClose={() => setMetadataOpen(false)} footer={<><Button onClick={() => setMetadataOpen(false)}>取消</Button><Button variant="primary" busy={saving} disabled={!publicName.trim()} onClick={() => void saveMetadata()}>保存</Button></>}>
        <div className="form-grid single-column"><Field label="下游模型名"><input className="input" value={publicName} onChange={(event) => setPublicName(event.target.value)} /></Field><Field label="显示名称"><input className="input" value={displayName} onChange={(event) => setDisplayName(event.target.value)} /></Field><Field label="官方计价 SKU"><input className="input" value={officialPriceSku} onChange={(event) => setOfficialPriceSku(event.target.value)} /></Field></div>
      </Dialog>

      <Dialog open={targetsOpen} title="路由网站" description="保留的网站会继续按照当前顺序排列，新网站追加到最后。" onClose={() => setTargetsOpen(false)} width="lg" footer={<><Button onClick={() => setTargetsOpen(false)}>取消</Button><Button variant="primary" busy={saving} disabled={selectedCount === 0} onClick={() => void saveTargets()}>保存网站</Button></>}>
        <TargetPicker sites={sites} selections={targetSelections} onChange={(siteId, next) => setTargetSelections((current) => ({ ...current, [siteId]: next }))} />
      </Dialog>

      <Dialog open={Boolean(deletingModel)} title="删除发布模型" description={`将删除 ${deletingModel?.displayName || deletingModel?.publicName || ''} 及其全部网站映射。`} onClose={() => setDeletingModel(null)} footer={<><Button onClick={() => setDeletingModel(null)}>取消</Button><Button variant="danger" icon={Trash2} busy={saving} onClick={() => void deleteModel()}>确认删除</Button></>}><p className="dialog-warning">下游将立即无法再调用这个模型。</p></Dialog>
      <Dialog open={profileDialog != null} title={profileDialog === 'rename' ? '重命名路由方案' : '新建路由方案'} description="方案只保存默认候选的子集与顺序，未配置的模型自动回退默认路由。" onClose={() => setProfileDialog(null)} width="sm" footer={<><Button onClick={() => setProfileDialog(null)}>取消</Button><Button variant="primary" busy={profileBusy} disabled={!profileName.trim()} onClick={() => void saveProfile()}>保存</Button></>}><Field label="方案名称"><input className="input" autoFocus value={profileName} onChange={(event) => setProfileName(event.target.value)} placeholder="例如：个人低延迟" /></Field></Dialog>
      <Dialog open={Boolean(deletingProfile)} title="删除路由方案" description={`删除 ${deletingProfile?.name || ''} 后，绑定它的下游密钥会自动恢复默认路由。`} onClose={() => setDeletingProfile(null)} width="sm" footer={<><Button onClick={() => setDeletingProfile(null)}>取消</Button><Button variant="danger" icon={Trash2} busy={profileBusy} onClick={() => void deleteProfile()}>确认删除</Button></>}><p className="dialog-warning">此操作会删除该方案的全部模型覆盖，但不会删除默认路由和网站。</p></Dialog>
    </div>
  );
}
