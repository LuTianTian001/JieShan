import {
  Activity,
  KeyRound,
  LockKeyhole,
  Radar,
  Save,
  ScrollText,
  ShieldCheck,
  TimerReset,
} from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { useToast } from '../components/Toast';
import {
  Badge,
  Button,
  Dialog,
  Disclosure,
  ErrorState,
  Field,
  InlineNotice,
  LoadingState,
  PageHeader,
  Panel,
  SectionHeader,
  submitForm,
  UnavailableState,
} from '../components/ui';
import { api, ApiError, ApiUnavailableError } from '../lib/api';
import { useResource } from '../lib/hooks';
import type { GatewaySettings } from '../lib/types';
import { PricingCatalogPanel } from './settings/PricingCatalogPanel';
import { SystemHealthPanel } from './settings/SystemHealthPanel';

interface SettingsData {
  settings: GatewaySettings | null;
}

async function loadSettings(): Promise<SettingsData> {
  let settings: GatewaySettings | null = null;
  try {
    settings = await api.settings();
  } catch (error) {
    if (!(error instanceof ApiUnavailableError)) throw error;
  }
  return { settings };
}

function seconds(value: number): string {
  return String(Math.round(value / 1_000));
}

function minutes(value: number): string {
  return String(Math.round(value / 60_000));
}

export function SettingsPage() {
  const toast = useToast();
  const resource = useResource(loadSettings, []);
  const [draft, setDraft] = useState<GatewaySettings | null>(null);
  const [saving, setSaving] = useState(false);
  const [passwordOpen, setPasswordOpen] = useState(false);
  const [currentPassword, setCurrentPassword] = useState('');
  const [nextPassword, setNextPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [passwordSaving, setPasswordSaving] = useState(false);

  const settingsDirty = useMemo(() => Boolean(
    draft
    && resource.data?.settings
    && JSON.stringify(draft) !== JSON.stringify(resource.data.settings)
  ), [draft, resource.data?.settings]);

  useEffect(() => setDraft(resource.data?.settings || null), [resource.data?.settings]);

  const setNumber = (field: keyof GatewaySettings, value: number) => {
    if (!draft || !Number.isFinite(value)) return;
    setDraft({ ...draft, [field]: value });
  };
  const setSeconds = (field: keyof GatewaySettings, value: string) => setNumber(field, Math.max(0, Number(value)) * 1_000);
  const setMinutes = (field: keyof GatewaySettings, value: string) => setNumber(field, Math.max(0, Number(value)) * 60_000);

  const save = async () => {
    if (!draft) return;
    if (draft.failureThreshold < 2) {
      toast.show('连续失败阈值至少为 2，第一次失败只切换当前请求，不立即冷却。', 'error');
      return;
    }
    if (draft.requestTimeoutMs < Math.max(draft.firstOutputTimeoutMs, draft.streamIdleTimeoutMs)) {
      toast.show('请求总时限不能短于首 Token 超时或流空闲超时。', 'error');
      return;
    }
    setSaving(true);
    try {
      const { revision, ...input } = draft;
      const updated = await api.updateSettings(revision, input);
      setDraft(updated);
      resource.setData((current) => current ? { ...current, settings: updated } : current);
      toast.show('网关设置已保存', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '设置保存失败', 'error');
      void resource.refresh();
    } finally {
      setSaving(false);
    }
  };

  const closePasswordDialog = () => {
    if (passwordSaving) return;
    setPasswordOpen(false);
    setCurrentPassword('');
    setNextPassword('');
    setConfirmPassword('');
  };

  const updatePassword = async () => {
    if (!currentPassword) {
      toast.show('请输入当前管理密码。', 'error');
      return;
    }
    if (Array.from(nextPassword).length < 12) {
      toast.show('新密码至少需要 12 位。', 'error');
      return;
    }
    if (nextPassword !== confirmPassword) {
      toast.show('两次输入的新密码不一致。', 'error');
      return;
    }
    if (currentPassword === nextPassword) {
      toast.show('新密码不能与当前密码相同。', 'error');
      return;
    }
    setPasswordSaving(true);
    try {
      await api.changeAdminPassword(currentPassword, nextPassword, confirmPassword);
      setPasswordOpen(false);
      setCurrentPassword('');
      setNextPassword('');
      setConfirmPassword('');
      toast.show('管理密码已更新，其他管理会话已退出。', 'success');
    } catch (reason) {
      const messages: Record<string, string> = {
        invalid_current_password: '当前管理密码不正确。',
        password_confirmation_mismatch: '两次输入的新密码不一致。',
        password_unchanged: '新密码不能与当前密码相同。',
        password_too_short: '新密码至少需要 12 位。',
        password_too_long: '新密码过长，请缩短后重试。',
        password_change_conflict: '管理密码刚刚发生变化，请使用当前密码重试。',
      };
      const message = reason instanceof ApiError ? messages[reason.code] || reason.message : '管理密码更新失败';
      toast.show(message, 'error');
    } finally {
      setPasswordSaving(false);
    }
  };

  if (resource.loading && !resource.data) return <div className="page"><LoadingState label="正在读取设置" /></div>;
  if (resource.error && !resource.data) return <div className="page"><ErrorState message={resource.error} onRetry={() => void resource.refresh()} /></div>;

  return (
    <div className="page settings-page">
      <PageHeader
        title="设置"
        description="这里只保留影响可用性、费用和数据安全的核心配置。"
        actions={draft ? <Button variant="primary" icon={Save} busy={saving} disabled={!settingsDirty} onClick={() => void save()}>保存设置</Button> : undefined}
      />

      {!draft ? (
        <Panel><UnavailableState title="网关设置暂不可用" description="当前实例尚未提供可编辑的网关设置，请检查服务状态后重试。" /></Panel>
      ) : (
        <div className="settings-stack">
          <Panel>
            <SectionHeader
              title="路由与故障处理"
              description="真实请求和自动探针共用同一套健康状态；第一次失败会立即尝试下一家，但不会立刻冷却。"
              actions={<Badge tone="success">按顺序路由</Badge>}
            />
            <div className="settings-grid">
              <Field label="连续失败阈值" hint="默认 2 次；达到阈值后进入冷却。">
                <div className="unit-input"><input className="input" type="number" min={2} max={10} value={draft.failureThreshold} onChange={(event) => setNumber('failureThreshold', Number(event.target.value))} /><span>次</span></div>
              </Field>
              <Field label="冷却时间" hint="冷却结束后自动进入恢复探测。">
                <div className="unit-input"><input className="input" type="number" min={1} value={minutes(draft.cooldownMs)} onChange={(event) => setMinutes('cooldownMs', event.target.value)} /><span>分钟</span></div>
              </Field>
              <Field label="失败统计窗口" hint="只累计这个时间范围内的连续失败。">
                <div className="unit-input"><input className="input" type="number" min={30} step={10} value={seconds(draft.failureWindowMs)} onChange={(event) => setSeconds('failureWindowMs', event.target.value)} /><span>秒</span></div>
              </Field>
              <Field label="单次最大上游尝试" hint="按你拖动后的顺序依次尝试。">
                <div className="unit-input"><input className="input" type="number" min={1} max={20} value={draft.maxAttempts} onChange={(event) => setNumber('maxAttempts', Number(event.target.value))} /><span>家</span></div>
              </Field>
            </div>
            <InlineNotice tone="success"><ShieldCheck size={16} />第一次失败只影响当前请求；达到连续失败阈值后才暂停该上游。</InlineNotice>
          </Panel>

          <Panel>
            <SectionHeader
              title="自动探针"
              description="只探测你在监控页选中的模型和上游 Key；探测结果与实际路由共用健康状态。"
              actions={<Badge tone="info">自动运行</Badge>}
            />
            <div className="settings-grid probe-settings-grid">
              <Field label="模型探针周期" hint="发起一次最小流式模型请求。">
                <div className="unit-input"><input className="input" type="number" min={1} value={minutes(draft.probeIntervalMs)} onChange={(event) => setMinutes('probeIntervalMs', event.target.value)} /><span>分钟</span></div>
              </Field>
              <Field label="首 Token 超时" hint="超过该时间仍无首 Token，立即切换并冷却当前上游。">
                <div className="unit-input"><input className="input" type="number" min={1} max={120} value={seconds(draft.firstOutputTimeoutMs)} onChange={(event) => setSeconds('firstOutputTimeoutMs', event.target.value)} /><span>秒</span></div>
              </Field>
            </div>
            <div className="settings-policy-row">
              <span className="settings-policy-icon"><Radar size={18} /></span>
              <div><strong>流式模型探针</strong><span>固定提示词：<code>Reply exactly OK</code> · 通常只产生 1–2 个输出 Token</span></div>
              <div><span>记录指标</span><strong>首 Token 延迟 · 慢首 Token 冷却</strong></div>
            </div>
          </Panel>

          <Panel>
            <Disclosure summary={<span className="settings-disclosure-title"><TimerReset size={17} /><span><strong>请求超时边界</strong><small>流空闲和总时限</small></span></span>}>
              <div className="settings-grid timeout-settings-grid">
                <Field label="流空闲超时" hint="流式输出中断过久时终止当前上游。">
                  <div className="unit-input"><input className="input" type="number" min={1} value={seconds(draft.streamIdleTimeoutMs)} onChange={(event) => setSeconds('streamIdleTimeoutMs', event.target.value)} /><span>秒</span></div>
                </Field>
                <Field label="请求总时限">
                  <div className="unit-input"><input className="input" type="number" min={1} value={seconds(draft.requestTimeoutMs)} onChange={(event) => setSeconds('requestTimeoutMs', event.target.value)} /><span>秒</span></div>
                </Field>
              </div>
            </Disclosure>
          </Panel>

          <Panel>
            <SectionHeader title="日志保留" description="不保存完整提示词和回答，只记录排障与计费所需的结构化字段。" />
            <div className="settings-compact-row">
              <span className="settings-policy-icon"><ScrollText size={18} /></span>
              <div><strong>调用日志保留时间</strong><span>到期后由后台自动清理。</span></div>
              <div className="unit-input"><input className="input" aria-label="调用日志保留天数" type="number" min={1} max={365} value={draft.logRetentionDays} onChange={(event) => setNumber('logRetentionDays', Number(event.target.value))} /><span>天</span></div>
            </div>
          </Panel>

          <PricingCatalogPanel />

          <SystemHealthPanel />

          <Panel>
            <SectionHeader title="安全" description="管理端会话和上游凭据的本机保护状态。" />
            <div className="security-settings-list">
              <div>
                <span className="settings-policy-icon"><LockKeyhole size={18} /></span>
                <div><strong>上游凭据加密</strong><span>API Key、Token 和 Cookie 加密保存，列表永不返回完整内容。</span></div>
                <Badge tone="success">已启用</Badge>
              </div>
              <div>
                <span className="settings-policy-icon"><Activity size={18} /></span>
                <div><strong>管理端会话</strong><span>当前使用安全 Cookie；退出登录后立即失效。</span></div>
                <Badge tone="success">正常</Badge>
              </div>
              <div>
                <span className="settings-policy-icon"><KeyRound size={18} /></span>
                <div><strong>管理密码</strong><span>更新后其他管理会话会重新登录。</span></div>
                <Button size="sm" onClick={() => setPasswordOpen(true)}>更新密码</Button>
              </div>
            </div>
          </Panel>
        </div>
      )}

      {draft && settingsDirty && <div className="settings-save-bar" role="status"><div><strong>有未保存的更改</strong><span>保存后只影响后续请求和探针。</span></div><Button variant="primary" icon={Save} busy={saving} onClick={() => void save()}>保存设置</Button></div>}

      <Dialog
        open={passwordOpen}
        title="更新管理密码"
        description="验证当前密码后更新；完成时会撤销其他管理会话。"
        onClose={closePasswordDialog}
        footer={<><Button disabled={passwordSaving} onClick={closePasswordDialog}>取消</Button><Button variant="primary" busy={passwordSaving} onClick={() => submitForm('admin-password-form')}>确认更新</Button></>}
      >
        <form id="admin-password-form" className="form-stack" onSubmit={(event) => { event.preventDefault(); void updatePassword(); }}>
          <Field label="当前密码"><input className="input" type="password" autoComplete="current-password" data-autofocus="true" disabled={passwordSaving} value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} /></Field>
          <Field label="新密码" hint="至少 12 位；不要与当前密码相同。"><input className="input" type="password" autoComplete="new-password" disabled={passwordSaving} value={nextPassword} onChange={(event) => setNextPassword(event.target.value)} /></Field>
          <Field label="确认新密码"><input className="input" type="password" autoComplete="new-password" disabled={passwordSaving} value={confirmPassword} onChange={(event) => setConfirmPassword(event.target.value)} /></Field>
        </form>
      </Dialog>
    </div>
  );
}
