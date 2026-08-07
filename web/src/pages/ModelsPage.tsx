import { AlertTriangle, CheckCircle2, ChevronDown, CircleX, Layers3, Pencil, Plus, RefreshCw, Route as RouteIcon, Trash2, Waypoints } from 'lucide-react';
import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { useToast } from '../components/Toast';
import {
  Badge,
  Button,
  Dialog,
  EmptyState,
  ErrorState,
  Field,
  FilterBar,
  IconButton,
  InlineNotice,
  LoadingState,
  PageHeader,
  Panel,
  SearchField,
  Switch,
  submitForm,
} from '../components/ui';
import { api } from '../lib/api';
import { useResource } from '../lib/hooks';
import type {
  CreateDefaultRouteInput,
  GatewaySettings,
  ModelRoute,
  ModelTarget,
  MonitorSnapshot,
  MonitorTarget,
  RoutingProfile,
} from '../lib/types';
import { RouteEditorDialog } from './routes/RouteEditorDialog';
import { RouteTargetOrder } from './routes/RouteTargetOrder';

interface ModelsBaseData {
  profiles: RoutingProfile[];
  targets: ModelTarget[];
  defaultRoutes: ModelRoute[];
  monitor: MonitorSnapshot;
  settings: GatewaySettings | null;
}

async function loadModelsBase(): Promise<ModelsBaseData> {
  const profiles = await api.listRoutingProfiles();
  const defaultProfile = profiles.find((profile) => profile.isDefault);
  const [targets, defaultRoutes, monitor, settings] = await Promise.all([
    api.listModelTargets(),
    defaultProfile ? api.listProfileRoutes(defaultProfile.id) : Promise.resolve([]),
    api.monitorSnapshot().catch(() => ({ items: [] })),
    api.settings().catch(() => null),
  ]);
  return { profiles, targets, defaultRoutes, monitor, settings };
}

function ProfileDialog({
  open,
  saving,
  profile,
  profiles,
  onClose,
  onSubmit,
}: {
  open: boolean;
  saving: boolean;
  profile: RoutingProfile | null;
  profiles: RoutingProfile[];
  onClose: () => void;
  onSubmit: (input: { name: string }) => Promise<void>;
}) {
  const [name, setName] = useState(profile?.name || '');
  const [error, setError] = useState('');
  const editing = Boolean(profile);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!name.trim()) {
      setError('方案名称不能为空');
      return;
    }
    if (profiles.some((item) => item.id !== profile?.id && item.name.trim().toLowerCase() === name.trim().toLowerCase())) {
      setError('已有同名路由方案');
      return;
    }
    void onSubmit({ name: name.trim() });
  };

  return <Dialog open={open} title={editing ? '重命名路由方案' : '新建路由方案'} description={editing ? '名称只用于管理和分配下游密钥，不改变已有模型顺序。' : '命名方案默认继承全局路由，只需覆盖有差异的模型。'} onClose={onClose} footer={<><Button onClick={onClose}>取消</Button><Button variant="primary" busy={saving} onClick={() => submitForm('profile-form')}>{editing ? '保存名称' : '创建方案'}</Button></>}><form id="profile-form" className="form-stack" onSubmit={submit}><Field label="方案名称" required hint="例如 低延迟、Claude 专线。"><input className="input" value={name} onChange={(event) => { setName(event.target.value); setError(''); }} maxLength={120} autoFocus /></Field>{error && <p className="form-error">{error}</p>}</form></Dialog>;
}

type RouteTargetRuntimeState = 'ready' | 'unprobed' | 'attention' | 'cooling' | 'unavailable';

function isSlowFirstOutput(target: MonitorTarget | undefined, thresholdMs: number): boolean {
  const firstOutputMs = target?.latest?.firstOutputMs;
  return target?.latest?.outcome === 'success'
    && firstOutputMs !== null
    && firstOutputMs !== undefined
    && firstOutputMs > thresholdMs;
}

function policyDurationLabel(value: number): string {
  if (value >= 60_000 && value % 60_000 === 0) return `${value / 60_000} 分钟`;
  return `${Math.max(1, Math.round(value / 1_000))} 秒`;
}

function routeTargetRuntimeState(
  target: ModelTarget | undefined,
  monitor: MonitorTarget | undefined,
  slowFirstOutputThresholdMs: number,
): RouteTargetRuntimeState {
  if (isSlowFirstOutput(monitor, slowFirstOutputThresholdMs)) return 'cooling';
  if (!target?.routable || !target.usableCredentialCount || !target.enabled || !target.siteEnabled || !target.endpointEnabled) return 'unavailable';
  const monitorStatus = monitor?.status;
  if (monitorStatus === 'cooling') return 'cooling';
  if (['unavailable', 'disabled', 'model_disabled', 'no_credentials', 'unsupported'].includes(monitorStatus || '')) return 'unavailable';
  if (['degraded', 'suspect', 'recovering'].includes(monitorStatus || '')) return 'attention';
  if (!monitorStatus || ['unprobed', 'skipped'].includes(monitorStatus)) return 'unprobed';
  return 'ready';
}

function routeTargetNeedsAttention(state: RouteTargetRuntimeState): boolean {
  return state !== 'ready';
}

function routeTargetIsVerifiedUsable(state: RouteTargetRuntimeState): boolean {
  return state === 'ready' || state === 'attention';
}

export function ModelsPage() {
  const toast = useToast();
  const base = useResource(loadModelsBase, []);
  const [selectedProfileId, setSelectedProfileId] = useState(0);
  const routes = useResource(() => selectedProfileId ? api.listProfileRoutes(selectedProfileId) : Promise.resolve([]), [selectedProfileId]);
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState<'all' | 'enabled' | 'disabled' | 'attention'>('all');
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingRoute, setEditingRoute] = useState<ModelRoute | null>(null);
  const [saving, setSaving] = useState(false);
  const [reorderingRouteId, setReorderingRouteId] = useState<number | null>(null);
  const [profileDialogOpen, setProfileDialogOpen] = useState(false);
  const [editingProfile, setEditingProfile] = useState<RoutingProfile | null>(null);
  const [deletingProfile, setDeletingProfile] = useState<RoutingProfile | null>(null);
  const [profileSaving, setProfileSaving] = useState(false);
  const [expandedRouteIds, setExpandedRouteIds] = useState<Set<number>>(() => new Set());
  const [probingRouteId, setProbingRouteId] = useState<number | null>(null);
  const slowFirstOutputThresholdMs = base.data?.settings?.firstOutputTimeoutMs || 15_000;

  useEffect(() => {
    const profiles = base.data?.profiles || [];
    if (!profiles.length) {
      setSelectedProfileId(0);
      return;
    }
    const remembered = Number(localStorage.getItem('jieshan.routes.profile'));
    const preferred = profiles.some((profile) => profile.id === remembered)
      ? remembered
      : (profiles.find((profile) => profile.isDefault)?.id || profiles[0].id);
    setSelectedProfileId((current) => profiles.some((profile) => profile.id === current) ? current : preferred);
  }, [base.data?.profiles]);

  useEffect(() => {
    if (selectedProfileId) localStorage.setItem('jieshan.routes.profile', String(selectedProfileId));
    setExpandedRouteIds(new Set());
  }, [selectedProfileId]);

  const toggleRouteDetails = (publishedModelId: number) => {
    setExpandedRouteIds((current) => {
      const next = new Set(current);
      if (next.has(publishedModelId)) next.delete(publishedModelId);
      else next.add(publishedModelId);
      return next;
    });
  };

  const catalog = base.data?.targets || [];
  const catalogById = useMemo(() => new Map(catalog.map((target) => [target.id, target])), [catalog]);
  const monitorByModelId = useMemo(() => new Map((base.data?.monitor.items || []).map((item) => [item.publishedModelId, item])), [base.data?.monitor.items]);
  const visibleRoutes = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return (routes.data || []).filter((route) => {
      const monitored = monitorByModelId.get(route.publishedModelId);
      const hasIssue = route.targets.some((target) => {
        const item = catalogById.get(target.providerModelTargetId);
        const health = monitored?.targets.find((candidate) => candidate.providerModelTargetId === target.providerModelTargetId);
        const runtimeState = routeTargetRuntimeState(
          item,
          health,
          slowFirstOutputThresholdMs,
        );
        return routeTargetNeedsAttention(runtimeState);
      });
      if (status === 'enabled' && !route.enabled) return false;
      if (status === 'disabled' && route.enabled) return false;
      if (status === 'attention' && !hasIssue) return false;
      return !normalized || `${route.publicName} ${route.officialPriceSku} ${route.targets.map((item) => `${item.siteName} ${item.sourceModel}`).join(' ')}`.toLowerCase().includes(normalized);
    });
  }, [catalogById, monitorByModelId, query, routes.data, slowFirstOutputThresholdMs, status]);

  const selectedProfile = base.data?.profiles.find((profile) => profile.id === selectedProfileId);
  const editorTargets = catalog;

  const saveProfile = async (input: { name: string }) => {
    setProfileSaving(true);
    try {
      if (editingProfile) {
        const profile = await api.updateRoutingProfile(editingProfile.id, editingProfile.revision, input);
        base.setData((current) => current ? {
          ...current,
          profiles: current.profiles.map((item) => item.id === profile.id ? profile : item),
        } : current);
        routes.setData((current) => current?.map((route) => ({
          ...route,
          routingProfileName: route.routingProfileId === profile.id ? profile.name : route.routingProfileName,
          sourceProfileName: route.sourceProfileId === profile.id ? profile.name : route.sourceProfileName,
        })) || null);
        toast.show('路由方案已重命名', 'success');
      } else {
        const profile = await api.createRoutingProfile(input);
        base.setData((current) => current ? { ...current, profiles: [...current.profiles, profile] } : current);
        setSelectedProfileId(profile.id);
        toast.show('路由方案已创建', 'success');
      }
      setProfileDialogOpen(false);
      setEditingProfile(null);
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '路由方案保存失败', 'error');
      void base.refresh();
    } finally {
      setProfileSaving(false);
    }
  };

  const deleteProfile = async () => {
    if (!deletingProfile) return;
    setProfileSaving(true);
    try {
      await api.deleteRoutingProfile(deletingProfile.id, deletingProfile.revision);
      const fallback = base.data?.profiles.find((profile) => profile.isDefault && profile.id !== deletingProfile.id);
      setSelectedProfileId(fallback?.id || 0);
      base.setData((current) => current ? {
        ...current,
        profiles: current.profiles.filter((profile) => profile.id !== deletingProfile.id),
      } : current);
      setDeletingProfile(null);
      toast.show('路由方案已删除', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '路由方案删除失败', 'error');
      void base.refresh();
    } finally {
      setProfileSaving(false);
    }
  };

  const openCreateProfile = () => {
    setEditingProfile(null);
    setProfileDialogOpen(true);
  };

  const openRenameProfile = (profile: RoutingProfile) => {
    setEditingProfile(profile);
    setProfileDialogOpen(true);
  };

  const createRoute = async (input: CreateDefaultRouteInput) => {
    if (!selectedProfile?.isDefault) return;
    setSaving(true);
    try {
      const item = await api.createProfileRoute(selectedProfile.id, selectedProfile.revision, input);
      routes.setData((current) => [...(current || []), item].sort((left, right) => left.publicName.localeCompare(right.publicName)));
      setEditorOpen(false);
      toast.show(`模型 ${item.publicName} 已发布`, 'success');
      await base.refresh();
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '发布模型失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  const replaceTargets = async (route: ModelRoute, targetIds: number[]) => {
    setSaving(true);
    try {
      const updated = await api.replaceProfileRouteTargets(
        selectedProfileId,
        route.publishedModelId,
        route.revision,
        targetIds,
      );
      routes.setData((current) => current?.map((item) => item.publishedModelId === route.publishedModelId ? updated : item) || null);
      setEditorOpen(false);
      setEditingRoute(null);
      toast.show('模型上游列表已保存', 'success');
      await base.refresh();
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '上游列表保存失败', 'error');
      void routes.refresh();
    } finally {
      setSaving(false);
    }
  };

  const reorder = async (route: ModelRoute, targetIds: number[]) => {
    setReorderingRouteId(route.publishedModelId);
    const optimisticTargets = targetIds.map((id, position) => ({
      ...route.targets.find((target) => target.providerModelTargetId === id)!,
      position,
    }));
    routes.setData((current) => current?.map((item) => item.publishedModelId === route.publishedModelId ? { ...item, targets: optimisticTargets } : item) || null);
    try {
      const updated = await api.replaceProfileRouteTargets(selectedProfileId, route.publishedModelId, route.revision, targetIds);
      routes.setData((current) => current?.map((item) => item.publishedModelId === route.publishedModelId ? updated : item) || null);
      toast.show('优先级顺序已保存', 'success');
      await base.refresh();
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '顺序保存失败', 'error');
      await routes.refresh();
    } finally {
      setReorderingRouteId(null);
    }
  };

  const toggleRoute = async (route: ModelRoute, enabled: boolean) => {
    if (!selectedProfile) return;
    try {
      const updated = route.inherited
        ? await api.createProfileRoute(selectedProfileId, selectedProfile.revision, {
          publishedModelId: route.publishedModelId,
          enabled,
        })
        : await api.updateProfileRoute(selectedProfileId, route.publishedModelId, route.revision, { enabled });
      routes.setData((current) => current?.map((item) => item.publishedModelId === route.publishedModelId ? updated : item) || null);
      toast.show(enabled ? '模型已发布' : '模型已暂停', 'success');
      await base.refresh();
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '更新失败', 'error');
      void routes.refresh();
    }
  };

  const openCreate = () => {
    setEditingRoute(null);
    setEditorOpen(true);
  };

  const openRouteEditor = (route: ModelRoute) => {
    setEditingRoute(route);
    setEditorOpen(true);
  };

  const probeRoute = async (route: ModelRoute) => {
    if (probingRouteId !== null || !monitorByModelId.has(route.publishedModelId)) return;
    setProbingRouteId(route.publishedModelId);
    try {
      await api.probeModel(route.publishedModelId);
      const monitor = await api.monitorSnapshot();
      base.setData((current) => current ? { ...current, monitor } : current);
      toast.show(`${route.publicName} 的全部上游已探测`, 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '模型探测失败', 'error');
    } finally {
      setProbingRouteId(null);
    }
  };

  if (base.loading && !base.data) return <div className="page"><LoadingState label="正在读取模型目录" /></div>;
  if (base.error && !base.data) return <div className="page"><ErrorState message={base.error} onRetry={() => void base.refresh()} /></div>;

  return (
    <div className="page models-page">
      <PageHeader
        title="模型与路由"
        description="按模型维护严格的上游优先级；连接、探针和路由资格分开判断，故障时自动切换。"
        actions={<><Button icon={Plus} onClick={openCreateProfile}>新建方案</Button>{selectedProfile?.isDefault && <Button variant="primary" icon={Plus} disabled={!catalog.length} onClick={openCreate}>发布模型</Button>}</>}
      />

      {!base.data?.profiles.length ? <Panel><EmptyState title="还没有路由方案" description="创建第一套方案后，它将成为默认路由；随后按模型选择上游站点。" action={<Button variant="primary" icon={Plus} onClick={openCreateProfile}>创建默认路由</Button>} /></Panel> : <>
        <Panel className="route-context-panel">
          <div className="route-context-copy"><span className="route-context-icon"><Waypoints size={18} /></span><div><strong>当前路由方案</strong><span>{selectedProfile?.isDefault ? '维护全局发布模型和默认故障切换顺序。' : '未单独配置的模型自动继承默认路由。'}</span></div></div>
          <select className="select" aria-label="选择路由方案" value={selectedProfileId} onChange={(event) => setSelectedProfileId(Number(event.target.value))}>{base.data.profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}{profile.isDefault ? ' · 默认' : ''}</option>)}</select>
          <div className="profile-context-actions">
            {selectedProfile?.isDefault ? <Badge tone="accent">默认路由</Badge> : <Badge tone="info">继承默认</Badge>}
            {selectedProfile && <>
              <IconButton label="重命名路由方案" onClick={() => openRenameProfile(selectedProfile)}><Pencil size={15} /></IconButton>
              {!selectedProfile.isDefault && <IconButton label={selectedProfile.downstreamKeyCount ? `该方案仍被 ${selectedProfile.downstreamKeyCount} 个下游 Key 使用` : '删除路由方案'} disabled={selectedProfile.downstreamKeyCount > 0} onClick={() => setDeletingProfile(selectedProfile)}><Trash2 size={15} /></IconButton>}
            </>}
          </div>
          {selectedProfile && <span className="profile-usage">{selectedProfile.downstreamKeyCount} 个 Key 使用 · {selectedProfile.modelCount} 个模型</span>}
        </Panel>

        {base.data.settings && <div className="routing-policy-strip" role="status">
          <RouteIcon size={15} />
          <span><strong>严格顺序路由</strong> · 请求失败立即尝试下一家 · 仅连续 {base.data.settings.failureThreshold} 次失败后冷却 {policyDurationLabel(base.data.settings.cooldownMs)} · 首 Token 超过 {policyDurationLabel(slowFirstOutputThresholdMs)}单次即冷却</span>
        </div>}

        {!catalog.length && <InlineNotice tone="warning">模型目录为空。请先在“上游站点”中添加 API Key，并获取它支持的模型。</InlineNotice>}

        <Panel className="list-panel">
          <FilterBar trailing={<span className="result-count">{visibleRoutes.length} / {routes.data?.length || 0} 个模型</span>}>
            <SearchField value={query} onChange={setQuery} placeholder="搜索模型或上游站点" />
            <div className="segmented" role="group" aria-label="模型路由状态">{([
              ['all', '全部'],
              ['enabled', '已发布'],
              ['disabled', '已暂停'],
              ['attention', '需处理'],
            ] as const).map(([value, label]) => <button type="button" className={status === value ? 'is-active' : ''} onClick={() => setStatus(value)} key={value}>{label}</button>)}</div>
          </FilterBar>

          {routes.loading && !routes.data ? <LoadingState label="正在读取模型路由" /> : routes.error && !routes.data ? <ErrorState message={routes.error} onRetry={() => void routes.refresh()} /> : visibleRoutes.length === 0 ? <EmptyState title={routes.data?.length ? '没有匹配的模型' : '这套路由还没有模型'} description={routes.data?.length ? '调整搜索或状态筛选。' : selectedProfile?.isDefault ? '点击“发布模型”，先选模型，再按顺序选择上游站点。' : '默认路由还没有可继承的模型。'} action={!routes.data?.length && catalog.length && selectedProfile?.isDefault ? <Button variant="primary" icon={Plus} onClick={openCreate}>发布第一个模型</Button> : undefined} /> : <div className="model-route-list">{visibleRoutes.map((route) => {
            const monitored = monitorByModelId.get(route.publishedModelId);
            const targetStates = route.targets.map((target) => {
              const item = catalogById.get(target.providerModelTargetId);
              const health = monitored?.targets.find((candidate) => candidate.providerModelTargetId === target.providerModelTargetId);
              return routeTargetRuntimeState(
                item,
                health,
                slowFirstOutputThresholdMs,
              );
            });
            const usable = targetStates.filter(routeTargetIsVerifiedUsable).length;
            const attention = targetStates.filter((state) => state === 'attention').length;
            const cooling = targetStates.filter((state) => state === 'cooling').length;
            const unavailable = targetStates.filter((state) => state === 'unavailable').length;
            const unprobed = targetStates.filter((state) => state === 'unprobed').length;
            const hasIssue = targetStates.some(routeTargetNeedsAttention);
            const issueSummary = [
              attention ? `${attention} 个观察中` : '',
              cooling ? `${cooling} 个冷却中` : '',
              unavailable ? `${unavailable} 个不可用` : '',
              unprobed ? `${unprobed} 个尚未探测` : '',
            ].filter(Boolean).join(' · ');
            const statusSummary = issueSummary || '全部上游近期探针通过';
            const expanded = expandedRouteIds.has(route.publishedModelId);
            const routeStatusPoints = (monitored?.targets || []).flatMap((target) => target.statusBar).sort((left, right) => left.finishedAt - right.finishedAt).slice(-12);
            const visibleMonitorTargets = monitored?.targets || [];
            const primaryTarget = route.targets[0];
            return <article className="model-route" key={route.publishedModelId}>
              <header className="model-route-header">
                <button
                  type="button"
                  className="model-route-summary"
                  aria-expanded={expanded}
                  title={expanded ? '点击收起上游顺序' : '点击展开上游顺序'}
                  onClick={() => toggleRouteDetails(route.publishedModelId)}
                >
                  <span className="model-icon"><Layers3 size={17} /></span>
                  <span className="model-route-name"><span><code>{route.publicName}</code><Badge tone={route.enabled ? 'success' : 'neutral'}>{route.enabled ? '已发布' : '已暂停'}</Badge>{route.inherited && <Badge tone="info">继承 {route.sourceProfileName || '默认路由'}</Badge>}</span><span><span>{route.targets.length} 个上游</span><span>计价 <code>{route.officialPriceSku}</code></span></span></span>
                  <span className="model-route-primary"><small>首选上游</small><strong>{primaryTarget?.siteName || '尚未配置'}</strong><code>{primaryTarget?.sourceModel || '—'}</code></span>
                  <span className="model-route-health">
                    {!hasIssue && usable === route.targets.length && route.targets.length > 0
                      ? <CheckCircle2 size={16} />
                      : usable > 0 || unprobed > 0
                        ? <AlertTriangle size={16} />
                        : <CircleX className="model-route-health-failed" size={16} />}
                    <strong>{usable} / {route.targets.length} 上游可用</strong>
                    {routeStatusPoints.length ? <span className="status-history" aria-label={`${route.publicName} 的近期上游状态`} title={statusSummary}>{routeStatusPoints.map((point) => <span className={`history-${point.outcome === 'success' && point.firstOutputMs !== null && point.firstOutputMs > slowFirstOutputThresholdMs ? 'slow' : point.outcome}`} key={`${point.runId}-${point.finishedAt}`} />)}</span> : <span>{statusSummary}</span>}
                  </span>
                </button>
                <div className="model-route-actions"><IconButton label={monitored ? `探测 ${route.publicName} 的全部上游` : `${route.publicName} 未加入监控`} disabled={!monitored || probingRouteId !== null} onClick={() => void probeRoute(route)}><RefreshCw className={probingRouteId === route.publishedModelId ? 'spin' : undefined} size={16} /></IconButton><Switch checked={route.enabled} label={route.enabled ? '发布' : '暂停'} onChange={(next) => void toggleRoute(route, next)} /><IconButton className={`model-route-disclosure ${expanded ? 'is-open' : ''}`} label={`${expanded ? '收起' : '展开'} ${route.publicName} 的上游顺序`} aria-expanded={expanded} onClick={() => toggleRouteDetails(route.publishedModelId)}><ChevronDown size={17} /></IconButton></div>
              </header>
              {expanded && <div className="model-route-details">
                <div className="route-details-toolbar"><div><strong>上游优先级</strong><span>{route.inherited ? `当前继承“${route.sourceProfileName || '默认路由'}”，自定义后独立维护顺序。` : '从上到下依次尝试；拖动行或使用移动按钮调整。'}</span></div><Button size="sm" icon={Pencil} onClick={() => openRouteEditor(route)}>{route.inherited ? '自定义上游' : '调整上游'}</Button></div>
                <RouteTargetOrder
                  targets={route.targets}
                  catalog={catalog}
                  monitorTargets={visibleMonitorTargets}
                  slowFirstOutputThresholdMs={slowFirstOutputThresholdMs}
                  saving={reorderingRouteId === route.publishedModelId}
                  readOnly={route.inherited}
                  onReorder={(targetIds) => reorder(route, targetIds)}
                />
                <footer className="model-route-footer"><RouteIcon size={14} /><span>{route.inherited ? '继承路由不可直接拖动；点击“自定义上游”即可生成本方案的独立顺序。' : `普通失败累计到阈值才冷却；首 Token 超过 ${policyDurationLabel(slowFirstOutputThresholdMs)}则单次冷却。`}</span></footer>
              </div>}
            </article>;
          })}</div>}
        </Panel>
      </>}

      <ProfileDialog key={`${profileDialogOpen}-${editingProfile?.id || 'new'}`} open={profileDialogOpen} saving={profileSaving} profile={editingProfile} profiles={base.data?.profiles || []} onClose={() => { setProfileDialogOpen(false); setEditingProfile(null); }} onSubmit={saveProfile} />
      <Dialog open={Boolean(deletingProfile)} title="删除路由方案" description="删除后该方案的模型覆盖无法恢复。" onClose={() => setDeletingProfile(null)} footer={<><Button onClick={() => setDeletingProfile(null)}>取消</Button><Button variant="danger" icon={Trash2} busy={profileSaving} onClick={() => void deleteProfile()}>确认删除</Button></>}>
        <InlineNotice tone="warning">将删除路由方案“{deletingProfile?.name}”。只有未分配给任何下游 Key 的方案才允许删除。</InlineNotice>
      </Dialog>
      <RouteEditorDialog open={editorOpen} route={editingRoute} targets={editorTargets} saving={saving} onClose={() => { setEditorOpen(false); setEditingRoute(null); }} onCreate={createRoute} onReplaceTargets={replaceTargets} />
    </div>
  );
}
