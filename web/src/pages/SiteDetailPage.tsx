import {
  ArrowLeft,
  ChevronDown,
  Copy,
  ExternalLink,
  FileJson,
  Gauge,
  KeyRound,
  Layers3,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Settings2,
  Trash2,
} from 'lucide-react';
import { useEffect, useMemo, useState, type FormEvent, type MouseEvent } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useToast } from '../components/Toast';
import {
  Badge,
  Button,
  Dialog,
  Disclosure,
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
  Switch,
  Tabs,
  submitForm,
} from '../components/ui';
import { api, endpointProfiles } from '../lib/api';
import { formatDateTime, protocolLabel, surfaceLabel } from '../lib/format';
import { useResource } from '../lib/hooks';
import type {
  CredentialBinding,
  MonitorHealth,
  ProviderModel,
  Site,
  SiteAccountConnection,
  SiteCredential,
  SiteEndpoint,
  SiteRuntimeStatus,
} from '../lib/types';
import { CredentialDialog, type CredentialEditorInput } from './sites/CredentialDialog';
import { ModelDiscoveryDialog } from './sites/ModelDiscoveryDialog';
import { PlatformDetectionPanel } from './sites/PlatformDetectionPanel';
import { SiteAccountSettings, SiteBalanceSummary, SiteUsagePanel } from './sites/SiteAccountPanel';
import { TokenJsonImportDialog } from './sites/TokenJsonImportDialog';
import './sites/sitePrototype.css';
import '../styles/overview-sites-polish.css';

interface SiteInventory {
  endpoints: SiteEndpoint[];
  credentials: SiteCredential[];
  bindings: Record<number, CredentialBinding[]>;
  models: Record<number, ProviderModel[]>;
}

interface KeyView {
  credential: SiteCredential;
  endpoint: SiteEndpoint | null;
  endpoints: SiteEndpoint[];
  models: ProviderModel[];
  modelNames: string[];
}

async function loadInventory(siteId: number): Promise<SiteInventory> {
  const [endpoints, credentials] = await Promise.all([api.listEndpoints(siteId), api.listCredentials(siteId)]);
  const [bindingGroups, modelGroups] = await Promise.all([
    Promise.all(endpoints.map((endpoint) => api.listEndpointCredentialBindings(siteId, endpoint.id))),
    Promise.all(endpoints.map((endpoint) => api.listProviderModels(siteId, endpoint.id))),
  ]);
  return {
    endpoints,
    credentials,
    bindings: Object.fromEntries(endpoints.map((endpoint, index) => [endpoint.id, bindingGroups[index]])),
    models: Object.fromEntries(endpoints.map((endpoint, index) => [endpoint.id, modelGroups[index]])),
  };
}

function keyViewsFromInventory(inventory: SiteInventory | null): KeyView[] {
  if (!inventory) return [];
  return inventory.credentials.map((credential) => {
    const endpoints = inventory.endpoints.filter((endpoint) => (inventory.bindings[endpoint.id] || []).some((binding) => binding.credentialId === credential.id));
    const models = endpoints.flatMap((endpoint) => inventory.models[endpoint.id] || []);
    const uniqueModels = [...new Map(models.map((model) => [model.sourceModel, model])).values()];
    return { credential, endpoint: endpoints[0] || null, endpoints, models: uniqueModels, modelNames: uniqueModels.map((model) => model.sourceModel) };
  });
}

function runtimeTone(state: string): 'success' | 'warning' | 'danger' | 'neutral' {
  if (state === 'ready' || state === 'healthy') return 'success';
  if (state === 'cooldown' || state === 'suspect') return 'warning';
  if (state === 'error' || state === 'credential_error') return 'danger';
  return 'neutral';
}

function runtimeLabel(state: string): string {
  return {
    ready: '可用', healthy: '可用', cooldown: '冷却中', suspect: '观察中', error: '异常', credential_error: '密钥异常', unknown: '尚未检测',
  }[state] || (state ? state : '尚未检测');
}

function siteRuntimeHealth(credentials: SiteCredential[]): MonitorHealth {
  const enabled = credentials.filter((credential) => credential.enabled);
  if (!enabled.length) return credentials.length ? 'disabled' : 'unprobed';
  const states = enabled.map((credential) => credential.runtimeState);
  const healthy = states.filter((state) => state === 'ready' || state === 'healthy').length;
  if (healthy === states.length) return 'healthy';
  if (healthy > 0) return 'degraded';
  if (states.some((state) => state === 'cooldown')) return 'cooling';
  if (states.some((state) => state === 'suspect')) return 'suspect';
  if (states.some((state) => state === 'error' || state === 'credential_error')) return 'unavailable';
  return 'unprobed';
}

function profileForEndpoint(endpoint: SiteEndpoint | null) {
  if (!endpoint) return endpointProfiles[0];
  return endpointProfiles.find((profile) => profile.protocol === endpoint.wireProtocol && profile.surface === endpoint.surface)
    || endpointProfiles.find((profile) => profile.protocol === endpoint.wireProtocol)
    || endpointProfiles[0];
}

function KeyApiLabel({ endpoint }: { endpoint: SiteEndpoint | null }) {
  if (!endpoint) return <span>未配置 API 类型</span>;
  return <span>{protocolLabel(endpoint.wireProtocol)} · {surfaceLabel(endpoint.surface)}</span>;
}

function SiteSettingsDialog({
  open,
  site,
  account,
  runtime,
  saving,
  onClose,
  onSubmit,
  onRefreshAccount,
  onDelete,
}: {
  open: boolean;
  site: Site;
  account: SiteAccountConnection | null;
  runtime: SiteRuntimeStatus | null;
  saving: boolean;
  onClose: () => void;
  onSubmit: (input: { name: string; dashboardUrl: string; enabled: boolean; maxConcurrency: number }) => Promise<void>;
  onRefreshAccount: () => Promise<void>;
  onDelete: () => void;
}) {
  const [tab, setTab] = useState<'basic' | 'account'>('basic');
  const [name, setName] = useState(site.name);
  const [dashboardUrl, setDashboardUrl] = useState(site.dashboardUrl);
  const [enabled, setEnabled] = useState(site.enabled);
  const [maxConcurrency, setMaxConcurrency] = useState(site.maxConcurrency || 4);

  useEffect(() => {
    if (!open) return;
    setTab('basic');
    setName(site.name);
    setDashboardUrl(site.dashboardUrl);
    setEnabled(site.enabled);
    setMaxConcurrency(site.maxConcurrency || 4);
  }, [open, site]);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!name.trim()) return;
    void onSubmit({ name: name.trim(), dashboardUrl: dashboardUrl.trim(), enabled, maxConcurrency });
  };

  return (
    <Dialog
      open={open}
      title="站点设置"
      description="统一维护站点基本信息和站点账户。"
      onClose={onClose}
      width="lg"
      footer={tab === 'basic' ? <><Button onClick={onClose}>取消</Button><Button variant="primary" busy={saving} onClick={() => submitForm('site-settings-form')}>保存设置</Button></> : <Button onClick={onClose}>完成</Button>}
    >
      <Tabs label="站点设置分类" value={tab} onChange={setTab} items={[{ value: 'basic', label: '基本信息' }, { value: 'account', label: '站点账户' }]} />
      {tab === 'basic' ? <form id="site-settings-form" className="form-stack" onSubmit={submit}>
        <Field label="站点名称" required><input className="input" autoFocus value={name} onChange={(event) => setName(event.target.value)} /></Field>
        <Field label="站点地址" required hint="用于打开站点，也作为站点账户自动识别地址。"><input className="input code-input" type="url" value={dashboardUrl} onChange={(event) => setDashboardUrl(event.target.value)} /></Field>
        <Switch checked={enabled} label="允许这个站点参与模型路由" onChange={setEnabled} />
        <Disclosure summary={<span className="settings-disclosure-title"><Gauge size={17} /><span><strong>并发与排队</strong><small>站点级并发上限和实时只读状态</small></span></span>}>
          <div className="site-concurrency-settings">
            <Field label="最大同时请求数" hint="当前站点满载时先尝试下一家；全部上游满载后才进入公平队列。">
              <div className="unit-input"><input className="input" type="number" min={1} max={10_000} value={maxConcurrency} onChange={(event) => setMaxConcurrency(Math.max(1, Number(event.target.value) || 1))} /><span>请求</span></div>
            </Field>
            <div className="site-runtime-readout" aria-label="站点实时并发状态">
              <div><span>处理中</span><strong>{runtime?.inflightRequests ?? '-'}</strong></div>
              <div><span>同时请求上限</span><strong>{runtime?.maxConcurrency ?? maxConcurrency}</strong></div>
              <div><span>排队</span><strong>{runtime?.queuedRequests ?? '-'}</strong></div>
            </div>
          </div>
        </Disclosure>
        <div className="danger-zone-inline"><div><strong>删除站点</strong><span>同时清理这个站点的 API Key、模型来源、余额连接和运行状态。</span></div><Button variant="danger" icon={Trash2} onClick={onDelete}>删除站点</Button></div>
      </form> : <SiteAccountSettings site={site} account={account} onSaved={onRefreshAccount} />}
    </Dialog>
  );
}

export function SiteDetailPage() {
  const siteId = Number(useParams().siteId);
  const navigate = useNavigate();
  const toast = useToast();
  const siteResource = useResource(() => api.getSite(siteId), [siteId]);
  const inventory = useResource(() => loadInventory(siteId), [siteId]);
  const accountResource = useResource(() => api.getSiteAccount(siteId), [siteId]);
  const platformResource = useResource(() => api.sitePlatformDetection(siteId), [siteId]);
  const runtimeResource = useResource(() => api.siteRuntimeStatus(siteId), [siteId]);
  const [tab, setTab] = useState<'keys' | 'models' | 'usage'>('keys');
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [siteSaving, setSiteSaving] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [siteDeleting, setSiteDeleting] = useState(false);
  const [keyDialogOpen, setKeyDialogOpen] = useState(false);
  const [keySaving, setKeySaving] = useState(false);
  const [editingKey, setEditingKey] = useState<KeyView | null>(null);
  const [discoveryKey, setDiscoveryKey] = useState<KeyView | null>(null);
  const [expandedKeyIds, setExpandedKeyIds] = useState<Set<number>>(new Set());
  const [enteredCredentialSecrets, setEnteredCredentialSecrets] = useState<Map<number, string>>(() => new Map());
  const [keyQuery, setKeyQuery] = useState('');
  const [keyStatus, setKeyStatus] = useState<'all' | 'enabled' | 'disabled'>('all');
  const [modelQuery, setModelQuery] = useState('');
  const [tokenImportOpen, setTokenImportOpen] = useState(false);

  const keyViews = useMemo(() => keyViewsFromInventory(inventory.data), [inventory.data]);
  const visibleKeys = useMemo(() => {
    const normalized = keyQuery.trim().toLowerCase();
    return keyViews.filter((item) => {
      if (keyStatus === 'enabled' && !item.credential.enabled) return false;
      if (keyStatus === 'disabled' && item.credential.enabled) return false;
      return !normalized || `${item.credential.name} ${item.endpoint?.baseUrl || ''} ${item.endpoint?.wireProtocol || ''} ${item.modelNames.join(' ')}`.toLowerCase().includes(normalized);
    });
  }, [keyQuery, keyStatus, keyViews]);

  const modelSources = useMemo(() => {
    const rows = new Map<string, { model: string; keyNames: string[]; protocols: string[]; lastSeenAt: number | null }>();
    for (const key of keyViews) {
      for (const model of key.models) {
        const current = rows.get(model.sourceModel) || { model: model.sourceModel, keyNames: [], protocols: [], lastSeenAt: null };
        if (!current.keyNames.includes(key.credential.name)) current.keyNames.push(key.credential.name);
        if (key.endpoint) {
          const protocol = protocolLabel(key.endpoint.wireProtocol);
          if (!current.protocols.includes(protocol)) current.protocols.push(protocol);
        }
        current.lastSeenAt = Math.max(current.lastSeenAt || 0, model.lastSeenAt || model.updatedAt || 0) || null;
        rows.set(model.sourceModel, current);
      }
    }
    const normalized = modelQuery.trim().toLowerCase();
    return [...rows.values()].filter((item) => !normalized || `${item.model} ${item.keyNames.join(' ')}`.toLowerCase().includes(normalized));
  }, [keyViews, modelQuery]);

  if (!Number.isInteger(siteId) || siteId <= 0) return <div className="page"><ErrorState message="站点 ID 无效" /></div>;
  if (siteResource.loading && !siteResource.data) return <div className="page"><LoadingState label="正在读取站点配置" /></div>;
  if (siteResource.error && !siteResource.data) return <div className="page"><ErrorState message={siteResource.error} onRetry={() => void siteResource.refresh()} /></div>;
  if (!siteResource.data) return null;

  const site = siteResource.data;
  const account = accountResource.data as SiteAccountConnection | null;
  const uniqueModelCount = new Set(keyViews.flatMap((item) => item.modelNames)).size;
  const latestUpdate = Math.max(site.updatedAt, ...keyViews.map((item) => item.credential.runtimeUpdatedAt || item.credential.updatedAt));

  const saveSite = async (input: { name: string; dashboardUrl: string; enabled: boolean; maxConcurrency: number }) => {
    setSiteSaving(true);
    try {
      const updated = await api.updateSite(site.id, site.revision, { ...input, dashboardUrl: input.dashboardUrl || null });
      siteResource.setData(updated);
      void runtimeResource.refresh();
      setSettingsOpen(false);
      toast.show('站点设置已保存', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '保存失败', 'error');
      void siteResource.refresh();
    } finally {
      setSiteSaving(false);
    }
  };

  const deleteSite = async () => {
    setSiteDeleting(true);
    try {
      await api.deleteSite(site.id, site.revision);
      toast.show(`站点“${site.name}”已删除`, 'success');
      navigate('/sites', { replace: true });
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '删除站点失败', 'error');
      void siteResource.refresh();
    } finally {
      setSiteDeleting(false);
    }
  };

  const saveCredential = async (input: CredentialEditorInput) => {
    setKeySaving(true);
    try {
      const profile = endpointProfiles.find((item) => item.id === input.profileId) || endpointProfiles[0];
      let savedCredential: SiteCredential;
      if (editingKey) {
        let updatedCredential = await api.updateCredential(site.id, editingKey.credential.id, editingKey.credential.revision, { name: input.name, enabled: input.enabled });
        if (input.secret) updatedCredential = await api.replaceCredentialSecret(site.id, updatedCredential.id, updatedCredential.revision, input.secret);
        savedCredential = updatedCredential;
        if (editingKey.endpoint) {
          await api.updateEndpoint(site.id, editingKey.endpoint.id, editingKey.endpoint.revision, {
            name: `${input.name} · ${profile.label}`,
            baseUrl: input.baseUrl,
            wireProtocol: profile.protocol,
            surface: profile.surface,
            adapterKind: 'generic',
            authScheme: profile.authScheme,
            headers: profile.protocol === 'anthropic' ? { 'anthropic-version': '2023-06-01' } : {},
            enabled: input.enabled,
          });
        } else {
          const endpoint = await api.createEndpoint(site.id, {
            name: `${input.name} · ${profile.label}`,
            baseUrl: input.baseUrl,
            wireProtocol: profile.protocol,
            surface: profile.surface,
            adapterKind: 'generic',
            authScheme: profile.authScheme,
            headers: profile.protocol === 'anthropic' ? { 'anthropic-version': '2023-06-01' } : {},
            enabled: input.enabled,
          });
          await api.replaceEndpointCredentialBindings(site.id, endpoint.id, endpoint.revision, [updatedCredential.id]);
        }
        toast.show('API Key 已更新', 'success');
      } else {
        const credential = await api.createCredential(site.id, { name: input.name, secret: input.secret, enabled: input.enabled });
        savedCredential = credential;
        const endpoint = await api.createEndpoint(site.id, {
          name: `${input.name} · ${profile.label}`,
          baseUrl: input.baseUrl,
          wireProtocol: profile.protocol,
          surface: profile.surface,
          adapterKind: 'generic',
          authScheme: profile.authScheme,
          headers: profile.protocol === 'anthropic' ? { 'anthropic-version': '2023-06-01' } : {},
          enabled: input.enabled,
        });
        await api.replaceEndpointCredentialBindings(site.id, endpoint.id, endpoint.revision, [credential.id]);
      }
      if (input.secret.trim()) {
        setEnteredCredentialSecrets((current) => new Map(current).set(savedCredential.id, input.secret.trim()));
        toast.show(editingKey ? 'API Key 已更新；本次输入的密钥可立即复制' : 'API Key 已添加；本次输入的密钥可立即复制', 'success');
      } else {
        toast.show('API Key 已更新', 'success');
      }
      await inventory.refresh();
      setKeyDialogOpen(false);
      setEditingKey(null);
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : 'API Key 保存失败', 'error');
    } finally {
      setKeySaving(false);
    }
  };

  const toggleCredential = async (item: KeyView, enabled: boolean) => {
    try {
      await api.updateCredential(site.id, item.credential.id, item.credential.revision, { enabled });
      if (item.endpoint) await api.updateEndpoint(site.id, item.endpoint.id, item.endpoint.revision, { enabled });
      await inventory.refresh();
      toast.show(enabled ? 'API Key 已启用' : 'API Key 已停用', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '更新失败', 'error');
      void inventory.refresh();
    }
  };

  const copyCredential = async (credential: SiteCredential) => {
    const secret = enteredCredentialSecrets.get(credential.id);
    if (!secret) {
      toast.show('已保存的 API Key 不会再次返回；替换密钥后可在当前会话立即复制。', 'info');
      return;
    }
    try {
      await navigator.clipboard.writeText(secret);
      toast.show(`${credential.name} 已复制`, 'success');
    } catch {
      toast.show('浏览器未允许写入剪贴板', 'error');
    }
  };

  const refreshAfterTokenImport = async () => {
    await inventory.refresh();
    setTab('keys');
  };

  const toggleKeyExpanded = (id: number) => {
    setExpandedKeyIds((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const stopRowAction = (event: MouseEvent<HTMLElement>) => event.stopPropagation();

  return (
    <div className="page site-detail-page">
      <Link className="back-link" to="/sites"><ArrowLeft size={15} />返回上游站点</Link>
      <PageHeader
        title={site.name}
        description={site.dashboardUrl || '尚未设置站点地址'}
        meta={<div className="site-detail-statuses">
          <span><small>配置</small><Badge tone={site.enabled ? 'success' : 'neutral'}>{site.enabled ? '已启用' : '已停用'}</Badge></span>
          <span><small>运行</small><HealthBadge state={siteRuntimeHealth(keyViews.map((item) => item.credential))} /></span>
        </div>}
        actions={<>
          {site.dashboardUrl && <a className="button button-secondary button-md" href={site.dashboardUrl} target="_blank" rel="noreferrer"><ExternalLink size={16} />打开站点</a>}
          <Button icon={Settings2} onClick={() => setSettingsOpen(true)}>站点设置</Button>
        </>}
      />

      <div className="site-overview-strip">
        <SiteBalanceSummary site={site} account={account} loading={accountResource.loading} onRefreshAccount={accountResource.refresh} onOpenSettings={() => setSettingsOpen(true)} />
        <div className="site-overview-metric"><KeyRound size={17} /><span>API Key</span><strong>{keyViews.length}</strong></div>
        <div className="site-overview-metric"><Layers3 size={17} /><span>支持模型</span><strong>{uniqueModelCount}</strong></div>
        <div className="site-overview-metric"><RefreshCw size={17} /><span>最近更新</span><strong>{formatDateTime(latestUpdate, true)}</strong></div>
      </div>

      <PlatformDetectionPanel
        detection={platformResource.data}
        loading={platformResource.loading}
        error={platformResource.error}
        onRetry={() => void platformResource.refresh()}
      />

      <Tabs label="站点详情视图" value={tab} onChange={setTab} items={[
        { value: 'keys', label: 'API Key', count: keyViews.length },
        { value: 'models', label: '模型', count: uniqueModelCount },
        { value: 'usage', label: '使用日志' },
      ]} />

      {tab === 'keys' && <Panel className="site-key-panel">
        <div className="site-section-heading"><div><h2>上游 API Key</h2><p>每把 Key 直接维护自己的 API 类型、地址和模型列表。</p></div><div className="site-section-actions"><Button icon={FileJson} size="sm" onClick={() => setTokenImportOpen(true)}>导入 Token JSON</Button><Button icon={Plus} size="sm" onClick={() => { setEditingKey(null); setKeyDialogOpen(true); }}>添加 API Key</Button></div></div>
        {keyViews.length > 0 && <FilterBar trailing={<span className="result-count">{visibleKeys.length} / {keyViews.length} 个 Key</span>}>
          <SearchField value={keyQuery} onChange={setKeyQuery} placeholder="搜索名称、API 地址或模型" />
          <div className="segmented" role="group" aria-label="API Key 状态">{([['all', '全部'], ['enabled', '已启用'], ['disabled', '已停用']] as const).map(([value, label]) => <button type="button" className={keyStatus === value ? 'is-active' : ''} onClick={() => setKeyStatus(value)} key={value}>{label}</button>)}</div>
        </FilterBar>}
        {inventory.loading && !inventory.data ? <LoadingState label="正在读取 API Key" />
          : inventory.error && !inventory.data ? <ErrorState message={inventory.error} onRetry={() => void inventory.refresh()} />
            : visibleKeys.length === 0 ? <EmptyState title={keyViews.length ? '没有匹配的 API Key' : '还没有 API Key'} description={keyViews.length ? '调整搜索词或状态筛选。' : '添加第一把 Key 后即可直接获取它支持的模型。'} action={!keyViews.length ? <Button variant="primary" icon={Plus} onClick={() => setKeyDialogOpen(true)}>添加第一把 API Key</Button> : undefined} />
              : <div className="site-key-list">{visibleKeys.map((item) => {
                const expanded = expandedKeyIds.has(item.credential.id);
                const latestModelAt = Math.max(0, ...item.models.map((model) => model.lastSeenAt || model.updatedAt || 0));
                return <article className={`site-key-row${expanded ? ' is-expanded' : ''}`} key={item.credential.id}>
                  <header className="site-key-heading">
                    <button type="button" className="site-key-click-area" aria-expanded={expanded} onClick={() => toggleKeyExpanded(item.credential.id)}>
                      <span className="credential-icon"><KeyRound size={16} /></span>
                      <div className="site-key-identity"><strong>{item.credential.name}</strong><code>{item.endpoint?.baseUrl || '尚未配置 API 地址'}</code></div>
                      <div className="site-key-api"><KeyApiLabel endpoint={item.endpoint} /><small>{item.modelNames.length ? `${item.modelNames.length} 个模型` : '未获取模型'}</small></div>
                      <div className="site-key-statuses">
                        <span><small>配置</small><Badge tone={item.credential.enabled ? 'success' : 'neutral'}>{item.credential.enabled ? '已启用' : '已停用'}</Badge></span>
                        <span><small>运行</small><Badge tone={runtimeTone(item.credential.runtimeState)}>{runtimeLabel(item.credential.runtimeState)}</Badge></span>
                      </div>
                      <div className="site-key-updated"><span>{latestModelAt ? `模型更新于 ${formatDateTime(latestModelAt, true)}` : '尚未获取模型'}</span><small>{item.credential.lastHttpStatus ? `最近 HTTP ${item.credential.lastHttpStatus}` : `状态更新于 ${formatDateTime(item.credential.runtimeUpdatedAt, true)}`}</small></div>
                      <ChevronDown className={expanded ? 'is-open' : ''} size={17} />
                    </button>
                    <div className="site-key-actions" onClick={stopRowAction}>
                      <IconButton label={enteredCredentialSecrets.has(item.credential.id) ? '复制本次输入的 API Key' : '已保存密钥不可再次显示，请编辑并替换'} disabled={!enteredCredentialSecrets.has(item.credential.id)} onClick={() => void copyCredential(item.credential)}><Copy size={16} /></IconButton>
                      <IconButton label="编辑 API Key" onClick={() => { setEditingKey(item); setKeyDialogOpen(true); }}><Pencil size={16} /></IconButton>
                      <Button size="sm" icon={RefreshCw} disabled={!item.endpoint || !item.credential.enabled} onClick={() => setDiscoveryKey(item)}>获取模型</Button>
                      <Switch checked={item.credential.enabled} label={item.credential.enabled ? '启用' : '停用'} onChange={(next) => void toggleCredential(item, next)} />
                    </div>
                  </header>
                  {expanded && <div className="site-key-models">
                    <div className="site-key-models-heading"><div><strong>这把 Key 支持的模型</strong><span>{item.modelNames.length ? `共 ${item.modelNames.length} 个，参与路由时按模型选择这把 Key。` : '还没有模型，点击“获取模型”读取上游列表。'}</span></div>{item.endpoint && <Badge tone="info">{profileForEndpoint(item.endpoint).label}</Badge>}</div>
                    <div className="site-key-mobile-actions">
                      <Button size="sm" icon={Copy} disabled={!enteredCredentialSecrets.has(item.credential.id)} onClick={() => void copyCredential(item.credential)}>复制本次输入</Button>
                      <Button size="sm" icon={Pencil} onClick={() => { setEditingKey(item); setKeyDialogOpen(true); }}>编辑</Button>
                      <Button size="sm" icon={RefreshCw} disabled={!item.endpoint || !item.credential.enabled} onClick={() => setDiscoveryKey(item)}>获取模型</Button>
                    </div>
                    <InlineNotice>{enteredCredentialSecrets.has(item.credential.id) ? '本次输入的密钥只在当前会话保留，刷新页面后不会再次显示。' : '已保存的上游密钥不会再次显示或复制；如需更新，请点击“编辑 API Key”并替换密钥。'}</InlineNotice>
                    {item.modelNames.length ? <div className="site-key-model-grid">{item.modelNames.map((model) => <code key={model}>{model}</code>)}</div> : <InlineNotice>尚未获取模型，不会显示虚假的“0 个模型”。</InlineNotice>}
                  </div>}
                </article>;
              })}</div>}
      </Panel>}

      {tab === 'models' && <Panel className="site-model-panel">
        <div className="site-section-heading"><div><h2>站点支持模型</h2><p>按所有启用 Key 的模型列表去重汇总，并标明具体来源。</p></div><span className="result-count">{modelSources.length} 个模型</span></div>
        <FilterBar><SearchField value={modelQuery} onChange={setModelQuery} placeholder="搜索模型或 API Key" /></FilterBar>
        {!modelSources.length ? <EmptyState title="尚未获取模型" description="回到 API Key 标签，选择一把 Key 获取上游模型。" /> : <div className="site-model-list">{modelSources.map((item) => <div className="site-model-row" key={item.model}>
          <span className="site-model-icon"><Layers3 size={16} /></span>
          <div><code>{item.model}</code><span>{item.protocols.join(' · ') || '协议待识别'}</span></div>
          <div><span>来自 {item.keyNames.length} 把 Key</span><strong>{item.keyNames.join('、')}</strong></div>
          <span>{item.lastSeenAt ? `获取于 ${formatDateTime(item.lastSeenAt, true)}` : '更新时间未知'}</span>
        </div>)}</div>}
      </Panel>}

      {tab === 'usage' && <SiteUsagePanel site={site} account={account} accountLoading={accountResource.loading} onRefreshAccount={accountResource.refresh} onOpenSettings={() => setSettingsOpen(true)} />}

      <SiteSettingsDialog open={settingsOpen} site={site} account={account} runtime={runtimeResource.data} saving={siteSaving} onClose={() => setSettingsOpen(false)} onSubmit={saveSite} onRefreshAccount={accountResource.refresh} onDelete={() => { setSettingsOpen(false); setDeleteOpen(true); }} />
      <Dialog
        open={deleteOpen}
        title="删除上游站点"
        description={`确认删除“${site.name}”？`}
        onClose={() => { if (!siteDeleting) setDeleteOpen(false); }}
        footer={<><Button disabled={siteDeleting} onClick={() => setDeleteOpen(false)}>取消</Button><Button variant="danger" icon={Trash2} busy={siteDeleting} onClick={() => void deleteSite()}>确认删除</Button></>}
      >
        <InlineNotice tone="danger">删除后，该站点不会再参与路由；它的上游配置、余额快照和站点使用记录也会一并清理。下游调用日志会保留历史快照。</InlineNotice>
      </Dialog>
      <CredentialDialog
        open={keyDialogOpen}
        saving={keySaving}
        credential={editingKey?.credential}
        endpoint={editingKey?.endpoint}
        defaultBaseUrl={site.dashboardUrl}
        onClose={() => { setKeyDialogOpen(false); setEditingKey(null); }}
        onSubmit={saveCredential}
      />
      <ModelDiscoveryDialog open={Boolean(discoveryKey)} siteId={site.id} credential={discoveryKey?.credential || null} endpoint={discoveryKey?.endpoint || null} onClose={() => setDiscoveryKey(null)} onImported={inventory.refresh} />
      <TokenJsonImportDialog open={tokenImportOpen} siteId={site.id} onClose={() => setTokenImportOpen(false)} onImported={refreshAfterTokenImport} />
    </div>
  );
}
