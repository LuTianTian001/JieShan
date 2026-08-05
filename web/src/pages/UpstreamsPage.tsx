import { Check, CircleDollarSign, Eye, FlaskConical, KeyRound, ListFilter, Pencil, Plus, RefreshCw, Server, Trash2, Waypoints } from 'lucide-react';
import { useMemo, useState, type FormEvent } from 'react';
import { Badge, Button, Dialog, Drawer, EmptyState, ErrorState, Field, LoadingState, PageHeader, SectionHeader, StatusBadge, Surface, Switch, Tabs } from '../components/ui';
import { useToast } from '../components/Toast';
import { api } from '../lib/api';
import { formatDateTime, formatLatency, formatRelativeTime } from '../lib/format';
import { useAsyncData } from '../lib/hooks';
import type { CreateUpstreamInput, ModelDiscovery, Protocol, Upstream, UpdateUpstreamInput } from '../lib/types';

type UpstreamTab = 'overview' | 'models' | 'account';

const protocolLabels: Record<Protocol, string> = {
  openai: 'OpenAI',
  anthropic: 'Anthropic',
  gemini: 'Gemini',
  compatible: '兼容协议',
};

const creatableProtocols: Protocol[] = ['openai', 'compatible'];

const emptyForm: CreateUpstreamInput = { name: '', baseUrl: '', protocol: 'openai', apiKey: '' };

function BalanceBlock({ upstream }: { upstream: Upstream }) {
  const supported = upstream.balanceSupported === true;
  const balance = upstream.balance;
  return (
    <div className="detail-block balance-block">
      <div className="detail-block-heading"><div><CircleDollarSign size={17} /><strong>站点账户</strong></div><Badge tone={supported ? 'success' : 'neutral'}>{supported ? '已配置' : '未配置'}</Badge></div>
      {!supported ? <p className="muted-copy">当前版本尚未配置该站点的余额适配器，因此不会调用占位接口或展示推测数据。</p> : balance ? (
        <div className="balance-value">
          <strong>{new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 4 }).format(balance.amount)}</strong>
          <span>{balance.currency}</span>
          {balance.sourceLabel && <Badge tone="neutral">{balance.sourceLabel}</Badge>}
        </div>
      ) : <p className="muted-copy">适配器已启用，但尚未返回余额数据。</p>}
      {supported && balance?.plan && <dl className="compact-definition"><div><dt>套餐</dt><dd>{balance.plan}</dd></div><div><dt>续期</dt><dd>{formatDateTime(balance.renewalAt || null)}</dd></div></dl>}
    </div>
  );
}

export function UpstreamsPage() {
  const toast = useToast();
  const state = useAsyncData(() => api.upstreams(), []);
  const [query, setQuery] = useState('');
  const [addOpen, setAddOpen] = useState(false);
  const [form, setForm] = useState<CreateUpstreamInput>(emptyForm);
  const [saving, setSaving] = useState(false);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [tab, setTab] = useState<UpstreamTab>('overview');
  const [busyAction, setBusyAction] = useState<string | null>(null);
  const [discovery, setDiscovery] = useState<ModelDiscovery | null>(null);
  const [editing, setEditing] = useState<Upstream | null>(null);
  const [editForm, setEditForm] = useState<UpdateUpstreamInput | null>(null);
  const [deleting, setDeleting] = useState<Upstream | null>(null);

  const upstreams = state.data ?? [];
  const selected = upstreams.find((item) => item.id === selectedId) ?? null;
  const filtered = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return upstreams.filter((item) => !normalized || `${item.name} ${item.baseUrl} ${item.protocol}`.toLowerCase().includes(normalized));
  }, [query, upstreams]);

  const replaceUpstream = (updated: Upstream) => state.setData((current) => current?.map((item) => item.id === updated.id ? updated : item) ?? [updated]);

  const openDetail = (upstream: Upstream) => {
    setSelectedId(upstream.id);
    setTab('overview');
    setDiscovery(null);
  };

  const openEdit = (upstream: Upstream) => {
    setSelectedId(null);
    setEditing(upstream);
    setEditForm({ name: upstream.name, baseUrl: upstream.baseUrl, protocol: upstream.protocol, enabled: upstream.enabled, apiKey: '' });
  };

  const create = async (event: FormEvent) => {
    event.preventDefault();
    if (!form.name.trim() || !form.baseUrl.trim() || !form.apiKey.trim()) return;
    setSaving(true);
    try {
      const created = await api.createUpstream({ ...form, name: form.name.trim(), baseUrl: form.baseUrl.trim(), apiKey: form.apiKey.trim() });
      state.setData((current) => [created, ...(current ?? [])]);
      setForm(emptyForm);
      setAddOpen(false);
      setSelectedId(created.id);
      toast.show('上游已添加，建议立即测试并获取模型', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '添加失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  const saveEdit = async (event: FormEvent) => {
    event.preventDefault();
    if (!editing || !editForm || !editForm.name.trim() || !editForm.baseUrl.trim()) return;
    setSaving(true);
    try {
      const payload: UpdateUpstreamInput = { ...editForm, name: editForm.name.trim(), baseUrl: editForm.baseUrl.trim() };
      if (!payload.apiKey?.trim()) delete payload.apiKey;
      const updated = await api.updateUpstream(editing.id, payload);
      replaceUpstream(updated);
      setEditing(null);
      setEditForm(null);
      toast.show('上游配置已更新，健康状态将重新确认', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '更新失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  const deleteUpstream = async () => {
    if (!deleting) return;
    setBusyAction(`delete-${deleting.id}`);
    try {
      await api.deleteUpstream(deleting.id);
      state.setData((current) => current?.filter((item) => item.id !== deleting.id) ?? []);
      if (selectedId === deleting.id) setSelectedId(null);
      setDeleting(null);
      toast.show('上游已删除，相关路由目标已同步移除', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '删除失败', 'error');
    } finally {
      setBusyAction(null);
    }
  };

  const test = async (upstream: Upstream) => {
    setBusyAction(`test-${upstream.id}`);
    try {
      const updated = await api.testUpstream(upstream.id);
      replaceUpstream(updated);
      toast.show(`${upstream.name} 连接正常`, 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '连接测试失败', 'error');
    } finally {
      setBusyAction(null);
    }
  };

  const discover = async (upstream: Upstream) => {
    setBusyAction(`discover-${upstream.id}`);
    try {
      const result = await api.discoverModels(upstream.id);
      setDiscovery(result);
      setSelectedId(upstream.id);
      setTab('models');
      toast.show(`发现完成：新增 ${result.added.length} 个模型`, 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '模型发现失败', 'error');
    } finally {
      setBusyAction(null);
    }
  };

  const applyDiscovery = async () => {
    if (!selected || !discovery) return;
    setBusyAction(`apply-${selected.id}`);
    try {
      replaceUpstream(await api.applyModels(selected.id, discovery));
      setDiscovery(null);
      toast.show('模型列表已原子更新', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '应用失败', 'error');
    } finally {
      setBusyAction(null);
    }
  };

  if (state.loading && !state.data) return <div className="page"><LoadingState label="正在读取上游" /></div>;
  if (state.error && !state.data) return <div className="page"><ErrorState message={state.error} onRetry={() => void state.refresh()} /></div>;

  return (
    <div className="page">
      <PageHeader title="上游管理" description="管理 API Key 上游、连接状态与模型发现。余额和套餐仅按站点原始数据展示。" actions={<Button variant="primary" icon={Plus} onClick={() => setAddOpen(true)}>添加上游</Button>} />
      <Surface>
        <div className="toolbar">
          <div className="search-box"><ListFilter size={15} /><input className="input" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索名称、地址或协议" /></div>
          <span className="toolbar-note">{upstreams.filter((item) => item.enabled).length} 个已启用</span>
          <span className="toolbar-spacer" />
          <Button size="sm" variant="ghost" icon={RefreshCw} busy={state.loading} onClick={() => void state.refresh()}>刷新</Button>
        </div>
        {filtered.length === 0 ? <EmptyState title="还没有上游" description="添加第一个 API Key 上游后，即可发现模型并配置路由。" action={<Button variant="primary" icon={Plus} onClick={() => setAddOpen(true)}>添加上游</Button>} /> : (
          <div className="data-scroller">
            <table className="data-table upstream-table">
              <thead><tr><th>上游</th><th>状态</th><th>协议</th><th>模型</th><th>延迟</th><th>最近同步</th><th aria-label="操作" /></tr></thead>
              <tbody>{filtered.map((upstream) => (
                <tr key={upstream.id}>
                  <td><div className="cell-main"><button type="button" className="table-primary-link" onClick={() => openDetail(upstream)}>{upstream.name}</button><code>{upstream.baseUrl}</code></div></td>
                  <td><StatusBadge state={upstream.enabled ? upstream.state : 'disabled'} /></td>
                  <td><Badge>{protocolLabels[upstream.protocol]}</Badge></td>
                  <td className="numeric">{upstream.modelCount}</td>
                  <td className="numeric">{formatLatency(upstream.latencyMs)}</td>
                  <td>{formatRelativeTime(upstream.lastSyncAt)}</td>
                  <td><div className="row-actions"><Button size="sm" variant="ghost" icon={Eye} onClick={() => openDetail(upstream)}>详情</Button><Button size="sm" variant="ghost" icon={Pencil} onClick={() => openEdit(upstream)}>编辑</Button><Button size="sm" variant="ghost" icon={Waypoints} busy={busyAction === `discover-${upstream.id}`} onClick={() => void discover(upstream)}>获取模型</Button></div></td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        )}
      </Surface>

      <Dialog open={addOpen} title="添加上游" description="凭据会由服务端加密保存，不会写入浏览器存储。" onClose={() => setAddOpen(false)} footer={<><Button onClick={() => setAddOpen(false)}>取消</Button><Button type="submit" form="upstream-form" variant="primary" busy={saving}>保存上游</Button></>}>
        <form id="upstream-form" className="form-grid" onSubmit={(event) => void create(event)}>
          <Field label="名称"><input className="input" value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} placeholder="例如：主线路" /></Field>
          <Field label="协议"><select className="select" value={form.protocol} onChange={(event) => setForm({ ...form, protocol: event.target.value as Protocol })}>{creatableProtocols.map((value) => <option value={value} key={value}>{protocolLabels[value]}</option>)}</select></Field>
          <div className="field-span-2"><Field label="API Base URL" hint="填写中转站实际兼容地址，末尾斜杠会自动处理。"><input className="input" type="url" value={form.baseUrl} onChange={(event) => setForm({ ...form, baseUrl: event.target.value })} placeholder="https://example.com/v1" /></Field></div>
          <div className="field-span-2"><Field label="API Key"><input className="input" type="password" value={form.apiKey} onChange={(event) => setForm({ ...form, apiKey: event.target.value })} autoComplete="new-password" placeholder="sk-..." /></Field></div>
        </form>
      </Dialog>

      <Dialog open={Boolean(editing && editForm)} title="编辑上游" description="API Key 留空会保留现有凭据；修改连接信息后健康状态会重新确认。" onClose={() => { setEditing(null); setEditForm(null); }} footer={<><Button onClick={() => { setEditing(null); setEditForm(null); }}>取消</Button><Button type="submit" form="edit-upstream-form" variant="primary" busy={saving}>保存更改</Button></>}>
        {editForm && <form id="edit-upstream-form" className="form-grid" onSubmit={(event) => void saveEdit(event)}>
          <Field label="名称"><input className="input" value={editForm.name} onChange={(event) => setEditForm({ ...editForm, name: event.target.value })} /></Field>
          <Field label="协议"><select className="select" value={editForm.protocol} onChange={(event) => setEditForm({ ...editForm, protocol: event.target.value as Protocol })}>{creatableProtocols.map((value) => <option value={value} key={value}>{protocolLabels[value]}</option>)}</select></Field>
          <div className="field-span-2"><Field label="API Base URL"><input className="input" type="url" value={editForm.baseUrl} onChange={(event) => setEditForm({ ...editForm, baseUrl: event.target.value })} /></Field></div>
          <div className="field-span-2"><Field label="替换 API Key" hint="留空表示不更换现有 API Key。"><input className="input" type="password" value={editForm.apiKey || ''} onChange={(event) => setEditForm({ ...editForm, apiKey: event.target.value })} autoComplete="new-password" placeholder="留空保留现有密钥" /></Field></div>
          <div className="field-span-2"><Switch checked={editForm.enabled} onChange={(enabled) => setEditForm({ ...editForm, enabled })} label="启用此上游" /></div>
        </form>}
      </Dialog>

      <Dialog open={Boolean(deleting)} title="删除上游" description="删除会同步移除使用该上游的路由目标，此操作无法撤销。" onClose={() => setDeleting(null)} width="sm" footer={<><Button onClick={() => setDeleting(null)}>取消</Button><Button variant="danger" icon={Trash2} busy={Boolean(deleting && busyAction === `delete-${deleting.id}`)} onClick={() => void deleteUpstream()}>确认删除</Button></>}>
        {deleting && <div className="delete-summary"><strong>{deleting.name}</strong><code>{deleting.baseUrl}</code><span>{deleting.modelCount} 个已应用模型</span></div>}
      </Dialog>

      <Drawer open={Boolean(selected)} title={selected?.name || ''} description={selected?.baseUrl} onClose={() => { setSelectedId(null); setDiscovery(null); }} footer={selected && <><Button variant="ghost" icon={Trash2} onClick={() => { setSelectedId(null); setDeleting(selected); }}>删除</Button><Button icon={Pencil} onClick={() => openEdit(selected)}>编辑</Button><Button variant="primary" icon={Waypoints} busy={busyAction === `discover-${selected.id}`} onClick={() => void discover(selected)}>获取模型</Button></>}>
        {selected && <div className="upstream-detail">
          <Tabs label="上游详情" items={[{ value: 'overview', label: '概览' }, { value: 'models', label: `模型 ${selected.modelCount}` }, { value: 'account', label: '账户适配' }]} value={tab} onChange={setTab} />
          {tab === 'overview' && <div className="detail-stack">
            <div className="detail-summary"><StatusBadge state={selected.enabled ? selected.state : 'disabled'} /><Badge>{protocolLabels[selected.protocol]}</Badge></div>
            <dl className="detail-definition"><div><dt>接口地址</dt><dd><code>{selected.baseUrl}</code></dd></div><div><dt>凭据</dt><dd>{selected.credentialCount} 个 API Key</dd></div><div><dt>探针延迟</dt><dd>{formatLatency(selected.latencyMs)}</dd></div><div><dt>模型同步</dt><dd>{formatDateTime(selected.lastSyncAt)}</dd></div></dl>
            {selected.lastError && <div className="inline-warning">{selected.lastError}</div>}
            <Button icon={FlaskConical} busy={busyAction === `test-${selected.id}`} onClick={() => void test(selected)}>测试连接</Button>
          </div>}
          {tab === 'models' && <div className="detail-stack">
            {discovery && discovery.upstreamId === selected.id && <div className={`sync-review ${discovery.complete ? '' : 'is-incomplete'}`}><div className="sync-review-heading"><div><Check size={17} /><strong>{discovery.complete ? '发现结果待应用' : '发现结果不完整'}</strong></div><span>{discovery.complete ? '完整响应' : '保留旧模型'}</span></div><div className="sync-counts"><span className="added">+{discovery.added.length} 新增</span><span>-{discovery.removed.length} 待移除</span><span>{discovery.unchanged.length} 未变化</span></div>{discovery.added.length > 0 && <div className="model-chip-list">{discovery.added.map((model) => <code key={model}>{model}</code>)}</div>}{!discovery.complete && <p className="muted-copy">本次响应不完整，不能覆盖当前模型列表。请检查上游后重新获取。</p>}<Button variant="primary" disabled={!discovery.complete} busy={busyAction === `apply-${selected.id}`} onClick={() => void applyDiscovery()}>确认并应用</Button></div>}
            <div className="model-list">{selected.models?.length ? selected.models.map((model) => <div className="model-list-row" key={model.id}><code>{model.name}</code><Badge tone={model.enabled ? 'success' : 'neutral'}>{model.enabled ? '可路由' : '未发布'}</Badge></div>) : <EmptyState title="尚未同步模型" description="点击获取上游模型，确认差异后再应用。" />}</div>
          </div>}
          {tab === 'account' && <div className="detail-stack"><BalanceBlock upstream={selected} /><div className="detail-block"><div className="detail-block-heading"><div><KeyRound size={17} /><strong>API Key 凭据</strong></div><Badge>{selected.credentialCount} 个</Badge></div><p className="muted-copy">凭据仅用于代理调用。账户登录、OAuth 和签到不属于核心连接方式。</p></div><div className="detail-block"><div className="detail-block-heading"><div><Server size={17} /><strong>上游日志适配</strong></div><Badge tone="neutral">{selected.usageSupported === true ? '已配置' : '未配置'}</Badge></div><p className="muted-copy">当前后端尚未实现真实使用日志适配，面板不会把空占位响应展示为成功。</p></div></div>}
        </div>}
      </Drawer>
    </div>
  );
}
