import { ChevronDown, ListFilter, Play, RefreshCw, Route as RouteIcon, Settings2 } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Button, Dialog, EmptyState, ErrorState, LoadingState, Metric, PageHeader, StatusBadge, Surface, Switch } from '../components/ui';
import { useToast } from '../components/Toast';
import { api } from '../lib/api';
import { formatLatency, formatRelativeTime } from '../lib/format';
import { useAsyncData } from '../lib/hooks';
import type { Route, RouteTarget } from '../lib/types';

function routeHealth(route: Route): 'healthy' | 'warning' | 'danger' {
  if (route.targets.some((target) => target.state === 'healthy')) return 'healthy';
  if (route.targets.some((target) => ['suspect', 'probing', 'unknown'].includes(target.state))) return 'warning';
  return 'danger';
}

function TargetRow({ route, target, index, probing, onProbe }: { route: Route; target: RouteTarget; index: number; probing: boolean; onProbe: (route: Route, target: RouteTarget) => void }) {
  return (
    <div className="monitor-target-row">
      <span className="route-rank">#{index + 1}</span>
      <div className="target-identity">
        <strong>{target.upstreamName}</strong>
        <span>{target.credentialName} · {target.sourceModel}</span>
      </div>
      <StatusBadge state={probing ? 'probing' : target.state} />
      <div className="target-metric"><span>延迟</span><strong>{formatLatency(target.latencyMs)}</strong></div>
      <div className="target-metric"><span>{target.state === 'cooldown' ? '恢复时间' : '最近情况'}</span><strong>{target.cooldownUntil ? formatRelativeTime(target.cooldownUntil) : target.lastFailure || '正常'}</strong></div>
      <Button size="sm" variant="ghost" icon={Play} busy={probing} onClick={() => onProbe(route, target)}>探针</Button>
    </div>
  );
}

export function MonitorPage() {
  const navigate = useNavigate();
  const toast = useToast();
  const state = useAsyncData(async () => {
    const [summary, matrix] = await Promise.all([api.dashboard(), api.monitor()]);
    return { summary, matrix };
  }, []);
  const [query, setQuery] = useState('');
  const [collapsed, setCollapsed] = useState<Set<number>>(new Set());
  const [pickerOpen, setPickerOpen] = useState(false);
  const [probing, setProbing] = useState<Set<number>>(new Set());

  const visibleRoutes = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return (state.data?.matrix.routes ?? []).filter((route) => route.monitored && (!normalized || `${route.model} ${route.displayName || ''}`.toLowerCase().includes(normalized)));
  }, [query, state.data]);

  const toggleCollapsed = (id: number) => {
    setCollapsed((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };

  const probeTarget = async (route: Route, target: RouteTarget) => {
    setProbing((current) => new Set(current).add(target.id));
    try {
      const updated = await api.probeRoute(route.id, target.id);
      state.setData((current) => current ? {
        ...current,
        matrix: { ...current.matrix, generatedAt: new Date().toISOString(), routes: current.matrix.routes.map((item) => item.id === updated.id ? updated : item) },
      } : current);
      toast.show(`${target.upstreamName} 探针已完成`, 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '探针失败', 'error');
    } finally {
      setProbing((current) => {
        const next = new Set(current);
        next.delete(target.id);
        return next;
      });
    }
  };

  const toggleMonitoring = async (route: Route, monitored: boolean) => {
    try {
      const updated = await api.updateRoute(route.id, { monitored });
      state.setData((current) => current ? {
        ...current,
        matrix: { ...current.matrix, routes: current.matrix.routes.map((item) => item.id === updated.id ? updated : item) },
      } : current);
      toast.show(monitored ? `已监控 ${route.displayName || route.model}` : `已停止监控 ${route.displayName || route.model}`, 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '保存失败', 'error');
    }
  };

  if (state.loading && !state.data) return <div className="page"><LoadingState label="正在读取健康矩阵" /></div>;
  if (state.error && !state.data) return <div className="page"><ErrorState message={state.error} onRetry={() => void state.refresh()} /></div>;

  const summary = state.data!.summary;
  const allRoutes = state.data!.matrix.routes;

  return (
    <div className="page monitor-page">
      <PageHeader
        title="模型监控"
        description={`仅探测已选择的模型，当前周期 ${Math.round(state.data!.matrix.probeIntervalSeconds / 60)} 分钟。健康状态会直接参与路由冷却。`}
        actions={<><Button icon={ListFilter} onClick={() => setPickerOpen(true)}>选择模型</Button><Button variant="primary" icon={RefreshCw} onClick={() => void state.refresh()} busy={state.loading}>刷新状态</Button></>}
      />

      <div className="metric-grid">
        <Metric label="监控模型" value={summary.monitoredModels} hint={`${summary.healthyModels} 个可用`} tone="success" />
        <Metric label="需要注意" value={summary.attentionTargets} hint="观察或凭据异常" tone={summary.attentionTargets ? 'warning' : 'neutral'} />
        <Metric label="冷却目标" value={summary.coolingTargets} hint="到期自动半开探测" tone={summary.coolingTargets ? 'danger' : 'neutral'} />
        <Metric label="24 小时成功率" value={`${summary.successRate24h.toFixed(2)}%`} hint={`${summary.requests24h} 次请求`} tone="success" />
      </div>

      <Surface>
        <div className="toolbar">
          <div className="search-box"><ListFilter size={15} /><input className="input" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="筛选监控模型" /></div>
          <span className="toolbar-note">状态更新时间 {formatRelativeTime(state.data!.matrix.generatedAt)}</span>
          <span className="toolbar-spacer" />
          <Button size="sm" variant="ghost" icon={Settings2} onClick={() => navigate('/settings')}>探针设置</Button>
        </div>

        <div className="monitor-list">
          {visibleRoutes.length === 0 ? <EmptyState title="没有匹配的监控模型" description="从模型选择器中启用需要持续探测的模型。" action={<Button icon={ListFilter} onClick={() => setPickerOpen(true)}>选择模型</Button>} /> : visibleRoutes.map((route) => {
            const health = routeHealth(route);
            const isCollapsed = collapsed.has(route.id);
            const available = route.targets.filter((target) => target.state === 'healthy').length;
            return (
              <section className="monitor-model" key={route.id}>
                <div className="monitor-model-header">
                  <button type="button" className="monitor-model-toggle" onClick={() => toggleCollapsed(route.id)} aria-expanded={!isCollapsed}>
                    <ChevronDown size={16} className={isCollapsed ? 'is-collapsed' : ''} />
                    <span className="monitor-model-name"><strong>{route.displayName || route.model}</strong><code>{route.model}</code></span>
                  </button>
                  <span className={`model-health model-health-${health}`}>{available}/{route.targets.length} 可用</span>
                  <Button size="sm" variant="ghost" icon={RouteIcon} className="model-route-link" onClick={() => navigate(`/routes?model=${encodeURIComponent(route.model)}`)}>查看路由</Button>
                </div>
                {!isCollapsed && <div className="monitor-targets">{route.targets.map((target, index) => <TargetRow key={target.id} route={route} target={target} index={index} probing={probing.has(target.id)} onProbe={probeTarget} />)}</div>}
              </section>
            );
          })}
        </div>
      </Surface>

      <Dialog open={pickerOpen} title="选择监控模型" description="未选择的模型不会产生定时探针请求。" onClose={() => setPickerOpen(false)} footer={<Button variant="primary" onClick={() => setPickerOpen(false)}>完成</Button>}>
        <div className="model-picker-list">
          {allRoutes.map((route) => (
            <div className="model-picker-row" key={route.id}>
              <div><strong>{route.displayName || route.model}</strong><code>{route.model}</code></div>
              <Switch checked={route.monitored} onChange={(checked) => void toggleMonitoring(route, checked)} label={route.monitored ? '监控中' : '未监控'} />
            </div>
          ))}
        </div>
      </Dialog>
    </div>
  );
}
