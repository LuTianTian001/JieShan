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
  UnavailableState,
} from '../components/ui';
import { api, ApiUnavailableError } from '../lib/api';
import { useResource } from '../lib/hooks';
import {
  loadProbePrototypePreferences,
  saveProbePrototypePreferences,
} from '../lib/probePreferences';
import type { GatewaySettings } from '../lib/types';
import { PricingCatalogPanel } from './settings/PricingCatalogPanel';

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
  const [slowFirstOutputThresholdMs, setSlowFirstOutputThresholdMs] = useState(
    () => loadProbePrototypePreferences().slowFirstOutputThresholdMs,
  );
  const [savedSlowFirstOutputThresholdMs, setSavedSlowFirstOutputThresholdMs] = useState(slowFirstOutputThresholdMs);
  const [saving, setSaving] = useState(false);
  const [passwordOpen, setPasswordOpen] = useState(false);
  const [currentPassword, setCurrentPassword] = useState('');
  const [nextPassword, setNextPassword] = useState('');

  const settingsDirty = useMemo(() => Boolean(
    draft
    && resource.data?.settings
    && (
      JSON.stringify(draft) !== JSON.stringify(resource.data.settings)
      || slowFirstOutputThresholdMs !== savedSlowFirstOutputThresholdMs
    )
  ), [draft, resource.data?.settings, savedSlowFirstOutputThresholdMs, slowFirstOutputThresholdMs]);

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
      toast.show('请求总时限不能短于首字超时或流空闲超时。', 'error');
      return;
    }
    if (slowFirstOutputThresholdMs >= draft.firstOutputTimeoutMs) {
      toast.show('首字冷却阈值需要短于首字超时，才能在请求超时前生效。', 'error');
      return;
    }
    setSaving(true);
    try {
      const { revision, ...input } = draft;
      const updated = await api.updateSettings(revision, input);
      const savedPrototypePreferences = saveProbePrototypePreferences({ slowFirstOutputThresholdMs });
      setDraft(updated);
      setSlowFirstOutputThresholdMs(savedPrototypePreferences.slowFirstOutputThresholdMs);
      setSavedSlowFirstOutputThresholdMs(savedPrototypePreferences.slowFirstOutputThresholdMs);
      resource.setData((current) => current ? { ...current, settings: updated } : current);
      toast.show('网关设置已保存', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '设置保存失败', 'error');
      void resource.refresh();
    } finally {
      setSaving(false);
    }
  };

  const updatePassword = () => {
    if (!currentPassword || nextPassword.length < 8) {
      toast.show('请输入当前密码，并设置至少 8 位的新密码。', 'error');
      return;
    }
    setPasswordOpen(false);
    setCurrentPassword('');
    setNextPassword('');
    toast.show('原型已模拟更新管理密码', 'success');
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
              <Field label="首字冷却阈值" hint="真实请求或探针已响应但首字过慢时，单次即进入冷却。">
                <div className="unit-input"><input className="input" type="number" min={1} max={120} value={seconds(slowFirstOutputThresholdMs)} onChange={(event) => setSlowFirstOutputThresholdMs(Math.max(1, Number(event.target.value)) * 1_000)} /><span>秒</span></div>
              </Field>
            </div>
            <div className="settings-policy-row">
              <span className="settings-policy-icon"><Radar size={18} /></span>
              <div><strong>流式模型探针</strong><span>固定提示词：<code>Reply exactly OK</code> · 最多 4 Token（固定）</span></div>
              <div><span>记录指标</span><strong>首字延迟 · 总耗时 · 慢首字冷却</strong></div>
            </div>
          </Panel>

          <Panel>
            <Disclosure summary={<span className="settings-disclosure-title"><TimerReset size={17} /><span><strong>请求超时边界</strong><small>无输出、流空闲和总时限</small></span></span>}>
              <div className="settings-grid timeout-settings-grid">
                <Field label="首字超时" hint="在此时间内没有任何有效输出就切换下一家。">
                  <div className="unit-input"><input className="input" type="number" min={1} value={seconds(draft.firstOutputTimeoutMs)} onChange={(event) => setSeconds('firstOutputTimeoutMs', event.target.value)} /><span>秒</span></div>
                </Field>
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
        description="这是高保真前端原型，提交后只模拟成功状态。"
        onClose={() => setPasswordOpen(false)}
        footer={<><Button onClick={() => setPasswordOpen(false)}>取消</Button><Button variant="primary" onClick={updatePassword}>确认更新</Button></>}
      >
        <div className="form-stack">
          <Field label="当前密码"><input className="input" type="password" data-autofocus="true" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} /></Field>
          <Field label="新密码" hint="至少 8 位。"><input className="input" type="password" value={nextPassword} onChange={(event) => setNextPassword(event.target.value)} /></Field>
        </div>
      </Dialog>
    </div>
  );
}
