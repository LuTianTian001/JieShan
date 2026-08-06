import { Copy, ListFilter, Pencil, Plus, RefreshCw, Route as RouteIcon, ShieldCheck, Trash2 } from 'lucide-react';
import { useMemo, useState, type FormEvent } from 'react';
import { Badge, Button, Dialog, EmptyState, ErrorState, Field, IconButton, LoadingState, Metric, PageHeader, ProgressBar, Surface, Switch } from '../components/ui';
import { useToast } from '../components/Toast';
import { api } from '../lib/api';
import { formatDateTime, formatRelativeTime, formatUsd } from '../lib/format';
import { useAsyncData } from '../lib/hooks';
import type { CreateKeyInput, DownstreamKey, UpdateKeyInput } from '../lib/types';

const emptyForm: CreateKeyInput = { name: '', quotaUsd: 10, allowedModels: [], routingProfileId: null, rpmLimit: 60, expiresAt: null };

export function KeysPage() {
  const toast = useToast();
  const keysState = useAsyncData(() => api.keys(), []);
  const routesState = useAsyncData(() => api.routes(), []);
  const publishedState = useAsyncData(() => api.v2PublishedModels(), []);
  const profilesState = useAsyncData(() => api.routingProfiles(), []);
  const [query, setQuery] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [form, setForm] = useState<CreateKeyInput>(emptyForm);
  const [unlimited, setUnlimited] = useState(false);
  const [saving, setSaving] = useState(false);
  const [secret, setSecret] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<number | null>(null);
  const [editingKey, setEditingKey] = useState<DownstreamKey | null>(null);
  const [deletingKey, setDeletingKey] = useState<DownstreamKey | null>(null);
  const [editorEnabled, setEditorEnabled] = useState(true);

  const keys = keysState.data ?? [];
  const models = useMemo(() => [...new Set([
    ...(publishedState.data ?? []).map((model) => model.publicName),
    ...(routesState.data ?? []).map((route) => route.model),
  ])].sort((left, right) => left.localeCompare(right)), [publishedState.data, routesState.data]);
  const profiles = profilesState.data ?? [];
  const filtered = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return keys.filter((key) => !normalized || `${key.name} ${key.prefix}`.toLowerCase().includes(normalized));
  }, [keys, query]);
  const totalSpent = keys.reduce((sum, key) => sum + key.spentUsd, 0);
  const finiteQuota = keys.reduce((sum, key) => sum + (key.quotaUsd ?? 0), 0);
  const finiteRemaining = keys.reduce((sum, key) => sum + Math.max(0, (key.quotaUsd ?? 0) - key.spentUsd), 0);

  const openCreate = () => {
    setEditingKey(null);
    setForm(emptyForm);
    setUnlimited(false);
    setEditorEnabled(true);
    setCreateOpen(true);
  };

  const openEdit = (key: DownstreamKey) => {
    setEditingKey(key);
    setForm({ name: key.name, quotaUsd: key.quotaUsd, allowedModels: [...key.allowedModels], routingProfileId: key.routingProfileId, rpmLimit: key.rpmLimit ?? 0, expiresAt: key.expiresAt });
    setUnlimited(key.quotaUsd == null);
    setEditorEnabled(key.enabled);
    setCreateOpen(true);
  };

  const submitKey = async (event: FormEvent) => {
    event.preventDefault();
    if (!form.name.trim()) return;
    setSaving(true);
    try {
      if (editingKey) {
        const payload: UpdateKeyInput = {
          name: form.name.trim(),
          enabled: editorEnabled,
          rpmLimit: form.rpmLimit ?? 0,
          allowedModels: form.allowedModels,
          expiresAt: form.expiresAt ?? '',
        };
        if (form.routingProfileId == null) payload.clearRoutingProfile = true;
        else payload.routingProfileId = form.routingProfileId;
        if (unlimited) payload.clearQuota = true;
        else payload.quotaUsd = form.quotaUsd ?? 0;
        const updated = await api.updateKey(editingKey.id, payload);
        keysState.setData((current) => current?.map((item) => item.id === updated.id ? updated : item) ?? [updated]);
        toast.show('密钥配置已更新', 'success');
      } else {
        const result = await api.createKey({ ...form, name: form.name.trim(), quotaUsd: unlimited ? null : form.quotaUsd });
        keysState.setData((current) => [result.item, ...(current ?? [])]);
        setSecret(result.secret);
        toast.show('下游密钥已创建', 'success');
      }
      setCreateOpen(false);
      setEditingKey(null);
      setForm(emptyForm);
      setUnlimited(false);
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '保存失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  const deleteKey = async () => {
    if (!deletingKey) return;
    setBusyId(deletingKey.id);
    try {
      await api.deleteKey(deletingKey.id);
      keysState.setData((current) => current?.filter((item) => item.id !== deletingKey.id) ?? []);
      setDeletingKey(null);
      toast.show('密钥已删除', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '删除失败', 'error');
    } finally {
      setBusyId(null);
    }
  };

  const toggleKey = async (key: DownstreamKey, enabled: boolean) => {
    setBusyId(key.id);
    try {
      const updated = await api.updateKey(key.id, { enabled });
      keysState.setData((current) => current?.map((item) => item.id === updated.id ? updated : item) ?? [updated]);
      toast.show(enabled ? '密钥已启用' : '密钥已停用', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '保存失败', 'error');
    } finally {
      setBusyId(null);
    }
  };

  const copySecret = async () => {
    if (!secret) return;
    await navigator.clipboard.writeText(secret);
    toast.show('密钥已复制', 'success');
  };

  if (keysState.loading && !keysState.data) return <div className="page"><LoadingState label="正在读取密钥" /></div>;
  if (keysState.error && !keysState.data) return <div className="page"><ErrorState message={keysState.error} onRetry={() => void keysState.refresh()} /></div>;

  return (
    <div className="page">
      <PageHeader title="下游密钥" description="用于分发调用权限。每把密钥可使用默认路由，也可绑定一个命名路由方案。" actions={<Button variant="primary" icon={Plus} onClick={openCreate}>创建密钥</Button>} />
      <div className="metric-grid key-metrics">
        <Metric label="有效密钥" value={keys.filter((key) => key.enabled).length} hint={`共 ${keys.length} 个`} tone="success" />
        <Metric label="累计消耗" value={formatUsd(totalSpent)} hint="官方目录价格" />
        <Metric label="有限额度" value={formatUsd(finiteQuota)} hint="不含无限额度密钥" />
        <Metric label="有限额度剩余" value={formatUsd(finiteRemaining)} hint="事务预占后结算" tone={finiteRemaining < finiteQuota * 0.2 ? 'warning' : 'neutral'} />
      </div>
      <Surface>
        <div className="toolbar">
          <div className="search-box"><ListFilter size={15} /><input className="input" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索名称或前缀" /></div>
          <span className="toolbar-spacer" />
          <Button size="sm" variant="ghost" icon={RefreshCw} busy={keysState.loading || profilesState.loading || publishedState.loading} onClick={() => { void keysState.refresh(); void profilesState.refresh(); void publishedState.refresh(); void routesState.refresh(); }}>刷新</Button>
        </div>
        {filtered.length === 0 ? <EmptyState title="还没有下游密钥" description="创建密钥后即可控制额度、模型范围和调用速率。" action={<Button variant="primary" icon={Plus} onClick={openCreate}>创建密钥</Button>} /> : <>
          <div className="data-scroller key-desktop-table"><table className="data-table key-table">
            <thead><tr><th>密钥</th><th>状态</th><th>额度 / 已用</th><th>模型范围</th><th>路由方案</th><th>速率</th><th>最后调用</th><th>到期</th><th aria-label="操作" /></tr></thead>
            <tbody>{filtered.map((key) => {
              const usagePercent = key.quotaUsd == null ? 0 : key.quotaUsd === 0 ? 100 : key.spentUsd / key.quotaUsd * 100;
              return <tr key={key.id}>
                <td><div className="cell-main"><strong>{key.name}</strong><code>{key.prefix}••••••••</code></div></td>
                <td><Badge tone={key.enabled ? 'success' : 'neutral'}>{key.enabled ? '启用' : '停用'}</Badge></td>
                <td><div className="quota-cell"><span><strong>{key.quotaUsd == null ? '无限' : formatUsd(key.quotaUsd, 2)}</strong><small>已用 {formatUsd(key.spentUsd)}</small></span>{key.quotaUsd != null && <ProgressBar value={usagePercent} tone={usagePercent > 90 ? 'danger' : usagePercent > 70 ? 'warning' : 'accent'} />}</div></td>
                <td><Badge>{key.allowedModels.length ? `${key.allowedModels.length} 个模型` : '全部已计价模型'}</Badge></td>
                <td><div className="key-route-profile"><RouteIcon size={14} /><span><strong>{key.usesDefaultRouting || key.routingProfileId == null ? '默认路由' : key.routingProfileName}</strong><small>{key.usesDefaultRouting || key.routingProfileId == null ? '跟随模型默认顺序' : '按模型覆盖，未配置则回退默认'}</small></span></div></td>
                <td>{key.rpmLimit ? `${key.rpmLimit} RPM` : '不限'}</td>
                <td>{formatRelativeTime(key.lastUsedAt)}</td>
                <td>{key.expiresAt ? formatDateTime(key.expiresAt) : '永不过期'}</td>
                <td><div className="row-actions"><Switch checked={key.enabled} disabled={busyId === key.id} onChange={(checked) => void toggleKey(key, checked)} label={`${key.enabled ? '停用' : '启用'} ${key.name}`} showLabel={false} /><IconButton label={`编辑 ${key.name}`} disabled={busyId === key.id} onClick={() => openEdit(key)}><Pencil size={16} /></IconButton><IconButton label={`删除 ${key.name}`} disabled={busyId === key.id} onClick={() => setDeletingKey(key)}><Trash2 size={16} /></IconButton></div></td>
              </tr>;
            })}</tbody>
          </table></div>
          <div className="key-mobile-list">
            {filtered.map((key) => <KeyMobileCard key={key.id} item={key} busy={busyId === key.id} onToggle={(enabled) => void toggleKey(key, enabled)} onEdit={() => openEdit(key)} onDelete={() => setDeletingKey(key)} />)}
          </div>
        </>}
      </Surface>

      <Dialog open={createOpen} title={editingKey ? '编辑下游密钥' : '创建下游密钥'} description={editingKey ? '修改额度、模型范围、速率和到期时间。' : '密钥原文仅在创建成功后显示一次。'} onClose={() => { setCreateOpen(false); setEditingKey(null); }} width="lg" footer={<><Button onClick={() => { setCreateOpen(false); setEditingKey(null); }}>取消</Button><Button type="submit" form="key-form" variant="primary" busy={saving}>{editingKey ? '保存更改' : '创建密钥'}</Button></>}>
        <form id="key-form" className="form-grid" onSubmit={(event) => void submitKey(event)}>
          <Field label="名称"><input className="input" value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} placeholder="例如：个人主密钥" /></Field>
          <Field label="每分钟请求上限" hint="设为 0 表示不单独限制 RPM。"><input className="input" type="number" min="0" value={form.rpmLimit ?? ''} onChange={(event) => setForm({ ...form, rpmLimit: event.target.value ? Number(event.target.value) : 0 })} /></Field>
          <div className="field-span-2 quota-editor"><Switch checked={unlimited} onChange={setUnlimited} label="无限额度" /><Field label="额度（USD）" hint={unlimited ? '保存后不限制总额度，RPM 和模型范围仍然生效。' : '扣费使用请求开始时锁定的官方价格快照。'}><input className="input" type="number" min="0" step="0.01" disabled={unlimited} value={form.quotaUsd ?? ''} onChange={(event) => setForm({ ...form, quotaUsd: event.target.value ? Number(event.target.value) : 0 })} /></Field></div>
          <Field label="到期时间" hint="留空表示永不过期。"><input className="input" type="datetime-local" value={form.expiresAt?.slice(0, 16) ?? ''} onChange={(event) => setForm({ ...form, expiresAt: event.target.value ? new Date(event.target.value).toISOString() : null })} /></Field>
          <Field label="路由方案" hint="命名方案只覆盖已配置的模型，其他模型自动使用默认路由。"><select className="select" value={form.routingProfileId ?? ''} onChange={(event) => setForm({ ...form, routingProfileId: event.target.value ? Number(event.target.value) : null })}><option value="">默认路由</option>{profiles.map((profile) => <option value={profile.id} key={profile.id}>{profile.name}</option>)}</select></Field>
          {editingKey && <div className="field-span-2"><Switch checked={editorEnabled} onChange={setEditorEnabled} label="启用此密钥" /></div>}
          <div className="field-span-2"><span className="field-label">允许模型</span><div className="model-scope-grid">{models.map((model) => { const selected = form.allowedModels.includes(model); return <label className={selected ? 'is-selected' : ''} key={model}><input type="checkbox" checked={selected} onChange={() => setForm({ ...form, allowedModels: selected ? form.allowedModels.filter((item) => item !== model) : [...form.allowedModels, model] })} /><code>{model}</code></label>; })}</div><span className="field-hint">不选择表示允许所有已计价模型。</span></div>
        </form>
      </Dialog>

      <Dialog open={Boolean(deletingKey)} title="删除下游密钥" description="删除后使用此密钥的客户端会立即失去访问权限。" onClose={() => setDeletingKey(null)} width="sm" footer={<><Button onClick={() => setDeletingKey(null)}>取消</Button><Button variant="danger" icon={Trash2} busy={Boolean(deletingKey && busyId === deletingKey.id)} onClick={() => void deleteKey()}>确认删除</Button></>}>
        {deletingKey && <div className="delete-summary"><strong>{deletingKey.name}</strong><code>{deletingKey.prefix}••••••••</code><span>已用 {formatUsd(deletingKey.spentUsd)}</span></div>}
      </Dialog>

      <Dialog open={Boolean(secret)} title="密钥已创建" description="关闭后无法再次查看完整密钥。" onClose={() => setSecret(null)} footer={<><Button icon={Copy} onClick={() => void copySecret()}>复制密钥</Button><Button variant="primary" onClick={() => setSecret(null)}>我已保存</Button></>}>
        <div className="secret-reveal"><ShieldCheck size={22} /><code>{secret}</code><p>JieShan 仅保存安全摘要。请把这段密钥配置到你的客户端。</p></div>
      </Dialog>
    </div>
  );
}

function KeyMobileCard({ item, busy, onToggle, onEdit, onDelete }: { item: DownstreamKey; busy: boolean; onToggle: (enabled: boolean) => void; onEdit: () => void; onDelete: () => void }) {
  const usagePercent = item.quotaUsd == null ? 0 : item.quotaUsd === 0 ? 100 : item.spentUsd / item.quotaUsd * 100;
  const usesDefaultRouting = item.usesDefaultRouting || item.routingProfileId == null;
  return (
    <article className="key-mobile-card">
      <header className="key-mobile-heading">
        <div className="cell-main"><strong>{item.name}</strong><code>{item.prefix}••••••••</code></div>
        <Badge tone={item.enabled ? 'success' : 'neutral'}>{item.enabled ? '启用' : '停用'}</Badge>
      </header>
      <div className="key-mobile-quota">
        <span><small>额度</small><strong>{item.quotaUsd == null ? '无限' : formatUsd(item.quotaUsd, 2)}</strong></span>
        <span><small>已用</small><strong>{formatUsd(item.spentUsd)}</strong></span>
        {item.quotaUsd != null && <ProgressBar value={usagePercent} tone={usagePercent > 90 ? 'danger' : usagePercent > 70 ? 'warning' : 'accent'} />}
      </div>
      <dl className="key-mobile-facts">
        <div><dt>模型范围</dt><dd>{item.allowedModels.length ? `${item.allowedModels.length} 个模型` : '全部已计价模型'}</dd></div>
        <div><dt>速率</dt><dd>{item.rpmLimit ? `${item.rpmLimit} RPM` : '不限'}</dd></div>
        <div className="key-mobile-route"><dt>路由方案</dt><dd><RouteIcon size={13} />{usesDefaultRouting ? '默认路由' : item.routingProfileName}</dd><small>{usesDefaultRouting ? '跟随模型默认顺序' : '未配置模型回退默认路由'}</small></div>
        <div><dt>最后调用</dt><dd>{formatRelativeTime(item.lastUsedAt)}</dd></div>
        <div><dt>到期</dt><dd>{item.expiresAt ? formatDateTime(item.expiresAt) : '永不过期'}</dd></div>
      </dl>
      <footer className="key-mobile-actions">
        <Switch checked={item.enabled} disabled={busy} onChange={onToggle} label={`${item.enabled ? '停用' : '启用'} ${item.name}`} showLabel={false} />
        <span />
        <Button size="sm" variant="ghost" icon={Pencil} disabled={busy} onClick={onEdit}>编辑</Button>
        <Button size="sm" variant="ghost" icon={Trash2} disabled={busy} onClick={onDelete}>删除</Button>
      </footer>
    </article>
  );
}
