import { AlertCircle, CheckCircle2, RefreshCw, Search, Waypoints } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { Badge, Button, EmptyState } from '../../components/ui';
import { useToast } from '../../components/Toast';
import { api, ApiError } from '../../lib/api';
import type { ModelDiscovery, V2DiscoveryStrategy, V2ModelDiscovery } from '../../lib/types';
import type { SiteCredentialView, SiteEndpointView, UpstreamSiteView } from './siteAdapter';

interface DiscoverySelection {
  endpoint: SiteEndpointView | null;
  credential: SiteCredentialView | null;
  strategy: V2DiscoveryStrategy;
}

type DiscoveryState =
  | { phase: 'idle' }
  | { phase: 'loading'; selection: DiscoverySelection }
  | { phase: 'error'; selection: DiscoverySelection; message: string }
  | { phase: 'empty'; selection: DiscoverySelection }
  | { phase: 'v2-result'; selection: DiscoverySelection; discovery: V2ModelDiscovery; requestFailed: boolean }
  | { phase: 'legacy-result'; selection: DiscoverySelection; discovery: ModelDiscovery }
  | { phase: 'applying'; selection: DiscoverySelection; discovery: ModelDiscovery };

interface Props {
  site: UpstreamSiteView;
  autoDiscover?: boolean;
  onAutoDiscoverHandled?: () => void;
  onChanged: () => Promise<void>;
}

const strategyOptions: Array<{ value: V2DiscoveryStrategy; label: string; title: string }> = [
  { value: 'first_success', label: '依次尝试', title: '按 Key 顺序尝试，首枚成功后停止' },
  { value: 'selected', label: '指定 Key', title: '只使用你选中的一枚 Key' },
  { value: 'all', label: '全部检测', title: '检测全部启用 Key，并合并模型结果' },
];

export function ModelDiscoveryPanel({ site, autoDiscover = false, onAutoDiscoverHandled, onChanged }: Props) {
  const toast = useToast();
  const enabledCredentials = useMemo(() => site.credentials.filter((item) => item.enabled), [site.credentials]);
  const enabledEndpoints = useMemo(() => site.endpoints.filter((item) => item.enabled), [site.endpoints]);
  const [credentialId, setCredentialId] = useState(enabledCredentials[0]?.id ?? site.credentials[0]?.id ?? '');
  const [endpointId, setEndpointId] = useState(enabledEndpoints[0]?.id ?? site.endpoints[0]?.id ?? '');
  const [strategy, setStrategy] = useState<V2DiscoveryStrategy>(site.sourceVersion === 'v2' ? 'first_success' : 'selected');
  const [state, setState] = useState<DiscoveryState>({ phase: 'idle' });
  const autoStarted = useRef(false);
  const selectedCredential = site.credentials.find((item) => item.id === credentialId) ?? enabledCredentials[0] ?? site.credentials[0] ?? null;
  const selectedEndpoint = site.endpoints.find((item) => item.id === endpointId) ?? enabledEndpoints[0] ?? site.endpoints[0] ?? null;
  const selection = useMemo<DiscoverySelection>(() => ({ endpoint: selectedEndpoint, credential: selectedCredential, strategy }), [selectedEndpoint, selectedCredential, strategy]);
  const busy = state.phase === 'loading' || state.phase === 'applying';

  useEffect(() => {
    if (!selectedCredential && site.credentials[0]) setCredentialId(site.credentials[0].id);
  }, [selectedCredential, site.credentials]);

  useEffect(() => {
    if (!selectedEndpoint && site.endpoints[0]) setEndpointId(site.endpoints[0].id);
  }, [selectedEndpoint, site.endpoints]);

  const discover = async (nextSelection = selection) => {
    if (!nextSelection.credential && nextSelection.strategy === 'selected') {
      setState({ phase: 'error', selection: nextSelection, message: '请先添加并启用一枚 API Key。' });
      return;
    }
    if (site.sourceVersion === 'v2') {
      if (site.siteId === null || nextSelection.endpoint?.endpointId == null) {
        setState({ phase: 'error', selection: nextSelection, message: '请先配置并启用一个接入地址。' });
        return;
      }
      setState({ phase: 'loading', selection: nextSelection });
      try {
        const discovery = await api.discoverV2Models(site.siteId, {
          endpointId: nextSelection.endpoint.endpointId,
          credentialId: nextSelection.strategy === 'selected' ? nextSelection.credential?.credentialId ?? undefined : undefined,
          strategy: nextSelection.strategy,
        });
        if (discovery.applied) await onChanged();
        setState(discovery.models.length === 0 && discovery.attempts.length === 0
          ? { phase: 'empty', selection: nextSelection }
          : { phase: 'v2-result', selection: nextSelection, discovery, requestFailed: false });
        if (discovery.applied) toast.show(`已更新 ${discovery.models.length} 个上游模型`, 'success');
      } catch (reason) {
        if (reason instanceof ApiError && isV2Discovery(reason.body)) {
          setState({ phase: 'v2-result', selection: nextSelection, discovery: reason.body, requestFailed: true });
        } else {
          setState({ phase: 'error', selection: nextSelection, message: reason instanceof Error ? reason.message : '模型发现失败' });
        }
      }
      return;
    }

    const credential = nextSelection.credential;
    if (!credential?.upstreamId) {
      setState({ phase: 'error', selection: nextSelection, message: '当前站点没有可用于读取模型的 API Key。' });
      return;
    }
    setState({ phase: 'loading', selection: nextSelection });
    try {
      const discovery = await api.discoverModels(credential.upstreamId);
      const count = discovery.added.length + discovery.removed.length + discovery.unchanged.length;
      setState(count === 0 ? { phase: 'empty', selection: nextSelection } : { phase: 'legacy-result', selection: nextSelection, discovery });
    } catch (reason) {
      setState({ phase: 'error', selection: nextSelection, message: reason instanceof Error ? reason.message : '模型发现失败' });
    }
  };

  useEffect(() => {
    if (!autoDiscover || autoStarted.current) return;
    autoStarted.current = true;
    onAutoDiscoverHandled?.();
    void discover(selection);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoDiscover, selection]);

  const applyLegacy = async () => {
    if (state.phase !== 'legacy-result') return;
    const upstreamId = state.selection.credential?.upstreamId;
    if (upstreamId == null) return;
    const current = state;
    setState({ phase: 'applying', selection: current.selection, discovery: current.discovery });
    try {
      await api.applyModels(upstreamId, current.discovery);
      await onChanged();
      setState({ phase: 'idle' });
      toast.show('模型列表已更新', 'success');
    } catch (reason) {
      setState({ phase: 'error', selection: current.selection, message: reason instanceof Error ? reason.message : '应用模型失败' });
    }
  };

  return (
    <section className="upstream-section discovery-section">
      <div className="upstream-section-heading discovery-heading">
        <div><Waypoints size={17} /><div><h2>获取上游模型</h2><p>选择接入地址和检测方式；结果只在当前区域展示，不会跳到空白页。</p></div></div>
        <Button icon={Search} busy={state.phase === 'loading'} disabled={busy || !site.credentials.length || !site.endpoints.length} onClick={() => void discover()}>开始获取</Button>
      </div>

      <div className="discovery-controls">
        {site.sourceVersion === 'v2' && (
          <label><span>接入地址</span><select className="select" value={selectedEndpoint?.id ?? ''} disabled={busy || !site.endpoints.length} onChange={(event) => setEndpointId(event.target.value)}>{site.endpoints.map((endpoint) => <option value={endpoint.id} key={endpoint.id}>{endpoint.name} · {protocolLabel(endpoint.protocol)}{endpoint.enabled ? '' : '（已停用）'}</option>)}</select></label>
        )}
        {site.sourceVersion === 'v2' && (
          <div className="discovery-strategy" aria-label="Key 检测方式">
            <span>检测方式</span>
            <div>{strategyOptions.map((option) => <button type="button" className={strategy === option.value ? 'is-active' : ''} title={option.title} disabled={busy} aria-pressed={strategy === option.value} onClick={() => setStrategy(option.value)} key={option.value}>{option.label}</button>)}</div>
          </div>
        )}
        {(site.sourceVersion === 'legacy' || strategy === 'selected') && (
          <label><span>API Key</span><select className="select" value={selectedCredential?.id ?? ''} disabled={busy || !site.credentials.length} onChange={(event) => setCredentialId(event.target.value)}>{site.credentials.map((credential) => <option value={credential.id} key={credential.id}>{credential.name}{credential.enabled ? '' : '（已停用）'}</option>)}</select></label>
        )}
      </div>

      <div className={`discovery-state discovery-${state.phase}`} aria-live="polite">
        {state.phase === 'idle' && <div className="discovery-idle"><Search size={22} /><div><strong>等待获取</strong><span>{site.sourceVersion === 'v2' ? '默认按 Key 顺序尝试，成功后自动更新模型清单。' : '结果会先预览，再由你确认应用。'}</span></div></div>}
        {state.phase === 'loading' && <div className="discovery-loading"><RefreshCw className="spin" size={22} /><div><strong>正在读取模型列表</strong><span>{selectionDescription(state.selection)}</span></div></div>}
        {state.phase === 'error' && <div className="discovery-error"><AlertCircle size={22} /><div><strong>模型获取失败</strong><span>{state.message}</span><small>{selectionDescription(state.selection)}</small></div><Button size="sm" icon={RefreshCw} onClick={() => void discover(state.selection)}>重试</Button></div>}
        {state.phase === 'empty' && <EmptyState title="上游没有返回模型" description={`${selectionDescription(state.selection)}，但没有得到可展示的模型。`} action={<Button size="sm" icon={RefreshCw} onClick={() => void discover(state.selection)}>重新获取</Button>} />}
        {state.phase === 'v2-result' && <V2DiscoveryResult discovery={state.discovery} requestFailed={state.requestFailed} />}
        {(state.phase === 'legacy-result' || state.phase === 'applying') && <LegacyDiscoveryResult state={state} onApply={() => void applyLegacy()} />}
      </div>
    </section>
  );
}

function V2DiscoveryResult({ discovery, requestFailed }: { discovery: V2ModelDiscovery; requestFailed: boolean }) {
  const failed = discovery.attempts.filter((attempt) => Boolean(attempt.error)).length;
  const successful = discovery.attempts.length - failed;
  const ResultIcon = requestFailed ? AlertCircle : CheckCircle2;
  return (
    <div className="discovery-result">
      <div className={`discovery-result-heading${requestFailed ? ' is-error' : ''}`}>
        <div><ResultIcon size={20} /><span><strong>{requestFailed ? '本次没有可应用的模型' : discovery.applied ? '模型清单已自动更新' : '模型已读取'}</strong><small>{discovery.complete ? '完整响应' : '部分 Key 或分页未完成'}</small></span></div>
        <div className="discovery-result-actions"><Badge tone={discovery.models.length ? 'success' : 'neutral'}>{discovery.models.length} 个模型</Badge><Badge tone={failed ? 'warning' : 'success'}>{successful} 成功 / {failed} 失败</Badge>{discovery.applied && <Badge tone="info">已应用</Badge>}</div>
      </div>
      <div className="discovery-attempts">
        {discovery.attempts.map((attempt) => (
          <div className={attempt.error ? 'is-error' : 'is-success'} key={attempt.credentialId}>
            <span>{attempt.error ? <AlertCircle size={16} /> : <CheckCircle2 size={16} />}</span>
            <div><strong>{attempt.credentialName}</strong><small>{attempt.error || `${attempt.models.length} 个模型 · ${attempt.pagesFetched} 页${attempt.complete ? '' : ' · 响应不完整'}`}</small></div>
            <Badge tone={attempt.error ? 'danger' : 'success'}>{attempt.error ? '失败' : '成功'}</Badge>
          </div>
        ))}
      </div>
      {!discovery.complete && <p className="discovery-incomplete">部分检测未完成；成功 Key 返回的模型仍会保留，失败原因已逐项列出。</p>}
    </div>
  );
}

function LegacyDiscoveryResult({ state, onApply }: { state: Extract<DiscoveryState, { phase: 'legacy-result' | 'applying' }>; onApply: () => void }) {
  const { discovery, selection } = state;
  const changed = discovery.added.length + discovery.removed.length;
  return (
    <div className="discovery-result">
      <div className="discovery-result-heading">
        <div><CheckCircle2 size={20} /><span><strong>{changed ? '发现结果待应用' : '模型列表已是最新'}</strong><small>{selection.credential?.name || 'API Key'} · {discovery.complete ? '完整响应' : '不完整响应'}</small></span></div>
        <div className="discovery-result-actions"><Badge tone="success">+{discovery.added.length} 新增</Badge><Badge tone={discovery.removed.length ? 'warning' : 'neutral'}>-{discovery.removed.length} 移除</Badge><Badge>{discovery.unchanged.length} 未变化</Badge><Button variant="primary" busy={state.phase === 'applying'} disabled={!discovery.complete || !changed} onClick={onApply}>{changed ? '确认应用' : '无需应用'}</Button></div>
      </div>
      {(discovery.added.length > 0 || discovery.removed.length > 0) && <div className="discovery-diff">{discovery.added.length > 0 && <div><strong>新增</strong><div>{discovery.added.map((model) => <code key={`add-${model}`}>{model}</code>)}</div></div>}{discovery.removed.length > 0 && <div><strong>待移除</strong><div>{discovery.removed.map((model) => <code key={`remove-${model}`}>{model}</code>)}</div></div>}</div>}
      {!discovery.complete && <p className="discovery-incomplete">响应不完整，现有模型不会被覆盖。</p>}
    </div>
  );
}

function selectionDescription(selection: DiscoverySelection): string {
  const endpoint = selection.endpoint?.name;
  if (selection.strategy === 'all') return `${endpoint ? `${endpoint} · ` : ''}检测全部启用 Key`;
  if (selection.strategy === 'first_success') return `${endpoint ? `${endpoint} · ` : ''}按顺序依次尝试`;
  return `${endpoint ? `${endpoint} · ` : ''}${selection.credential?.name || '未选择 Key'}`;
}

function protocolLabel(protocol: string): string {
  if (protocol === 'compatible') return 'OpenAI 兼容';
  if (protocol === 'openai') return 'OpenAI';
  if (protocol === 'anthropic') return 'Anthropic';
  if (protocol === 'gemini') return 'Gemini';
  return protocol;
}

function isV2Discovery(value: unknown): value is V2ModelDiscovery {
  if (!value || typeof value !== 'object') return false;
  const candidate = value as Partial<V2ModelDiscovery>;
  return typeof candidate.siteId === 'number' && Array.isArray(candidate.models) && Array.isArray(candidate.attempts);
}
