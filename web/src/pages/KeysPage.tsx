import { Check, ChevronDown, Clipboard, Eye, EyeOff, KeyRound, Pencil, Plus, RefreshCw } from 'lucide-react';
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
  InlineNotice,
  LoadingState,
  PageHeader,
  Panel,
  SearchField,
  submitForm,
  Switch,
} from '../components/ui';
import { api, ApiError } from '../lib/api';
import { formatDateTime, formatRelativeTime, formatUSDFromNano, nanoUSDFromInput } from '../lib/format';
import { useResource } from '../lib/hooks';
import type { DownstreamKey, RoutingProfile } from '../lib/types';

type SecretPresentation = {
  kind: 'created' | 'revealed' | 'rotated';
  keyName: string;
  secret: string;
};

type SecretAction = 'reveal' | 'copy';

type KeyEditorInput = {
  name: string;
  quotaNanoUSD: number | null;
  hourlyQuotaNanoUSD: number | null;
  billingMultiplier: number;
  expires: number | null;
  routingProfileId: number | null;
  enabled: boolean;
};

function formatBillingMultiplier(value: number): string {
  return `${value.toLocaleString('en-US', { minimumFractionDigits: 1, maximumFractionDigits: 2 })}x`;
}

function KeyDialog({
  open,
  item,
  billingMultiplier,
  profiles,
  saving,
  onClose,
  onSubmit,
}: {
  open: boolean;
  item: DownstreamKey | null;
  billingMultiplier: number;
  profiles: RoutingProfile[];
  saving: boolean;
  onClose: () => void;
  onSubmit: (input: KeyEditorInput) => Promise<void>;
}) {
  const defaultProfile = profiles.find((profile) => profile.isDefault);
  const [name, setName] = useState(item?.name || '');
  const [quota, setQuota] = useState(item?.quotaNanoUSD === null || item?.quotaNanoUSD === undefined ? '' : String(item.quotaNanoUSD / 1_000_000_000));
  const [hourlyQuota, setHourlyQuota] = useState(item?.hourlyQuotaNanoUSD === null || item?.hourlyQuotaNanoUSD === undefined ? '' : String(item.hourlyQuotaNanoUSD / 1_000_000_000));
  const [multiplier, setMultiplier] = useState(String(billingMultiplier));
  const [expires, setExpires] = useState(item?.expires ? new Date(item.expires).toISOString().slice(0, 16) : '');
  const [enabled, setEnabled] = useState(item?.enabled ?? true);
  const [routingProfileId, setRoutingProfileId] = useState(
    item && !item.usesDefaultRoutingProfile ? String(item.routingProfileId) : 'default',
  );
  const [error, setError] = useState('');

  useEffect(() => {
    if (!open) return;
    setName(item?.name || '');
    setQuota(item?.quotaNanoUSD === null || item?.quotaNanoUSD === undefined ? '' : String(item.quotaNanoUSD / 1_000_000_000));
    setHourlyQuota(item?.hourlyQuotaNanoUSD === null || item?.hourlyQuotaNanoUSD === undefined ? '' : String(item.hourlyQuotaNanoUSD / 1_000_000_000));
    setMultiplier(String(billingMultiplier));
    setExpires(item?.expires ? new Date(item.expires).toISOString().slice(0, 16) : '');
    setEnabled(item?.enabled ?? true);
    setRoutingProfileId(item && !item.usesDefaultRoutingProfile ? String(item.routingProfileId) : 'default');
    setError('');
  }, [billingMultiplier, item, open]);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const quotaNanoUSD = nanoUSDFromInput(quota);
    const hourlyQuotaNanoUSD = nanoUSDFromInput(hourlyQuota);
    const billingMultiplierValue = Number(multiplier);
    if (!name.trim()) {
      setError('密钥名称不能为空');
      return;
    }
    if (quota.trim() && quotaNanoUSD === null) {
      setError('额度必须是大于或等于 0 的美元金额');
      return;
    }
    if (hourlyQuota.trim() && hourlyQuotaNanoUSD === null) {
      setError('每小时消费额度必须是大于或等于 0 的美元金额');
      return;
    }
    if (!Number.isFinite(billingMultiplierValue) || billingMultiplierValue < 0 || billingMultiplierValue > 1_000) {
      setError('扣款倍率必须是 0 到 1000 之间的数字');
      return;
    }
    setError('');
    await onSubmit({
      name: name.trim(),
      quotaNanoUSD,
      hourlyQuotaNanoUSD,
      billingMultiplier: billingMultiplierValue,
      expires: expires ? new Date(expires).getTime() : null,
      routingProfileId: routingProfileId === 'default' ? null : Number(routingProfileId),
      enabled,
    });
  };

  return (
    <Dialog
      open={open}
      title={item ? '编辑下游密钥' : '创建下游密钥'}
      description="下游额度按官方美元价格 × 扣款倍率扣减；上游站点余额和倍率不会改变这里的计费。"
      onClose={onClose}
      footer={<><Button onClick={onClose}>取消</Button><Button variant="primary" busy={saving} onClick={() => submitForm('downstream-key-form')}>{item ? '保存' : '创建密钥'}</Button></>}
    >
      <form id="downstream-key-form" className="form-stack" onSubmit={submit}>
        <Field label="密钥名称" required><input className="input" value={name} onChange={(event) => setName(event.target.value)} maxLength={120} autoComplete="off" autoFocus /></Field>
        <div className="form-grid two-columns">
          <Field label="总额度（USD）" hint="留空表示不限额。"><input className="input" type="number" min="0" step="0.01" value={quota} onChange={(event) => setQuota(event.target.value)} placeholder="不限额" /></Field>
          <Field label="每小时消费额度（USD）" hint="留空表示每小时不限额。"><input className="input" type="number" min="0" step="0.01" value={hourlyQuota} onChange={(event) => setHourlyQuota(event.target.value)} placeholder="不限额" /></Field>
        </div>
        <div className="form-grid two-columns">
          <Field label="有效期" hint="留空表示永不过期。"><input className="input" type="datetime-local" value={expires} onChange={(event) => setExpires(event.target.value)} /></Field>
          <Field label="扣款倍率" hint="默认 1.0x；0x 允许，后续请求不扣下游额度。"><input className="input" type="number" min="0" step="0.01" value={multiplier} onChange={(event) => setMultiplier(event.target.value)} /></Field>
        </div>
        <Field label="路由方案" hint="模型自动来自所选方案，无需再次勾选。"><select className="select" value={routingProfileId} onChange={(event) => setRoutingProfileId(event.target.value)}><option value="default">{defaultProfile ? `${defaultProfile.name}（默认，自动跟随）` : '默认路由（自动跟随）'}</option>{profiles.filter((profile) => !profile.isDefault).map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}</select></Field>
        <InlineNotice tone="info">选择默认路由后，这把 Key 会始终跟随当前默认方案；切换或重命名默认方案时无需重新配置。</InlineNotice>
        <InlineNotice tone="info">扣款规则：官方费用 × 扣款倍率。保存后仅影响后续请求，历史账单不会重算。</InlineNotice>
        <Switch checked={enabled} label="启用这把密钥" onChange={setEnabled} />
        {error && <p className="form-error">{error}</p>}
      </form>
    </Dialog>
  );
}

export function KeysPage() {
  const toast = useToast();
  const resource = useResource(() => api.listDownstreamKeys(), []);
  const profiles = useResource(() => api.listRoutingProfiles(), []);
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState<'all' | 'enabled' | 'disabled'>('all');
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<DownstreamKey | null>(null);
  const [saving, setSaving] = useState(false);
  const [secretPresentation, setSecretPresentation] = useState<SecretPresentation | null>(null);
  const [secretVisible, setSecretVisible] = useState(false);
  const [copied, setCopied] = useState(false);
  const [revealingId, setRevealingId] = useState<number | null>(null);
  const [reauthTarget, setReauthTarget] = useState<DownstreamKey | null>(null);
  const [reauthAction, setReauthAction] = useState<SecretAction>('reveal');
  const [reauthPassword, setReauthPassword] = useState('');
  const [reauthError, setReauthError] = useState('');
  const [reauthenticating, setReauthenticating] = useState(false);
  const [rotateTarget, setRotateTarget] = useState<DownstreamKey | null>(null);
  const [rotateConfirmation, setRotateConfirmation] = useState('');
  const [rotating, setRotating] = useState(false);
  const [routeUpdatingId, setRouteUpdatingId] = useState<number | null>(null);
  const defaultProfile = profiles.data?.find((profile) => profile.isDefault);

  const rows = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return (resource.data || []).filter((item) => {
      if (status === 'enabled' && !item.enabled) return false;
      if (status === 'disabled' && item.enabled) return false;
      return !normalized || `${item.name} ${item.keyPrefix} ${item.routingProfileName} ${(item.models || []).join(' ')}`.toLowerCase().includes(normalized);
    });
  }, [query, resource.data, status]);

  const save = async (input: KeyEditorInput) => {
    setSaving(true);
    try {
      if (editing) {
        const updated = await api.updateDownstreamKey(editing.id, editing.revision, input);
        resource.setData((current) => current?.map((item) => item.id === editing.id ? updated : item) || null);
        toast.show('下游密钥已更新', 'success');
      } else {
        const issued = await api.createDownstreamKey(input);
        resource.setData((current) => [...(current || []), issued.item]);
        setCopied(false);
        setSecretVisible(false);
        setSecretPresentation({ kind: 'created', keyName: issued.item.name, secret: issued.secret });
      }
      setDialogOpen(false);
      setEditing(null);
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '保存失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  const toggle = async (item: DownstreamKey, enabled: boolean) => {
    try {
      const updated = await api.updateDownstreamKey(item.id, item.revision, { enabled });
      resource.setData((current) => current?.map((key) => key.id === item.id ? updated : key) || null);
      toast.show(enabled ? '密钥已启用' : '密钥已停用', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '更新失败', 'error');
      void resource.refresh();
    }
  };

  const updateRoute = async (item: DownstreamKey, profileValue: string) => {
    setRouteUpdatingId(item.id);
    try {
      const updated = await api.updateDownstreamKey(item.id, item.revision, { routingProfileId: profileValue === 'default' ? null : Number(profileValue) });
      resource.setData((current) => current?.map((key) => key.id === item.id ? updated : key) || null);
      toast.show(`“${item.name}”已切换到 ${updated.routingProfileName}`, 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '更新路由方案失败', 'error');
      void resource.refresh();
    } finally {
      setRouteUpdatingId(null);
    }
  };

  const showSecret = (kind: SecretPresentation['kind'], item: DownstreamKey, secret: string) => {
    setCopied(false);
    setSecretVisible(false);
    setSecretPresentation({ kind, keyName: item.name, secret });
  };

  const deliverSecret = async (action: SecretAction, item: DownstreamKey, secret: string) => {
    if (action === 'reveal') {
      showSecret('revealed', item, secret);
      return;
    }
    try {
      await navigator.clipboard.writeText(secret);
      toast.show(`“${item.name}”已复制`, 'success');
    } catch {
      showSecret('revealed', item, secret);
      toast.show('浏览器未允许直接复制，已打开密钥查看窗口', 'error');
    }
  };

  const requestSecret = async (action: SecretAction, item: DownstreamKey) => {
    setRevealingId(item.id);
    try {
      await deliverSecret(action, item, await api.revealDownstreamKey(item.id));
    } catch (reason) {
      if (reason instanceof ApiError && reason.code === 'recent_reauthentication_required') {
        setReauthTarget(item);
        setReauthAction(action);
        setReauthPassword('');
        setReauthError('');
        return;
      }
      toast.show(reason instanceof Error ? reason.message : '查看密钥失败', 'error');
      if (reason instanceof ApiError && ['key_not_revealable', 'not_found'].includes(reason.code)) {
        void resource.refresh();
      }
    } finally {
      setRevealingId(null);
    }
  };

  const reauthenticateAndReveal = async (event: FormEvent) => {
    event.preventDefault();
    if (!reauthTarget || !reauthPassword) {
      setReauthError('请输入管理密码');
      return;
    }
    setReauthenticating(true);
    setReauthError('');
    try {
      await api.login(reauthPassword);
      const secret = await api.revealDownstreamKey(reauthTarget.id);
      const target = reauthTarget;
      setReauthTarget(null);
      setReauthPassword('');
      await deliverSecret(reauthAction, target, secret);
    } catch (reason) {
      setReauthError(reason instanceof Error ? reason.message : '重新验证失败');
    } finally {
      setReauthenticating(false);
    }
  };

  const rotate = async () => {
    if (!rotateTarget || rotateConfirmation !== rotateTarget.name) return;
    setRotating(true);
    try {
      const issued = await api.rotateDownstreamKey(rotateTarget.id, rotateTarget.revision);
      resource.setData((current) => current?.map((item) => item.id === issued.item.id ? issued.item : item) || null);
      setRotateTarget(null);
      setRotateConfirmation('');
      showSecret('rotated', issued.item, issued.secret);
      toast.show('密钥已重新生成，旧密钥已立即失效', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '重新生成失败', 'error');
      if (reason instanceof ApiError && reason.code === 'revision_conflict') void resource.refresh();
    } finally {
      setRotating(false);
    }
  };

  const copySecret = async () => {
    if (!secretPresentation) return;
    try {
      await navigator.clipboard.writeText(secretPresentation.secret);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1800);
    } catch {
      toast.show('复制失败，请手动选择密钥', 'error');
    }
  };

  return (
    <div className="page keys-page">
      <PageHeader title="下游密钥" description="用于分发统一 API；每把密钥独立控制总额度、每小时消费额度和有效期，可用模型自动来自所选路由方案。" actions={<Button variant="primary" icon={Plus} onClick={() => { setEditing(null); setDialogOpen(true); }}>创建密钥</Button>} />
      <Panel className="list-panel">
        <FilterBar trailing={<span className="result-count">{rows.length} / {resource.data?.length || 0} 个密钥</span>}>
          <SearchField value={query} onChange={setQuery} placeholder="搜索密钥、路由方案或模型" />
          <div className="segmented" role="group" aria-label="密钥状态">{([['all', '全部'], ['enabled', '已启用'], ['disabled', '已停用']] as const).map(([value, label]) => <button type="button" className={status === value ? 'is-active' : ''} onClick={() => setStatus(value)} key={value}>{label}</button>)}</div>
        </FilterBar>
        {resource.loading && !resource.data ? <LoadingState label="正在读取下游密钥" /> : resource.error && !resource.data ? <ErrorState message={resource.error} onRetry={() => void resource.refresh()} /> : rows.length === 0 ? <EmptyState title={resource.data?.length ? '没有匹配的密钥' : '还没有下游密钥'} description={resource.data?.length ? '调整搜索或状态筛选。' : '创建密钥并选择路由方案，方案中已发布的模型会自动同步。'} action={!resource.data?.length ? <Button variant="primary" icon={Plus} onClick={() => setDialogOpen(true)}>创建第一个密钥</Button> : undefined} /> : <div className="key-list">{rows.map((item) => {
          const quotaUsed = item.quotaNanoUSD === null ? null : Math.min(100, ((item.usedNanoUSD + item.reservedNanoUSD) / Math.max(item.quotaNanoUSD, 1)) * 100);
           const remainingNanoUSD = item.quotaNanoUSD === null ? null : Math.max(0, item.quotaNanoUSD - item.usedNanoUSD - item.reservedNanoUSD);
           const followsDefault = item.usesDefaultRoutingProfile || !item.routingProfileId;
           const routeName = item.routingProfileName || (followsDefault ? defaultProfile?.name : '') || '未命名方案';
           const billingMultiplier = item.billingMultiplier;
           return <article className="key-row" key={item.id}>
            <header><span className="key-icon"><KeyRound size={17} /></span><div><span><strong>{item.name}</strong><Badge tone={item.enabled ? 'success' : 'neutral'}>{item.enabled ? '已启用' : '已停用'}</Badge>{followsDefault && <Badge tone="info">默认路由</Badge>}</span><code>{item.keyPrefix}••••••••</code><span className="table-subline">{routeName} · 自动同步 {item.models.length} 个模型 · 扣款 {formatBillingMultiplier(billingMultiplier)}</span></div><div className="key-actions">{item.revealable && <Button className="key-compact-action" size="sm" icon={Clipboard} aria-label="复制密钥" title="复制密钥" busy={revealingId === item.id} onClick={() => void requestSecret('copy', item)}>复制密钥</Button>}<Button className="key-compact-action" size="sm" icon={Pencil} aria-label="编辑密钥" title="编辑密钥" onClick={() => { setEditing(item); setDialogOpen(true); }}>编辑</Button><Switch checked={item.enabled} label={item.enabled ? '启用' : '停用'} onChange={(next) => void toggle(item, next)} /><Button className="key-compact-action" size="sm" icon={RefreshCw} aria-label="重新生成密钥" title={item.revealable ? '重新生成密钥' : '迁移密钥无法复制，重新生成后可查看和复制'} onClick={() => { setRotateTarget(item); setRotateConfirmation(''); }}>重新生成密钥</Button></div></header>
            <div className="key-facts">
              <div className="key-quota-overview">
                <span>额度使用</span>
                <strong>{formatUSDFromNano(item.usedNanoUSD)} <small>/ {formatUSDFromNano(item.quotaNanoUSD)}</small></strong>
                {quotaUsed !== null && <span className="quota-bar"><span style={{ width: `${quotaUsed}%` }} /></span>}
                <small>剩余 {formatUSDFromNano(remainingNanoUSD)}{item.reservedNanoUSD ? ` · ${formatUSDFromNano(item.reservedNanoUSD)} 处理中` : ''}</small>
              </div>
              <div><span>本小时消费</span><strong>{formatUSDFromNano(item.usedThisHourNanoUSD ?? 0)}</strong><small>上限 {formatUSDFromNano(item.hourlyQuotaNanoUSD)}</small></div>
              <div><span>扣款倍率</span><strong>{formatBillingMultiplier(billingMultiplier)}</strong><small>按官方美元价格扣减</small></div>
              <div><span>有效期</span><strong>{item.expires ? formatDateTime(item.expires, true) : '永不过期'}</strong><small>{item.expires ? formatRelativeTime(item.expires) : `最后使用 ${formatDateTime(item.lastUsedAt, true)}`}</small></div>
            </div>
            <details className="key-models"><summary><span>当前路由与发布模型</span><Badge tone="info">自动同步</Badge><Badge tone={item.models.length ? 'info' : 'warning'}>{item.models.length} 个</Badge><ChevronDown size={15} /></summary><div className="key-models-content"><label className="key-route-field"><span>当前路由方案</span><select className="select" value={followsDefault ? 'default' : String(item.routingProfileId)} disabled={routeUpdatingId === item.id} onChange={(event) => void updateRoute(item, event.target.value)}><option value="default">{defaultProfile ? `${defaultProfile.name}（默认，自动跟随）` : '默认路由（自动跟随）'}</option>{(profiles.data || []).filter((profile) => !profile.isDefault).map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}</select><small>{routeUpdatingId === item.id ? '正在同步路由与模型…' : `${routeName}${followsDefault ? ' · 跟随默认方案' : ' · 固定方案'}`}</small></label><div className="key-model-list"><span>已发布模型会自动同步给此 Key</span>{item.models.length ? <div>{item.models.map((model) => <code key={model}>{model}</code>)}</div> : <InlineNotice tone="warning">当前路由方案还没有发布模型；发布后会自动出现在这把 Key 中，无需单独勾选。</InlineNotice>}</div></div></details>
          </article>;
        })}</div>}
      </Panel>

      <KeyDialog key={`${dialogOpen}:${editing?.id || 0}`} open={dialogOpen} item={editing} billingMultiplier={editing?.billingMultiplier ?? 1} profiles={profiles.data || []} saving={saving} onClose={() => { setDialogOpen(false); setEditing(null); }} onSubmit={save} />
      <Dialog
        open={Boolean(reauthTarget)}
        title="重新验证管理身份"
        description={`${reauthAction === 'copy' ? '复制' : '查看'}“${reauthTarget?.name || ''}”的完整密钥前，需要重新输入管理密码。`}
        onClose={() => { if (!reauthenticating) { setReauthTarget(null); setReauthPassword(''); setReauthError(''); } }}
        footer={<><Button disabled={reauthenticating} onClick={() => { setReauthTarget(null); setReauthPassword(''); setReauthError(''); }}>取消</Button><Button variant="primary" busy={reauthenticating} onClick={() => submitForm('key-reauth-form')}>{reauthAction === 'copy' ? '验证并复制' : '验证并查看'}</Button></>}
      >
        <form id="key-reauth-form" className="form-stack" onSubmit={reauthenticateAndReveal}>
          <Field label="管理密码" error={reauthError || undefined}>
            <input className="input" type="password" autoComplete="current-password" value={reauthPassword} onChange={(event) => setReauthPassword(event.target.value)} autoFocus />
          </Field>
          <InlineNotice tone="info">验证成功会建立新的管理会话，并仅完成本次{reauthAction === 'copy' ? '复制' : '查看'}操作。</InlineNotice>
        </form>
      </Dialog>
      <Dialog
        open={Boolean(rotateTarget)}
        title="重新生成下游密钥"
        description={`重新生成“${rotateTarget?.name || ''}”后，旧密钥会立即失效，正在使用它的客户端必须改用新密钥。`}
        onClose={() => { if (!rotating) { setRotateTarget(null); setRotateConfirmation(''); } }}
        footer={<><Button disabled={rotating} onClick={() => { setRotateTarget(null); setRotateConfirmation(''); }}>取消</Button><Button variant="danger" icon={RefreshCw} busy={rotating} disabled={rotateConfirmation !== rotateTarget?.name} onClick={() => void rotate()}>确认重新生成</Button></>}
      >
        <div className="form-stack">
          <InlineNotice tone="warning">这是立即生效的操作。旧密钥不会保留宽限期，也无法恢复；只有需要替换泄露密钥时才使用。</InlineNotice>
          <Field label={`输入密钥名称“${rotateTarget?.name || ''}”以确认`}>
            <input className="input" value={rotateConfirmation} onChange={(event) => setRotateConfirmation(event.target.value)} autoComplete="off" autoFocus />
          </Field>
        </div>
      </Dialog>
      <Dialog
        open={Boolean(secretPresentation)}
        title={secretPresentation?.kind === 'created' ? '下游密钥已创建' : secretPresentation?.kind === 'rotated' ? '下游密钥已重新生成' : '查看下游密钥'}
        description={secretPresentation?.kind === 'rotated' ? '旧密钥已经失效，请立即更新客户端。' : `敏感结果：${secretPresentation?.keyName || ''}`}
        onClose={() => { setSecretPresentation(null); setSecretVisible(false); setCopied(false); }}
        footer={<Button variant="primary" onClick={() => { setSecretPresentation(null); setSecretVisible(false); setCopied(false); }}>关闭敏感结果</Button>}
      >
        <InlineNotice tone={secretPresentation?.kind === 'rotated' ? 'warning' : 'info'}>完整密钥默认遮蔽，只在这个弹窗内临时显示；关闭后会从页面状态中清除。</InlineNotice>
        <div className="issued-secret">
          <code>{secretVisible ? secretPresentation?.secret : `${secretPresentation?.secret.slice(0, 6) || 'js_'}••••••••••••••••••••••••`}</code>
          <div className="secret-result-actions">
            <Button icon={secretVisible ? EyeOff : Eye} onClick={() => setSecretVisible((visible) => !visible)}>{secretVisible ? '隐藏密钥' : '显示密钥'}</Button>
            <Button icon={copied ? Check : Clipboard} onClick={() => void copySecret()}>{copied ? '已复制' : '复制密钥'}</Button>
          </div>
        </div>
      </Dialog>
    </div>
  );
}
