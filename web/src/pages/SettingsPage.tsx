import { BookOpenCheck, Database, RefreshCw, Route as RouteIcon, Save, ShieldCheck, TimerReset } from 'lucide-react';
import { useEffect, useState, type ReactNode } from 'react';
import { Badge, Button, ErrorState, Field, LoadingState, PageHeader, SectionHeader, Surface } from '../components/ui';
import { useToast } from '../components/Toast';
import { api } from '../lib/api';
import { formatDateTime } from '../lib/format';
import { useAsyncData } from '../lib/hooks';
import type { GatewaySettings } from '../lib/types';

function SettingRow({ icon, title, description, children }: { icon: ReactNode; title: string; description: string; children: ReactNode }) {
  return <div className="setting-row"><span className="setting-icon">{icon}</span><div className="setting-copy"><strong>{title}</strong><p>{description}</p></div><div className="setting-control">{children}</div></div>;
}

export function SettingsPage() {
  const toast = useToast();
  const state = useAsyncData(() => api.settings(), []);
  const [draft, setDraft] = useState<GatewaySettings | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (state.data) setDraft(state.data);
  }, [state.data]);

  const setNumber = (field: keyof GatewaySettings, value: string) => {
    const parsed = Number(value);
    if (!draft || !Number.isFinite(parsed)) return;
    setDraft({ ...draft, [field]: parsed });
  };

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    try {
      const updated = await api.updateSettings(draft);
      state.setData(updated);
      setDraft(updated);
      toast.show('设置已保存并应用', 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '保存失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  if (state.loading && !draft) return <div className="page"><LoadingState label="正在读取设置" /></div>;
  if (state.error && !draft) return <div className="page"><ErrorState message={state.error} onRetry={() => void state.refresh()} /></div>;
  if (!draft) return null;

  return (
    <div className="page settings-page">
      <PageHeader title="系统设置" description="只保留网关运行所需配置。修改后统一保存，不提供破坏性维护按钮。" actions={<Button variant="primary" icon={Save} busy={saving} onClick={() => void save()}>保存设置</Button>} />
      <div className="settings-layout">
        <Surface>
          <SectionHeader title="路由与健康" description="探针和真实请求使用同一套健康状态机。" />
          <div className="setting-list">
            <SettingRow icon={<RefreshCw size={17} />} title="模型探针周期" description="仅探测监控页已选择的模型。"><div className="number-with-unit"><input className="input" type="number" min="60" step="30" value={draft.probeIntervalSeconds} onChange={(event) => setNumber('probeIntervalSeconds', event.target.value)} /><span>秒</span></div></SettingRow>
            <SettingRow icon={<ShieldCheck size={17} />} title="连续失败阈值" description="首次失败只观察，达到阈值后进入冷却。"><div className="number-with-unit"><input className="input" type="number" min="2" max="10" value={draft.failureThreshold} onChange={(event) => setNumber('failureThreshold', event.target.value)} /><span>次</span></div></SettingRow>
            <SettingRow icon={<TimerReset size={17} />} title="冷却时间" description="到期后通过单个半开请求恢复。"><div className="number-with-unit"><input className="input" type="number" min="30" step="30" value={draft.cooldownSeconds} onChange={(event) => setNumber('cooldownSeconds', event.target.value)} /><span>秒</span></div></SettingRow>
          </div>
        </Surface>

        <Surface>
          <SectionHeader title="请求边界" description="限制单次请求在上游之间切换的总成本。" />
          <div className="setting-list">
            <SettingRow icon={<RouteIcon size={17} />} title="最大尝试次数" description="包括首选目标在内的上游尝试总数。"><div className="number-with-unit"><input className="input" type="number" min="1" max="10" value={draft.maxAttempts} onChange={(event) => setNumber('maxAttempts', event.target.value)} /><span>次</span></div></SettingRow>
            <SettingRow icon={<TimerReset size={17} />} title="请求总超时" description="整个路由修订内共享的总截止时间。"><div className="number-with-unit"><input className="input" type="number" min="10" max="600" value={draft.requestTimeoutSeconds} onChange={(event) => setNumber('requestTimeoutSeconds', event.target.value)} /><span>秒</span></div></SettingRow>
          </div>
        </Surface>

        <Surface>
          <SectionHeader title="计费与数据" description="下游统一使用美元和不可变价格快照。" />
          <div className="setting-list">
            <SettingRow icon={<BookOpenCheck size={17} />} title="官方价格目录" description={`${draft.priceCatalogSource} · ${formatDateTime(draft.priceCatalogUpdatedAt)}`}><div className="catalog-status"><Badge tone="success">已锁定</Badge><code>{draft.priceCatalogVersion}</code></div></SettingRow>
            <SettingRow icon={<Database size={17} />} title="请求日志保留" description="到期日志由后台任务分批清理。"><div className="number-with-unit"><input className="input" type="number" min="1" max="365" value={draft.logRetentionDays} onChange={(event) => setNumber('logRetentionDays', event.target.value)} /><span>天</span></div></SettingRow>
            <SettingRow icon={<Database size={17} />} title="最近备份" description="备份不会包含明文下游密钥或日志中的鉴权头。"><span className="setting-readonly">{formatDateTime(draft.lastBackupAt)}</span></SettingRow>
          </div>
        </Surface>
      </div>
    </div>
  );
}
