import { Activity, ChevronDown, Clock3, Gauge, ListFilter, Play, RefreshCw, Route as RouteIcon, Settings2 } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Badge, Button, Dialog, EmptyState, ErrorState, LoadingState, Metric, PageHeader, Surface, Switch } from '../components/ui';
import { useToast } from '../components/Toast';
import { api } from '../lib/api';
import { formatLatency, formatRelativeTime } from '../lib/format';
import { useAsyncData } from '../lib/hooks';
import { inferenceProtocolCapabilities, inferenceProtocolLabel } from '../lib/inferenceProtocols';
import type { V2MonitorModel, V2MonitorTarget, V2PublishedModel } from '../lib/types';

function relativeTime(value: number | null | undefined): string {
  return value ? formatRelativeTime(new Date(value).toISOString()) : '尚未探测';
}

function targetState(target: V2MonitorTarget): 'success' | 'warning' | 'danger' | 'neutral' | 'info' {
  if (!target.enabled) return 'neutral';
  if (target.health.circuitPhase === 'open') return 'danger';
  if (target.health.circuitPhase === 'half_open') return 'warning';
  if (target.health.capabilityState === 'unsupported') return 'danger';
  if (target.lastProbe?.status === 'success') return 'success';
  if (target.lastProbe?.status === 'failed' || target.health.consecutiveFailures > 0) return 'warning';
  return 'neutral';
}

function targetStateLabel(target: V2MonitorTarget, probing: boolean): string {
  if (probing) return '探测中';
  if (!target.enabled) return '已停用';
  if (target.health.circuitPhase === 'open') return '冷却中';
  if (target.health.circuitPhase === 'half_open') return '恢复验证';
  if (!inferenceProtocolCapabilities(target.wireProtocol).routeEligible) return '协议不可路由';
  if (target.health.capabilityState === 'unsupported') return '模型不可用';
  if (target.lastProbe?.status === 'success') return '可用';
  if (target.lastProbe?.status === 'failed' || target.health.consecutiveFailures > 0) return '观察中';
  return '未探测';
}

function isAvailable(target: V2MonitorTarget): boolean {
  return target.enabled
    && target.health.circuitPhase === 'closed'
    && target.health.capabilityState !== 'unsupported'
    && target.lastProbe?.status === 'success';
}

function probeSummary(target: V2MonitorTarget): string {
  const probe = target.lastProbe;
  if (!probe) return '等待首次自动探测';
  if (probe.status === 'success') return `${relativeTime(probe.finishedAt)}完成`;
  return probe.errorMessage || probe.errorClass || `HTTP ${probe.httpStatus ?? '-'}`;
}

function TargetRow({ target, index, probing, modelBusy, onProbe }: { target: V2MonitorTarget; index: number; probing: boolean; modelBusy: boolean; onProbe: (target: V2MonitorTarget) => void }) {
  const tone = probing ? 'info' : targetState(target);
  return (
    <div className="monitor-target-row v2-monitor-target-row">
      <span className="route-rank">#{index + 1}</span>
      <div className="target-identity"><strong>{target.siteName}</strong><span>{target.endpointName}</span></div>
      <span className="monitor-protocol-badge"><Badge tone={inferenceProtocolCapabilities(target.wireProtocol).routeEligible ? 'neutral' : 'warning'}>{inferenceProtocolLabel(target.wireProtocol)}</Badge></span>
      <code className="source-model">{target.sourceModel}</code>
      <span className="monitor-state-badge"><Badge tone={tone}>{targetStateLabel(target, probing)}</Badge></span>
      <div className="monitor-latency-pair monitor-total-latency"><span><Gauge size={13} />总延迟</span><strong>{formatLatency(target.lastProbe?.latencyMs ?? null)}</strong></div>
      <div className="monitor-latency-pair monitor-first-output"><span><Activity size={13} />首输出</span><strong>{formatLatency(target.lastProbe?.firstOutputMs ?? null)}</strong></div>
      <div className="monitor-last-result" title={target.health.lastErrorMessage || target.lastProbe?.errorMessage || undefined}><span><Clock3 size={13} />{relativeTime(target.lastProbe?.finishedAt)}</span><strong>{probeSummary(target)}</strong></div>
      <Button size="sm" variant="ghost" icon={Play} busy={probing} disabled={modelBusy || !target.enabled || !inferenceProtocolCapabilities(target.wireProtocol).routeEligible} onClick={() => onProbe(target)}>探测</Button>
    </div>
  );
}

function monitoredModelTone(model: V2MonitorModel): 'success' | 'warning' | 'danger' | 'neutral' {
  const enabled = model.targets.filter((target) => target.enabled);
  if (enabled.some(isAvailable)) return 'success';
  if (enabled.some((target) => target.lastProbe == null || target.health.circuitPhase === 'half_open')) return 'warning';
  return enabled.length ? 'danger' : 'neutral';
}

export function MonitorPage() {
  const navigate = useNavigate();
  const toast = useToast();
  const state = useAsyncData(async () => {
    const [matrix, publishedModels] = await Promise.all([api.v2MonitorMatrix(), api.v2PublishedModels()]);
    return { matrix, publishedModels };
  }, []);
  const [query, setQuery] = useState('');
  const [collapsed, setCollapsed] = useState<Set<number>>(new Set());
  const [pickerOpen, setPickerOpen] = useState(false);
  const [probingModels, setProbingModels] = useState<Set<number>>(new Set());
  const [probingTargets, setProbingTargets] = useState<Set<number>>(new Set());
  const [savingModels, setSavingModels] = useState<Set<number>>(new Set());

  const visibleModels = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return (state.data?.matrix.models ?? []).filter((model) => !normalized || `${model.publicName} ${model.displayName ?? ''}`.toLowerCase().includes(normalized));
  }, [query, state.data]);

  const refreshMatrix = async () => {
    const matrix = await api.v2MonitorMatrix();
    state.setData((current) => current ? { ...current, matrix } : current);
  };

  const toggleCollapsed = (id: number) => {
    setCollapsed((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };

  const probeModel = async (model: V2MonitorModel) => {
    setProbingModels((current) => new Set(current).add(model.id));
    try {
      const result = await api.probeV2PublishedModel(model.id);
      await refreshMatrix();
      const failed = result.attempts.filter((attempt) => attempt.status === 'failed').length;
      toast.show(failed ? `探测完成：${result.run.successCount} 个可用，${failed} 个失败` : `${model.displayName || model.publicName} 全部网站可用`, failed ? 'info' : 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '模型探测失败', 'error');
    } finally {
      setProbingModels((current) => {
        const next = new Set(current);
        next.delete(model.id);
        return next;
      });
    }
  };

  const probeTarget = async (model: V2MonitorModel, target: V2MonitorTarget) => {
    setProbingTargets((current) => new Set(current).add(target.id));
    try {
      const result = await api.probeV2PublishedModel(model.id, target.id);
      await refreshMatrix();
      const attempt = result.attempts[0];
      toast.show(attempt?.status === 'success' ? `${target.siteName} 可用` : `${target.siteName} 探测失败`, attempt?.status === 'success' ? 'success' : 'error');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '网站探测失败', 'error');
    } finally {
      setProbingTargets((current) => {
        const next = new Set(current);
        next.delete(target.id);
        return next;
      });
    }
  };

  const toggleMonitoring = async (model: V2PublishedModel, monitorEnabled: boolean) => {
    setSavingModels((current) => new Set(current).add(model.id));
    try {
      const updated = await api.updateV2PublishedModel(model.id, { monitorEnabled, revision: model.revision });
      state.setData((current) => current ? {
        ...current,
        publishedModels: current.publishedModels.map((item) => item.id === updated.id ? updated : item),
      } : current);
      await refreshMatrix();
      toast.show(monitorEnabled ? `已监控 ${updated.displayName || updated.publicName}` : `已停止监控 ${updated.displayName || updated.publicName}`, 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '监控设置保存失败', 'error');
    } finally {
      setSavingModels((current) => {
        const next = new Set(current);
        next.delete(model.id);
        return next;
      });
    }
  };

  if (state.loading && !state.data) return <div className="page"><LoadingState label="正在读取模型监控矩阵" /></div>;
  if (state.error && !state.data) return <div className="page"><ErrorState message={state.error} onRetry={() => void state.refresh()} /></div>;

  const matrix = state.data!.matrix;
  const targets = matrix.models.flatMap((model) => model.targets.filter((target) => target.enabled));
  const healthyModels = matrix.models.filter((model) => model.targets.some(isAvailable)).length;
  const healthyTargets = targets.filter(isAvailable).length;
  const coolingTargets = targets.filter((target) => target.health.circuitPhase === 'open').length;
  const waitingTargets = targets.filter((target) => !isAvailable(target) && target.health.circuitPhase !== 'open').length;

  return (
    <div className="page monitor-page v2-monitor-page">
      <PageHeader title="模型监控" description="自动探测已选择模型，并将结果直接用于网站冷却与恢复。" actions={<><Button icon={ListFilter} onClick={() => setPickerOpen(true)}>选择模型</Button><Button variant="primary" icon={RefreshCw} onClick={() => void state.refresh()} busy={state.loading}>刷新状态</Button></>} />

      <div className="metric-grid">
        <Metric label="监控模型" value={matrix.models.length} hint={`${healthyModels} 个存在可用网站`} tone={matrix.models.length && healthyModels === matrix.models.length ? 'success' : 'warning'} />
        <Metric label="可用网站" value={`${healthyTargets}/${targets.length}`} hint="以最近一次探测为准" tone={healthyTargets ? 'success' : 'danger'} />
        <Metric label="等待确认" value={waitingTargets} hint="未探测或首次失败" tone={waitingTargets ? 'warning' : 'neutral'} />
        <Metric label="冷却网站" value={coolingTargets} hint="到期后自动恢复验证" tone={coolingTargets ? 'danger' : 'neutral'} />
      </div>

      <Surface>
        <div className="toolbar">
          <div className="search-box"><ListFilter size={15} /><input className="input" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="筛选监控模型" /></div>
          <span className="toolbar-note">更新于 {relativeTime(matrix.generatedAt)}</span>
          <span className="toolbar-spacer" />
          <Button size="sm" variant="ghost" icon={Settings2} onClick={() => navigate('/settings')}>全局设置</Button>
        </div>

        <div className="monitor-list">
          {visibleModels.length === 0 ? <EmptyState title="没有监控模型" description="从模型选择器中开启需要自动探测的发布模型。" action={<Button icon={ListFilter} onClick={() => setPickerOpen(true)}>选择模型</Button>} /> : visibleModels.map((model) => {
            const available = model.targets.filter(isAvailable).length;
            const isCollapsed = collapsed.has(model.id);
            const modelBusy = probingModels.has(model.id);
            return (
              <div className="monitor-model" key={model.id}>
                <div className="monitor-model-header">
                  <button type="button" className="monitor-model-toggle" onClick={() => toggleCollapsed(model.id)} aria-expanded={!isCollapsed}>
                    <ChevronDown className={isCollapsed ? 'is-collapsed' : ''} size={16} />
                    <span className="monitor-model-name"><strong>{model.displayName || model.publicName}</strong><code>{model.publicName}</code></span>
                  </button>
                  <Badge tone={monitoredModelTone(model)}>{available}/{model.targets.filter((target) => target.enabled).length} 可用</Badge>
                  <div className="monitor-model-actions"><Button size="sm" variant="ghost" icon={RouteIcon} onClick={() => navigate(`/routes?model=${encodeURIComponent(model.publicName)}`)}>路由</Button><Button size="sm" variant="primary" icon={Play} busy={modelBusy} disabled={!model.targets.some((target) => target.enabled)} onClick={() => void probeModel(model)}>探测全部网站</Button></div>
                </div>
                {!isCollapsed && <div className="monitor-targets">
                  <div className="v2-monitor-target-head"><span>顺序</span><span>网站 / Endpoint</span><span>协议</span><span>实际模型</span><span>状态</span><span>总延迟</span><span>首输出</span><span>最近结果</span><span /></div>
                  {model.targets.length === 0 ? <EmptyState title="没有路由网站" description="先在模型路由中为这个模型添加网站。" /> : model.targets.map((target, index) => <TargetRow key={target.id} target={target} index={index} probing={probingTargets.has(target.id)} modelBusy={modelBusy} onProbe={(item) => void probeTarget(model, item)} />)}
                </div>}
              </div>
            );
          })}
        </div>
      </Surface>

      <Dialog open={pickerOpen} title="选择监控模型" description="只有已启用且已配置路由网站的模型会进入自动探测队列。" onClose={() => setPickerOpen(false)} width="lg" footer={<Button variant="primary" onClick={() => setPickerOpen(false)}>完成</Button>}>
        <div className="monitor-picker-list">
          {state.data!.publishedModels.map((model) => {
            const canMonitor = model.enabled && model.targets.some((target) => target.enabled);
            return <div className="monitor-picker-row" key={model.id}><div><strong>{model.displayName || model.publicName}</strong><code>{model.publicName}</code></div><span>{model.targets.filter((target) => target.enabled).length} 个网站 · {Math.round(model.monitorIntervalSeconds / 60 * 10) / 10} 分钟</span><Badge tone={model.enabled ? 'success' : 'neutral'}>{model.enabled ? '可调用' : '已停用'}</Badge><Switch checked={model.monitorEnabled} disabled={savingModels.has(model.id) || (!model.monitorEnabled && !canMonitor)} showLabel={false} label={`监控 ${model.displayName || model.publicName}`} onChange={(enabled) => void toggleMonitoring(model, enabled)} /></div>;
          })}
          {state.data!.publishedModels.length === 0 && <EmptyState title="没有发布模型" description="先在模型路由中发布至少一个模型。" action={<Button icon={RouteIcon} onClick={() => navigate('/routes')}>前往模型路由</Button>} />}
        </div>
      </Dialog>
    </div>
  );
}
