import { Link2, Pencil, Plus, Save, Settings2, Trash2 } from 'lucide-react';
import { useEffect, useState, type FormEvent } from 'react';
import { Badge, Button, Dialog, Field, IconButton, Switch } from '../../components/ui';
import { useToast } from '../../components/Toast';
import { api } from '../../lib/api';
import { inferenceProtocolAuthScheme, inferenceProtocolHint, inferenceProtocolLabel, normalizeInferenceProtocol } from '../../lib/inferenceProtocols';
import type { Protocol } from '../../lib/types';
import type { SiteEndpointView, UpstreamSiteView } from './siteAdapter';

interface Props {
  site: UpstreamSiteView;
  onChanged: () => Promise<void>;
  onDeleted: () => void;
}

interface SettingsForm {
  name: string;
  dashboardUrl: string;
  baseUrl: string;
  enabled: boolean;
  protocol: Protocol;
}

interface EndpointForm {
  name: string;
  baseUrl: string;
  protocol: Protocol;
  enabled: boolean;
}

const emptyEndpoint: EndpointForm = { name: '', baseUrl: '', protocol: 'compatible', enabled: true };

export function SiteSettingsTab({ site, onChanged, onDeleted }: Props) {
  const toast = useToast();
  const [form, setForm] = useState<SettingsForm>(() => settingsFrom(site));
  const [saving, setSaving] = useState(false);
  const [endpointOpen, setEndpointOpen] = useState(false);
  const [endpointForm, setEndpointForm] = useState<EndpointForm>(emptyEndpoint);
  const [editingEndpoint, setEditingEndpoint] = useState<SiteEndpointView | null>(null);
  const [endpointBusy, setEndpointBusy] = useState(false);
  const [deletingEndpoint, setDeletingEndpoint] = useState<SiteEndpointView | null>(null);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);

  useEffect(() => setForm(settingsFrom(site)), [site]);

  const save = async (event: FormEvent) => {
    event.preventDefault();
    const name = form.name.trim();
    const baseUrl = form.baseUrl.trim();
    if (!name || (site.sourceVersion === 'legacy' && !baseUrl)) return;
    setSaving(true);
    try {
      if (site.sourceVersion === 'v2' && site.siteId !== null) {
        await api.updateV2Site(site.siteId, {
          name,
          dashboardUrl: form.dashboardUrl.trim(),
          enabled: form.enabled,
          revision: site.siteRevision ?? undefined,
        });
      } else {
        await Promise.all(site.credentials.map((credential, index) => {
          if (!credential.legacy) return Promise.resolve(null);
          const backendName = site.credentials.length === 1 ? name : `${name} / ${credential.name || `Key ${index + 1}`}`;
          return api.updateUpstream(credential.legacy.id, {
            name: backendName,
            baseUrl,
            protocol: form.protocol,
            enabled: form.enabled && credential.enabled,
          });
        }));
      }
      await onChanged();
      toast.show('站点设置已保存', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '保存站点设置失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  const openNewEndpoint = () => {
    setEditingEndpoint(null);
    setEndpointForm({ ...emptyEndpoint, name: `接入地址 ${site.endpoints.length + 1}` });
    setEndpointOpen(true);
  };

  const openEndpoint = (endpoint: SiteEndpointView) => {
    setEditingEndpoint(endpoint);
    setEndpointForm({
      name: endpoint.name,
      baseUrl: endpoint.baseUrl,
      protocol: normalizeInferenceProtocol(endpoint.protocol) ?? 'compatible',
      enabled: endpoint.enabled,
    });
    setEndpointOpen(true);
  };

  const saveEndpoint = async (event: FormEvent) => {
    event.preventDefault();
    if (site.siteId === null || !endpointForm.name.trim() || !endpointForm.baseUrl.trim()) return;
    setEndpointBusy(true);
    try {
      if (editingEndpoint?.endpointId !== null && editingEndpoint?.endpointId !== undefined) {
        await api.updateV2Endpoint(editingEndpoint.endpointId, {
          name: endpointForm.name.trim(),
          baseUrl: endpointForm.baseUrl.trim(),
          wireProtocol: endpointForm.protocol,
          authScheme: inferenceProtocolAuthScheme(endpointForm.protocol),
          enabled: endpointForm.enabled,
          revision: editingEndpoint.revision ?? undefined,
        });
      } else {
        await api.createV2Endpoint(site.siteId, {
          name: endpointForm.name.trim(),
          baseUrl: endpointForm.baseUrl.trim(),
          wireProtocol: endpointForm.protocol,
          compatibilityProfile: 'generic',
          authScheme: inferenceProtocolAuthScheme(endpointForm.protocol),
          enabled: endpointForm.enabled,
          revision: site.siteRevision ?? undefined,
        });
      }
      setEndpointOpen(false);
      await onChanged();
      toast.show(editingEndpoint ? '接入地址已更新' : '接入地址已添加', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '保存接入地址失败', 'error');
    } finally {
      setEndpointBusy(false);
    }
  };

  const removeEndpoint = async () => {
    if (!deletingEndpoint?.endpointId || site.endpoints.length <= 1) return;
    setEndpointBusy(true);
    try {
      await api.deleteV2Endpoint(deletingEndpoint.endpointId, deletingEndpoint.revision ?? undefined);
      setDeletingEndpoint(null);
      await onChanged();
      toast.show('接入地址已删除', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '删除接入地址失败', 'error');
    } finally {
      setEndpointBusy(false);
    }
  };

  const removeSite = async () => {
    setDeleting(true);
    try {
      if (site.sourceVersion === 'v2' && site.siteId !== null) {
        await api.deleteV2Site(site.siteId, site.siteRevision ?? undefined);
      } else {
        await Promise.all(site.memberUpstreamIds.map((id) => api.deleteUpstream(id)));
      }
      toast.show('站点已删除', 'success');
      onDeleted();
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '删除站点失败', 'error');
    } finally {
      setDeleting(false);
    }
  };

  const isV2 = site.sourceVersion === 'v2';

  return (
    <div className="detail-tab-stack">
      <section className="upstream-section">
        <div className="upstream-section-heading"><div><Settings2 size={17} /><div><h2>站点设置</h2><p>网站身份、控制台地址和总开关。</p></div></div><Badge tone={isV2 ? 'success' : 'neutral'}>{isV2 ? '网站级数据' : '旧版兼容数据'}</Badge></div>
        <form className="site-settings-form" onSubmit={(event) => void save(event)}>
          <Field label="站点名称"><input className="input" value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} required /></Field>
          <Field label="站点网址" hint={isV2 ? '用于打开网站控制台；不等于 API 地址。' : '旧版数据会从账户连接中读取。'}><input className="input" type="url" value={form.dashboardUrl} disabled={!isV2} onChange={(event) => setForm({ ...form, dashboardUrl: event.target.value })} placeholder="https://example.com" /></Field>
          {!isV2 && <Field label="API 地址"><input className="input" type="url" value={form.baseUrl} onChange={(event) => setForm({ ...form, baseUrl: event.target.value })} required /></Field>}
          {!isV2 && <Field label="API 格式" hint={inferenceProtocolHint(form.protocol)}><ProtocolSelect value={form.protocol} onChange={(protocol) => setForm({ ...form, protocol })} /></Field>}
          <div className="site-setting-switch"><Switch checked={form.enabled} label="启用此站点" onChange={(enabled) => setForm({ ...form, enabled })} /></div>
          <div className="site-settings-actions"><Button type="submit" variant="primary" icon={Save} busy={saving}>保存设置</Button></div>
        </form>
      </section>

      {isV2 && (
        <section className="upstream-section">
          <div className="upstream-section-heading"><div><Link2 size={17} /><div><h2>接入地址</h2><p>一个网站可以有多个 API 地址；每个地址会明确区分模型获取与路由能力。</p></div></div><Button icon={Plus} onClick={openNewEndpoint}>添加地址</Button></div>
          <div className="endpoint-settings-list">
            {site.endpoints.map((endpoint) => (
              <div className="endpoint-settings-row" key={endpoint.id}>
                <div><strong>{endpoint.name}</strong><code>{endpoint.baseUrl}</code></div>
                <div><Badge tone={endpoint.enabled ? 'success' : 'neutral'}>{endpoint.enabled ? '已启用' : '已停用'}</Badge><Badge tone={endpoint.capabilities.routeEligible ? 'success' : 'warning'}>{endpoint.capabilities.routeEligible ? '可路由' : '仅获取模型'}</Badge><span>{inferenceProtocolLabel(endpoint.protocol)}</span></div>
                <div><IconButton label={`编辑 ${endpoint.name}`} onClick={() => openEndpoint(endpoint)}><Pencil size={16} /></IconButton><IconButton label={`删除 ${endpoint.name}`} disabled={site.endpoints.length <= 1} onClick={() => setDeletingEndpoint(endpoint)}><Trash2 size={16} /></IconButton></div>
              </div>
            ))}
          </div>
        </section>
      )}

      <section className="upstream-danger-zone">
        <div><strong>删除站点</strong><span>删除网站、全部 API Key、模型和相关路由目标。</span></div>
        <Button variant="danger" icon={Trash2} onClick={() => setDeleteOpen(true)}>删除站点</Button>
      </section>

      <Dialog open={endpointOpen} title={editingEndpoint ? '编辑接入地址' : '添加接入地址'} description="API 地址与网站控制台地址彼此独立。" onClose={() => setEndpointOpen(false)} footer={<><Button onClick={() => setEndpointOpen(false)}>取消</Button><Button type="submit" form="endpoint-settings-form" variant="primary" busy={endpointBusy}>保存地址</Button></>}>
        <form id="endpoint-settings-form" className="site-create-form" onSubmit={(event) => void saveEndpoint(event)}>
          <Field label="名称"><input className="input" value={endpointForm.name} onChange={(event) => setEndpointForm({ ...endpointForm, name: event.target.value })} placeholder="主要接入地址" required /></Field>
          <Field label="API 地址"><input className="input" type="url" value={endpointForm.baseUrl} onChange={(event) => setEndpointForm({ ...endpointForm, baseUrl: event.target.value })} placeholder="https://example.com/v1" required /></Field>
          <Field label="API 格式" hint={inferenceProtocolHint(endpointForm.protocol)}><ProtocolSelect value={endpointForm.protocol} onChange={(protocol) => setEndpointForm({ ...endpointForm, protocol })} /></Field>
          <Switch checked={endpointForm.enabled} label="启用此接入地址" onChange={(enabled) => setEndpointForm({ ...endpointForm, enabled })} />
        </form>
      </Dialog>

      <Dialog open={Boolean(deletingEndpoint)} title="删除接入地址" description="该地址下发现的模型和关联路由可能同时失效。" width="sm" onClose={() => setDeletingEndpoint(null)} footer={<><Button onClick={() => setDeletingEndpoint(null)}>取消</Button><Button variant="danger" icon={Trash2} busy={endpointBusy} onClick={() => void removeEndpoint()}>确认删除</Button></>}>
        {deletingEndpoint && <div className="delete-summary"><strong>{deletingEndpoint.name}</strong><code>{deletingEndpoint.baseUrl}</code></div>}
      </Dialog>

      <Dialog open={deleteOpen} title="删除整个站点" description="此操作会删除该网站下的全部 API Key、模型和关联数据。" width="sm" onClose={() => setDeleteOpen(false)} footer={<><Button onClick={() => setDeleteOpen(false)}>取消</Button><Button variant="danger" icon={Trash2} busy={deleting} onClick={() => void removeSite()}>确认删除</Button></>}>
        <div className="delete-summary"><strong>{site.name}</strong><code>{site.origin}</code><span>{site.credentials.length} 枚 API Key · {site.modelCount} 个模型</span></div>
      </Dialog>
    </div>
  );
}

function ProtocolSelect({ value, onChange }: { value: Protocol; onChange: (value: Protocol) => void }) {
  return <select className="select" value={value} onChange={(event) => onChange(event.target.value as Protocol)}><option value="compatible">OpenAI 兼容 · 可路由</option><option value="openai">OpenAI 官方 · 可路由</option><option value="anthropic">Anthropic 原生 · 仅获取模型</option><option value="gemini">Gemini 原生 · 仅获取模型</option></select>;
}

function settingsFrom(site: UpstreamSiteView): SettingsForm {
  const protocol = site.endpoints[0]?.protocol ?? site.credentials.find((item) => item.legacy)?.legacy?.protocol;
  return {
    name: site.name,
    dashboardUrl: site.dashboardUrl,
    baseUrl: site.baseUrl,
    enabled: site.enabled,
    protocol: normalizeInferenceProtocol(protocol) ?? 'compatible',
  };
}
