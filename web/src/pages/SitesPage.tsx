import {
  ArrowLeft,
  ArrowRight,
  Check,
  CircleCheck,
  CircleDollarSign,
  ExternalLink,
  Eye,
  EyeOff,
  KeyRound,
  Layers3,
  Plus,
  Server,
} from 'lucide-react';
import { useMemo, useState, type FormEvent } from 'react';
import { Link, useNavigate } from 'react-router-dom';
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
  Switch,
  submitForm,
} from '../components/ui';
import { useToast } from '../components/Toast';
import { api, endpointProfiles } from '../lib/api';
import { formatDateTime } from '../lib/format';
import { useResource } from '../lib/hooks';
import type { MonitorHealth, Site, SiteCredential } from '../lib/types';
import './sites/sitePrototype.css';
import '../styles/overview-sites-polish.css';

const wizardSteps = ['站点信息', '添加 API Key', '确认模型'] as const;

interface SiteRow extends Site {
  credentialCount: number;
  modelCount: number | null;
  balanceValue: string | null;
  balanceUnit: string;
  balanceUpdatedAt: number | null;
  runtimeHealth: MonitorHealth;
}

function credentialHealth(credentials: SiteCredential[]): MonitorHealth {
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

async function loadSites(): Promise<SiteRow[]> {
  const sites = await api.listSites();
  return Promise.all(sites.map(async (site) => {
    const [endpointsResult, credentialsResult, accountResult] = await Promise.allSettled([
      api.listEndpoints(site.id),
      api.listCredentials(site.id),
      api.getSiteAccount(site.id),
    ]);
    const endpoints = endpointsResult.status === 'fulfilled' ? endpointsResult.value : [];
    const credentials = credentialsResult.status === 'fulfilled' ? credentialsResult.value : [];
    const modelGroups = await Promise.allSettled(endpoints.map((endpoint) => api.listProviderModels(site.id, endpoint.id)));
    const modelNames = new Set(modelGroups.flatMap((result) => result.status === 'fulfilled' ? result.value.map((model) => model.sourceModel) : []));
    const account = accountResult.status === 'fulfilled' ? accountResult.value : null;
    return {
      ...site,
      credentialCount: credentials.length,
      modelCount: endpointsResult.status === 'fulfilled' ? modelNames.size : null,
      balanceValue: account?.latestBalance?.availableValue || null,
      balanceUnit: account?.latestBalance?.availableUnit || 'USD',
      balanceUpdatedAt: account?.latestBalance?.capturedAt || null,
      runtimeHealth: credentialHealth(credentials),
    };
  }));
}

export function SitesPage() {
  const navigate = useNavigate();
  const toast = useToast();
  const resource = useResource(loadSites, []);
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState<'all' | 'enabled' | 'disabled'>('all');
  const [createOpen, setCreateOpen] = useState(false);
  const [createStep, setCreateStep] = useState(0);
  const [name, setName] = useState('');
  const [dashboardUrl, setDashboardUrl] = useState('');
  const [enabled, setEnabled] = useState(true);
  const [profileId, setProfileId] = useState(endpointProfiles[0].id);
  const [baseUrl, setBaseUrl] = useState('');
  const [credentialName, setCredentialName] = useState('主 API Key');
  const [secret, setSecret] = useState('');
  const [secretVisible, setSecretVisible] = useState(false);
  const [testing, setTesting] = useState(false);
  const [tested, setTested] = useState(false);
  const [models, setModels] = useState<string[]>([]);
  const [selectedModels, setSelectedModels] = useState<string[]>([]);
  const [createError, setCreateError] = useState('');
  const [saving, setSaving] = useState(false);

  const rows = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return (resource.data || []).filter((site) => {
      if (status === 'enabled' && !site.enabled) return false;
      if (status === 'disabled' && site.enabled) return false;
      return !normalized || `${site.name} ${site.dashboardUrl}`.toLowerCase().includes(normalized);
    });
  }, [query, resource.data, status]);

  const selectedProfile = endpointProfiles.find((item) => item.id === profileId) || endpointProfiles[0];

  const resetCreate = () => {
    setCreateStep(0);
    setName('');
    setDashboardUrl('');
    setEnabled(true);
    setProfileId(endpointProfiles[0].id);
    setBaseUrl('');
    setCredentialName('主 API Key');
    setSecret('');
    setSecretVisible(false);
    setTesting(false);
    setTested(false);
    setModels([]);
    setSelectedModels([]);
    setCreateError('');
  };

  const validateStep = (): boolean => {
    if (createStep === 0 && (!name.trim() || !dashboardUrl.trim())) {
      setCreateError('请填写站点名称和地址');
      return false;
    }
    if (createStep === 1 && (!credentialName.trim() || !baseUrl.trim() || !secret.trim())) {
      setCreateError('请完整填写 API Key 名称、API 地址和密钥');
      return false;
    }
    setCreateError('');
    return true;
  };

  const advanceCreate = (event: FormEvent) => {
    event.preventDefault();
    if (!validateStep()) return;
    if (createStep === 0 && !baseUrl) setBaseUrl(dashboardUrl.trim());
    if (createStep < wizardSteps.length - 1) setCreateStep((current) => current + 1);
  };

  const testConnection = async () => {
    if (!baseUrl.trim() || !secret.trim()) {
      setCreateError('API 地址和 API Key 不能为空');
      return;
    }
    setTesting(true);
    setCreateError('');
    try {
      const result = await api.previewModels({
        baseUrl: baseUrl.trim(),
        wireProtocol: selectedProfile.protocol,
        surface: selectedProfile.surface,
        authScheme: selectedProfile.authScheme,
        apiKey: secret.trim(),
      });
      if (!result.models.length) {
        setModels([]);
        setSelectedModels([]);
        setTested(false);
        setCreateError('上游返回了空模型列表，请检查 API 地址、类型和 Key 权限');
        return;
      }
      setModels(result.models);
      setSelectedModels(result.models);
      setTested(true);
    } catch (reason) {
      setModels([]);
      setSelectedModels([]);
      setTested(false);
      setCreateError(reason instanceof Error ? reason.message : '获取模型失败');
    } finally {
      setTesting(false);
    }
  };

  const toggleModel = (model: string) => {
    setSelectedModels((current) => current.includes(model)
      ? current.filter((item) => item !== model)
      : [...current, model]);
  };

  const create = async () => {
    if (!tested) {
      setCreateError('请先获取这把 Key 支持的模型');
      return;
    }
    setSaving(true);
    try {
      const site = await api.createSite({ name: name.trim(), dashboardUrl: dashboardUrl.trim(), enabled });
      const credential = await api.createCredential(site.id, { name: credentialName.trim(), secret: secret.trim(), enabled: true });
      const endpoint = await api.createEndpoint(site.id, {
        name: `${credentialName.trim()} · ${selectedProfile.label}`,
        baseUrl: baseUrl.trim(),
        wireProtocol: selectedProfile.protocol,
        surface: selectedProfile.surface,
        adapterKind: 'generic',
        authScheme: selectedProfile.authScheme,
        headers: selectedProfile.protocol === 'anthropic' ? { 'anthropic-version': '2023-06-01' } : {},
        enabled: true,
      });
      await api.replaceEndpointCredentialBindings(site.id, endpoint.id, endpoint.revision, [credential.id]);
      if (selectedModels.length) await api.importModels(site.id, endpoint.id, credential.id, selectedModels);
      toast.show(`已添加 ${site.name}，获取到 ${selectedModels.length} 个模型`, 'success');
      setCreateOpen(false);
      resetCreate();
      navigate(`/sites/${site.id}`);
    } catch (reason) {
      const message = reason instanceof Error ? reason.message : '创建失败';
      setCreateError(message);
      toast.show(message, 'error');
    } finally {
      setSaving(false);
    }
  };

  const toggleSite = async (site: SiteRow, next: boolean) => {
    try {
      const updated = await api.updateSite(site.id, site.revision, { enabled: next });
      resource.setData((current) => current?.map((item) => item.id === site.id ? { ...item, ...updated } : item) || null);
      toast.show(next ? '站点已启用' : '站点已停用', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '更新失败', 'error');
      void resource.refresh();
    }
  };

  return (
    <div className="page sites-page">
      <PageHeader
        title="上游站点"
        description="集中管理网站、API Key、支持模型、真实余额和上游使用日志。"
        actions={<Button variant="primary" icon={Plus} onClick={() => setCreateOpen(true)}>添加站点</Button>}
      />

      <Panel className="list-panel">
        <FilterBar trailing={<span className="result-count">{rows.length} / {resource.data?.length || 0} 个站点</span>}>
          <SearchField value={query} onChange={setQuery} placeholder="搜索站点名称或地址" />
          <div className="segmented" role="group" aria-label="站点状态">
            {([['all', '全部'], ['enabled', '已启用'], ['disabled', '已停用']] as const).map(([value, label]) => (
              <button type="button" className={status === value ? 'is-active' : ''} onClick={() => setStatus(value)} key={value}>{label}</button>
            ))}
          </div>
        </FilterBar>

        {resource.loading && !resource.data ? <LoadingState label="正在读取上游站点" />
          : resource.error && !resource.data ? <ErrorState message={resource.error} onRetry={() => void resource.refresh()} />
            : rows.length === 0 ? <EmptyState
              title={resource.data?.length ? '没有匹配的站点' : '还没有上游站点'}
              description={resource.data?.length ? '调整搜索词或状态筛选。' : '添加网站和第一把 API Key 后即可自动获取模型。'}
              action={!resource.data?.length ? <Button icon={Plus} variant="primary" onClick={() => setCreateOpen(true)}>添加第一个站点</Button> : undefined}
            /> : <div className="site-table" role="list">
              {rows.map((site) => (
                <article className="site-row site-row-v2" role="listitem" key={site.id}>
                  <Link className="site-row-summary" to={`/sites/${site.id}`} aria-label={`查看 ${site.name} 详情`}>
                    <span className="site-avatar"><Server size={18} /></span>
                    <div className="site-identity">
                      <strong>{site.name}</strong>
                      <span>{site.dashboardUrl || '未设置站点地址'}</span>
                      <small>更新于 {formatDateTime(site.balanceUpdatedAt || site.updatedAt, true)}</small>
                    </div>
                    <div className="site-row-statuses">
                      <span><small>配置</small><Badge tone={site.enabled ? 'success' : 'neutral'}>{site.enabled ? '已启用' : '已停用'}</Badge></span>
                      <span><small>运行</small><HealthBadge state={site.runtimeHealth} /></span>
                    </div>
                    <div className="site-metrics">
                      <span><CircleDollarSign size={14} /><small>余额</small><strong>{site.balanceValue ? `${site.balanceValue} ${site.balanceUnit}` : '未连接'}</strong></span>
                      <span><KeyRound size={14} /><small>API Key</small><strong>{site.credentialCount}</strong></span>
                      <span><Layers3 size={14} /><small>支持模型</small><strong>{site.modelCount === null ? '未获取' : site.modelCount}</strong></span>
                    </div>
                    <ArrowRight className="site-row-chevron" size={16} />
                  </Link>
                  <div className="site-row-actions">
                    <Switch checked={site.enabled} label={site.enabled ? '启用' : '停用'} onChange={(next) => void toggleSite(site, next)} />
                    {site.dashboardUrl && <a className="icon-button" href={site.dashboardUrl} target="_blank" rel="noreferrer" aria-label="打开站点" title="打开站点"><ExternalLink size={16} /></a>}
                  </div>
                </article>
              ))}
            </div>}
      </Panel>

      <Dialog
        open={createOpen}
        title="添加上游站点"
        description="填写站点和第一把 API Key，系统会直接读取它支持的模型。"
        onClose={() => { setCreateOpen(false); resetCreate(); }}
        width="lg"
        footer={<>
          {createStep === 0
            ? <Button onClick={() => { setCreateOpen(false); resetCreate(); }}>取消</Button>
            : <Button icon={ArrowLeft} onClick={() => { setCreateError(''); setCreateStep((current) => current - 1); }}>上一步</Button>}
          {createStep < wizardSteps.length - 1
            ? <Button variant="primary" onClick={() => submitForm('create-site-form')}>下一步<ArrowRight size={15} /></Button>
            : <Button variant="primary" busy={saving} disabled={!tested} onClick={() => void create()}>完成接入</Button>}
        </>}
      >
        <div className="site-wizard-steps site-wizard-steps-three" aria-label="接入进度">
          {wizardSteps.map((step, index) => <div className={index === createStep ? 'is-current' : index < createStep ? 'is-complete' : ''} key={step}>
            <span>{index < createStep ? <Check size={13} /> : index + 1}</span><strong>{step}</strong>
          </div>)}
        </div>

        <form id="create-site-form" className="form-stack site-wizard-form" onSubmit={advanceCreate}>
          {createStep === 0 && <>
            <div className="form-grid two-columns">
              <Field label="站点名称" required hint="例如 Ciii、东京备用。"><input className="input" autoFocus value={name} onChange={(event) => setName(event.target.value)} maxLength={120} /></Field>
              <Field label="站点地址" required hint="用于打开站点、自动识别账户体系。"><input className="input" type="url" value={dashboardUrl} onChange={(event) => setDashboardUrl(event.target.value)} placeholder="https://relay.example.com" /></Field>
            </div>
            <Switch checked={enabled} label="接入完成后立即启用" onChange={setEnabled} />
          </>}

          {createStep === 1 && <>
            <div className="form-grid two-columns">
              <Field label="Key 名称" required><input className="input" autoFocus value={credentialName} onChange={(event) => setCredentialName(event.target.value)} /></Field>
              <Field label="API 类型" required><select className="select" value={profileId} onChange={(event) => { setProfileId(event.target.value); setTested(false); }}>
                {endpointProfiles.map((profile) => <option value={profile.id} key={profile.id}>{profile.label}</option>)}
              </select></Field>
            </div>
            <Field label="API 地址" required hint="默认可与站点地址相同，也可为这把 Key 单独修改。"><input className="input code-input" type="url" value={baseUrl} onChange={(event) => { setBaseUrl(event.target.value); setTested(false); }} placeholder={selectedProfile.baseUrlPlaceholder} /></Field>
            <Field label="上游 API Key" required><div className="secret-input">
              <input className="input code-input" type={secretVisible ? 'text' : 'password'} autoComplete="off" value={secret} onChange={(event) => { setSecret(event.target.value); setTested(false); }} placeholder="sk-..." />
              <IconButton label={secretVisible ? '隐藏 API Key' : '显示 API Key'} onClick={() => setSecretVisible((current) => !current)}>{secretVisible ? <EyeOff size={16} /> : <Eye size={16} />}</IconButton>
            </div></Field>
            <InlineNotice>站点账户登录稍后在“站点设置”中连接，只负责余额和上游日志。</InlineNotice>
          </>}

          {createStep === 2 && <div className="wizard-test-stage">
            <div className="wizard-test-card">
              <span className={tested ? 'is-success' : ''}>{tested ? <CircleCheck size={22} /> : <KeyRound size={22} />}</span>
              <div><strong>{tested ? '模型列表已获取' : '获取这把 Key 支持的模型'}</strong><p>{selectedProfile.label} · {baseUrl}</p></div>
              <Button busy={testing} variant={tested ? 'secondary' : 'primary'} onClick={() => void testConnection()}>{tested ? '重新获取' : '获取模型'}</Button>
            </div>
            {tested && <div className="wizard-model-picker">
              <header><div><strong>支持模型</strong><span>默认全部导入，之后可随时重新获取。</span></div><button type="button" className="text-link" onClick={() => setSelectedModels(selectedModels.length === models.length ? [] : models)}>{selectedModels.length === models.length ? '取消全选' : '全部选择'}</button></header>
              <div>{models.map((model) => <label className={selectedModels.includes(model) ? 'is-selected' : ''} key={model}><input type="checkbox" checked={selectedModels.includes(model)} onChange={() => toggleModel(model)} /><span><Check size={13} /></span><code>{model}</code></label>)}</div>
            </div>}
          </div>}
          {createError && <p className="form-error">{createError}</p>}
        </form>
      </Dialog>
    </div>
  );
}
