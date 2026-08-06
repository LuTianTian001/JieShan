import { FlaskConical, KeyRound, Pencil, Plus, Trash2 } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { Badge, Button, Dialog, EmptyState, Field, IconButton, Switch } from '../../components/ui';
import { useToast } from '../../components/Toast';
import { api } from '../../lib/api';
import { formatRelativeTime } from '../../lib/format';
import type { Protocol } from '../../lib/types';
import type { SiteCredentialView, UpstreamSiteView } from './siteAdapter';

interface Props {
  site: UpstreamSiteView;
  onChanged: () => Promise<void>;
}

export function ApiKeysTab({ site, onChanged }: Props) {
  const toast = useToast();
  const [addOpen, setAddOpen] = useState(false);
  const [alias, setAlias] = useState('');
  const [secret, setSecret] = useState('');
  const [busy, setBusy] = useState<string | null>(null);
  const [editing, setEditing] = useState<SiteCredentialView | null>(null);
  const [editAlias, setEditAlias] = useState('');
  const [editSecret, setEditSecret] = useState('');
  const [deleting, setDeleting] = useState<SiteCredentialView | null>(null);

  const addCredential = async (event: FormEvent) => {
    event.preventDefault();
    if (!secret.trim()) return;
    setBusy('add');
    try {
      const label = alias.trim() || `Key ${site.credentials.length + 1}`;
      if (site.sourceVersion === 'v2' && site.siteId !== null) {
        await api.createV2Credential(site.siteId, { name: label, apiKey: secret.trim(), enabled: true });
      } else {
        await api.createUpstream({
          name: `${site.name} / ${label}`,
          baseUrl: site.baseUrl,
          protocol: legacyProtocol(site),
          apiKey: secret.trim(),
        });
      }
      setAlias('');
      setSecret('');
      setAddOpen(false);
      await onChanged();
      toast.show('API Key 已添加到站点', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '添加 API Key 失败', 'error');
    } finally {
      setBusy(null);
    }
  };

  const toggleCredential = async (credential: SiteCredentialView, enabled: boolean) => {
    setBusy(`toggle-${credential.id}`);
    try {
      if (credential.v2 && credential.credentialId !== null) {
        await api.updateV2Credential(credential.credentialId, { enabled, revision: credential.revision ?? undefined });
      } else if (credential.legacy) {
        await api.updateUpstream(credential.legacy.id, {
          name: credential.legacy.name,
          baseUrl: credential.legacy.baseUrl,
          protocol: credential.legacy.protocol,
          enabled,
        });
      }
      await onChanged();
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '更新 API Key 失败', 'error');
    } finally {
      setBusy(null);
    }
  };

  const testCredential = async (credential: SiteCredentialView) => {
    setBusy(`test-${credential.id}`);
    try {
      if (site.sourceVersion === 'v2' && site.siteId !== null && credential.credentialId !== null) {
        const endpoint = site.endpoints.find((item) => item.enabled && item.endpointId !== null);
        if (!endpoint?.endpointId) throw new Error('请先配置并启用接入地址');
        const result = await api.discoverV2Models(site.siteId, {
          endpointId: endpoint.endpointId,
          credentialId: credential.credentialId,
          strategy: 'selected',
        });
        await onChanged();
        toast.show(`${credential.name} 验证成功，读取到 ${result.models.length} 个模型`, 'success');
      } else if (credential.upstreamId !== null) {
        await api.testUpstream(credential.upstreamId);
        toast.show(`${credential.name} 可正常读取模型`, 'success');
      }
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : 'API Key 验证失败', 'error');
    } finally {
      setBusy(null);
    }
  };

  const openEdit = (credential: SiteCredentialView) => {
    setEditing(credential);
    setEditAlias(credential.name);
    setEditSecret('');
  };

  const saveCredential = async (event: FormEvent) => {
    event.preventDefault();
    if (!editing || !editAlias.trim()) return;
    setBusy(`edit-${editing.id}`);
    try {
      if (editing.v2 && editing.credentialId !== null) {
        await api.updateV2Credential(editing.credentialId, {
          name: editAlias.trim(),
          apiKey: editSecret.trim() || undefined,
          enabled: editing.enabled,
          revision: editing.revision ?? undefined,
        });
      } else if (editing.legacy) {
        const suffix = site.credentials.length > 1 ? ` / ${editAlias.trim()}` : '';
        await api.updateUpstream(editing.legacy.id, {
          name: `${site.name}${suffix}`,
          baseUrl: editing.legacy.baseUrl,
          protocol: editing.legacy.protocol,
          enabled: editing.enabled,
          apiKey: editSecret.trim() || undefined,
        });
      }
      setEditing(null);
      setEditSecret('');
      await onChanged();
      toast.show('API Key 已更新', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '更新 API Key 失败', 'error');
    } finally {
      setBusy(null);
    }
  };

  const removeCredential = async () => {
    if (!deleting) return;
    setBusy(`delete-${deleting.id}`);
    try {
      if (deleting.v2 && deleting.credentialId !== null) {
        await api.deleteV2Credential(deleting.credentialId, deleting.revision ?? undefined);
      } else if (deleting.upstreamId !== null && site.credentials.length > 1) {
        await api.deleteUpstream(deleting.upstreamId);
      } else {
        throw new Error('旧版兼容站点至少需要保留一枚 Key');
      }
      setDeleting(null);
      await onChanged();
      toast.show('API Key 已移除', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '移除 API Key 失败', 'error');
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="detail-tab-stack">
      <section className="upstream-section">
        <div className="upstream-section-heading">
          <div><KeyRound size={17} /><div><h2>API Keys</h2><p>Key 只负责请求鉴权；余额和套餐统一归到站点账户。</p></div></div>
          <Button icon={Plus} variant="primary" disabled={site.sourceVersion === 'v2' && site.siteId === null} onClick={() => setAddOpen(true)}>添加 API Key</Button>
        </div>
        {site.credentials.length === 0 ? (
          <EmptyState title="还没有 API Key" description="添加至少一枚 Key 后才能获取模型并参与路由。" action={<Button icon={Plus} variant="primary" onClick={() => setAddOpen(true)}>添加 API Key</Button>} />
        ) : (
          <div className="credential-table" role="table" aria-label="站点 API Keys">
            <div className="credential-row credential-header" role="row"><span>名称</span><span>模型覆盖</span><span>最近验证</span><span>运行状态</span><span aria-label="操作" /></div>
            {site.credentials.map((credential) => {
              const state = credentialState(credential);
              return (
                <div className="credential-row" role="row" key={credential.id}>
                  <div className="credential-identity"><span className="credential-icon"><KeyRound size={15} /></span><div><strong>{credential.name}</strong><small>{credential.secretConfigured ? '凭据已加密保存' : '尚未配置凭据'}</small></div></div>
                  <div className="credential-metric"><strong>{credential.modelCount ?? '—'}</strong><small>{credential.modelCount === null ? '按模型查看覆盖' : '已发现模型'}</small></div>
                  <div className="credential-metric"><strong>{credential.lastSyncAt ? formatRelativeTime(credential.lastSyncAt) : '从未'}</strong><small>{credential.lastTestStatus === 'failed' ? '上次失败' : '模型接口'}</small></div>
                  <div className="credential-state"><div><Badge tone={state.tone}>{state.label}</Badge>{credential.lastErrorMessage && <small title={credential.lastErrorMessage}>{credential.lastErrorMessage}</small>}</div><Switch checked={credential.enabled} showLabel={false} disabled={busy === `toggle-${credential.id}`} label={`启用 ${credential.name}`} onChange={(enabled) => void toggleCredential(credential, enabled)} /></div>
                  <div className="credential-actions"><Button size="sm" icon={FlaskConical} busy={busy === `test-${credential.id}`} disabled={!credential.enabled} onClick={() => void testCredential(credential)}>验证</Button><IconButton label={`编辑 ${credential.name}`} onClick={() => openEdit(credential)}><Pencil size={16} /></IconButton><IconButton label={`移除 ${credential.name}`} disabled={site.sourceVersion === 'legacy' && site.credentials.length <= 1} onClick={() => setDeleting(credential)}><Trash2 size={16} /></IconButton></div>
                </div>
              );
            })}
          </div>
        )}
      </section>

      <Dialog open={addOpen} title="添加 API Key" description={site.name} onClose={() => setAddOpen(false)} footer={<><Button onClick={() => setAddOpen(false)}>取消</Button><Button type="submit" form="add-site-key-form" variant="primary" busy={busy === 'add'}>保存 API Key</Button></>}>
        <form id="add-site-key-form" className="site-create-form" onSubmit={(event) => void addCredential(event)}>
          <Field label="Key 名称"><input className="input" value={alias} onChange={(event) => setAlias(event.target.value)} placeholder={`Key ${site.credentials.length + 1}`} /></Field>
          <Field label="API Key"><input className="input" type="password" autoComplete="new-password" value={secret} onChange={(event) => setSecret(event.target.value)} placeholder="sk-..." required /></Field>
        </form>
      </Dialog>

      <Dialog open={Boolean(editing)} title="编辑 API Key" description="密钥留空时只更新名称。" onClose={() => setEditing(null)} footer={<><Button onClick={() => setEditing(null)}>取消</Button><Button type="submit" form="edit-site-key-form" variant="primary" busy={Boolean(editing && busy === `edit-${editing.id}`)}>保存修改</Button></>}>
        <form id="edit-site-key-form" className="site-create-form" onSubmit={(event) => void saveCredential(event)}>
          <Field label="Key 名称"><input className="input" value={editAlias} onChange={(event) => setEditAlias(event.target.value)} required /></Field>
          <Field label="替换 API Key" hint="不填写则保留当前密钥。"><input className="input" type="password" autoComplete="new-password" value={editSecret} onChange={(event) => setEditSecret(event.target.value)} placeholder="sk-..." /></Field>
        </form>
      </Dialog>

      <Dialog open={Boolean(deleting)} title="移除 API Key" description="删除后，该 Key 将立即停止参与请求。" width="sm" onClose={() => setDeleting(null)} footer={<><Button onClick={() => setDeleting(null)}>取消</Button><Button variant="danger" icon={Trash2} busy={Boolean(deleting && busy === `delete-${deleting.id}`)} onClick={() => void removeCredential()}>确认移除</Button></>}>
        {deleting && <div className="delete-summary"><strong>{deleting.name}</strong><span>{deleting.modelCount === null ? '模型覆盖按探针结果统计' : `${deleting.modelCount} 个已发现模型`}</span></div>}
      </Dialog>
    </div>
  );
}

function legacyProtocol(site: UpstreamSiteView): Protocol {
  const value = site.credentials.find((item) => item.legacy)?.legacy?.protocol;
  return value === 'openai' || value === 'anthropic' || value === 'gemini' || value === 'compatible' ? value : 'compatible';
}

function credentialState(credential: SiteCredentialView): { label: string; tone: 'neutral' | 'success' | 'warning' | 'danger' | 'info' } {
  if (!credential.enabled) return { label: '已停用', tone: 'neutral' };
  if (credential.state === 'healthy') return { label: '可用', tone: 'success' };
  if (credential.state === 'cooldown') return { label: '冷却中', tone: 'warning' };
  if (credential.state === 'suspect') return { label: '待复核', tone: 'warning' };
  if (credential.state === 'probing') return { label: '验证中', tone: 'info' };
  if (credential.state === 'credential_error') return { label: '凭据异常', tone: 'danger' };
  return { label: '尚未验证', tone: 'neutral' };
}
