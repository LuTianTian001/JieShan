import { ArrowDown, ArrowUp, Check, Search } from 'lucide-react';
import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { Badge, Button, Dialog, Disclosure, Field, IconButton, InlineNotice, Switch, submitForm } from '../../components/ui';
import type { CreateDefaultRouteInput, ModelRoute, ModelTarget } from '../../lib/types';
import {
  flattenUpstreamGroups,
  groupUpstreamTargets,
  upstreamChannelSummary,
  type UpstreamGroup,
} from '../../lib/upstreamGroups';

function missingCapabilities(target: ModelTarget): string {
  const labels: Record<keyof ModelTarget['capabilities'], string> = {
    discovery: '发现',
    request: '请求转换',
    response: '响应转换',
    stream: '流式',
    usage: '用量',
    error: '错误转换',
  };
  const missing = (Object.keys(labels) as Array<keyof typeof labels>).filter((key) => !target.capabilities[key]);
  if (missing.length) return `缺少 ${missing.map((key) => labels[key]).join('、')}`;
  if (!target.usableCredentialCount) return '没有可用 API Key';
  if (!target.siteEnabled || !target.endpointEnabled || !target.enabled) return '站点、API Key 或模型已停用';
  if (!target.routable) return '当前通道不可路由';
  return '';
}

function isRoutableTarget(target: ModelTarget): boolean {
  return !missingCapabilities(target);
}

function groupUnavailableReason(group: UpstreamGroup<ModelTarget>): string {
  const reasons = [...new Set(group.targets.map(missingCapabilities).filter(Boolean))];
  if (!reasons.length || group.targets.some(isRoutableTarget)) return '';
  return reasons.length === 1 ? reasons[0] : '没有可路由通道';
}

function credentialSummary(targets: readonly ModelTarget[]): string {
  const names = [...new Set(targets.flatMap((target) => target.credentialNames || []).filter(Boolean))];
  if (names.length <= 2 && names.length) return names.join('、');
  if (names.length > 2) return `${names.slice(0, 2).join('、')} +${names.length - 2}`;
  const usableCredentialCount = Math.max(0, ...targets.map((target) => target.usableCredentialCount));
  return `${usableCredentialCount} 个 API Key`;
}

export function RouteEditorDialog({
  open,
  route,
  targets,
  saving,
  onClose,
  onCreate,
  onReplaceTargets,
}: {
  open: boolean;
  route: ModelRoute | null;
  targets: ModelTarget[];
  saving: boolean;
  onClose: () => void;
  onCreate: (input: CreateDefaultRouteInput) => Promise<void>;
  onReplaceTargets: (route: ModelRoute, targetIds: number[]) => Promise<void>;
}) {
  const modelNames = useMemo(() => [...new Set(targets.map((target) => target.sourceModel))].sort(), [targets]);
  const targetGroups = useMemo(() => groupUpstreamTargets(targets), [targets]);
  const targetsById = useMemo(() => new Map(targets.map((target) => [target.id, target])), [targets]);
  const [sourceModel, setSourceModel] = useState('');
  const [publicModel, setPublicModel] = useState('');
  const [priceSKU, setPriceSKU] = useState('');
  const [enabled, setEnabled] = useState(true);
  const [selectedIds, setSelectedIds] = useState<number[]>([]);
  const [query, setQuery] = useState('');
  const [showAllModels, setShowAllModels] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    if (!open) return;
    const firstSource = route?.targets[0]?.sourceModel || modelNames[0] || '';
    setSourceModel(firstSource);
    setPublicModel(route?.publicName || firstSource);
    setPriceSKU(route?.officialPriceSku || route?.publicName || firstSource);
    setEnabled(route?.enabled ?? true);
    setSelectedIds(route?.targets.map((target) => target.providerModelTargetId) || []);
    setQuery('');
    setShowAllModels(Boolean(route && new Set(route.targets.map((target) => target.sourceModel)).size > 1));
    setError('');
  }, [modelNames, open, route]);

  const chooseSourceModel = (next: string) => {
    const wasDefault = !publicModel || publicModel === sourceModel;
    const priceWasDefault = !priceSKU || priceSKU === sourceModel || priceSKU === publicModel;
    setSourceModel(next);
    if (wasDefault) setPublicModel(next);
    if (priceWasDefault) setPriceSKU(next);
    if (!route) setSelectedIds([]);
  };

  const visibleGroups = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return targetGroups.filter((group) => {
      if (!showAllModels && group.sourceModel !== sourceModel) return false;
      const searchable = `${group.siteName} ${group.sourceModel} ${upstreamChannelSummary(group.targets)} ${group.targets.map((target) => `${target.endpointName} ${target.wireProtocol}`).join(' ')}`;
      return !normalized || searchable.toLowerCase().includes(normalized);
    });
  }, [query, showAllModels, sourceModel, targetGroups]);

  const selectedTargets = useMemo(
    () => selectedIds.map((id) => targetsById.get(id)).filter((target): target is ModelTarget => Boolean(target)),
    [selectedIds, targetsById],
  );
  const selectedGroups = useMemo(() => groupUpstreamTargets(selectedTargets), [selectedTargets]);
  const selectedGroupKeys = useMemo(() => new Set(selectedGroups.map((group) => group.key)), [selectedGroups]);
  const normalizedSelectedIds = useMemo(() => {
    const knownIds = new Set(selectedTargets.map((target) => target.id));
    return [
      ...flattenUpstreamGroups(selectedGroups, (target) => target.id),
      ...selectedIds.filter((id) => !knownIds.has(id)),
    ];
  }, [selectedGroups, selectedIds, selectedTargets]);

  const toggleGroup = (group: UpstreamGroup<ModelTarget>) => {
    const routableIds = group.targets.filter(isRoutableTarget).map((target) => target.id);
    if (!routableIds.length) return;
    const groupIds = new Set(group.targets.map((target) => target.id));
    setSelectedIds((current) => current.some((id) => groupIds.has(id))
      ? current.filter((id) => !groupIds.has(id))
      : [...current, ...routableIds.filter((id) => !current.includes(id))]);
  };

  const move = (groupKey: string, direction: -1 | 1) => {
    setSelectedIds((current) => {
      const knownTargets = current.map((id) => targetsById.get(id)).filter((target): target is ModelTarget => Boolean(target));
      const groups = groupUpstreamTargets(knownTargets);
      const index = groups.findIndex((group) => group.key === groupKey);
      const nextIndex = index + direction;
      if (index < 0 || nextIndex < 0 || nextIndex >= groups.length) return current;
      const next = [...groups];
      [next[index], next[nextIndex]] = [next[nextIndex], next[index]];
      const knownIds = new Set(knownTargets.map((target) => target.id));
      const unknownIds = current.filter((id) => !knownIds.has(id));
      return [...flattenUpstreamGroups(next, (target) => target.id), ...unknownIds];
    });
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!publicModel.trim() || !selectedIds.length) {
      setError('下游模型名和至少一个可路由上游不能为空');
      return;
    }
    setError('');
    if (route) await onReplaceTargets(route, normalizedSelectedIds);
    else await onCreate({
      publicName: publicModel.trim(),
      officialPriceSku: priceSKU.trim() || publicModel.trim(),
      enabled,
      providerTargetIds: normalizedSelectedIds,
    });
  };

  return (
    <Dialog
      open={open}
      title={route ? `调整上游 · ${route.publicName}` : '发布模型'}
      description={route ? '选择参与路由的上游，并按实际故障切换顺序排列。' : '先选上游模型，再选择支持它的站点；下游模型名默认保持一致。'}
      onClose={onClose}
      width="lg"
      footer={<><Button onClick={onClose}>取消</Button><Button variant="primary" busy={saving} onClick={() => submitForm('route-editor-form')}>{route ? '保存上游列表' : '发布模型'}</Button></>}
    >
      <form id="route-editor-form" className="route-editor" onSubmit={submit}>
        {!route && <div className="form-grid two-columns">
          <Field label="上游模型" required>
            <select className="select code-input" value={sourceModel} onChange={(event) => chooseSourceModel(event.target.value)}>
              {!modelNames.length && <option value="">没有已导入模型</option>}
              {modelNames.map((model) => <option key={model} value={model}>{model}</option>)}
            </select>
          </Field>
          <Field label="下游模型名" required hint="默认与上游模型一致；需要别名时再修改。">
            <input className="input code-input" value={publicModel} onChange={(event) => setPublicModel(event.target.value)} />
          </Field>
        </div>}

        <section className="route-selection-section">
          <div className="route-selection-heading"><div><h3>选择上游 <span>{selectedGroups.length} 个已选</span></h3><p>按站点和模型聚合协议通道；选择后会加入该上游当前所有可路由通道。</p></div></div>
          <div className="route-selection-filters">
            <label className="inline-search"><Search size={15} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索站点、API 类型或模型" /></label>
            <div className="segmented" role="group" aria-label="上游模型范围"><button type="button" className={!showAllModels ? 'is-active' : ''} onClick={() => setShowAllModels(false)}>当前模型</button><button type="button" className={showAllModels ? 'is-active' : ''} onClick={() => setShowAllModels(true)}>全部映射</button></div>
            <span className="route-target-result-count">{visibleGroups.length} 个上游</span>
          </div>
          <div className="target-picker-list">
            {visibleGroups.map((group) => {
              const selected = selectedGroupKeys.has(group.key);
              const unavailable = groupUnavailableReason(group);
              const routableChannelCount = group.targets.filter(isRoutableTarget).length;
              const availabilityLabel = routableChannelCount === group.targets.length ? '可路由' : `${routableChannelCount}/${group.targets.length} 通道可路由`;
              const channelSummary = upstreamChannelSummary(group.targets);
              return <button type="button" aria-pressed={selected} className={`${selected ? 'is-selected' : ''} ${unavailable ? 'is-unavailable' : ''}`} key={group.key} onClick={() => toggleGroup(group)} disabled={Boolean(unavailable)}><span className="target-check"><Check size={13} /></span><div><strong>{group.siteName}</strong><span title={channelSummary}>{credentialSummary(group.targets)} · {channelSummary}</span></div><code>{group.sourceModel}</code>{unavailable ? <Badge tone="warning">{unavailable}</Badge> : <Badge tone="success">{availabilityLabel}</Badge>}</button>;
            })}
          </div>
          {!visibleGroups.length && <InlineNotice tone="warning">没有匹配的模型目标。请先在上游站点获取并导入模型。</InlineNotice>}
        </section>

        <section className="selected-route-order">
          <div><h3>故障切换顺序</h3><p>最上方优先。保存后仍可在模型卡片上直接拖动。</p></div>
          {selectedGroups.length ? <div>{selectedGroups.map((group, index) => <div key={group.key}><span>{index + 1}</span><div className="selected-route-target"><strong>{group.siteName}</strong><small>{upstreamChannelSummary(group.targets)}</small></div><code>{group.sourceModel}</code><IconButton label="上移" disabled={index === 0} onClick={() => move(group.key, -1)}><ArrowUp size={15} /></IconButton><IconButton label="下移" disabled={index === selectedGroups.length - 1} onClick={() => move(group.key, 1)}><ArrowDown size={15} /></IconButton></div>)}</div> : <InlineNotice>尚未选择上游。</InlineNotice>}
        </section>

        {!route && <Disclosure summary="高级计费映射">
          <Field label="官方价格 SKU" hint="默认使用下游模型名。只有官方目录中的 SKU 不同时才修改。"><input className="input code-input" value={priceSKU} onChange={(event) => setPriceSKU(event.target.value)} /></Field>
          <Switch checked={enabled} label="创建后立即发布" onChange={setEnabled} />
        </Disclosure>}
        {error && <p className="form-error">{error}</p>}
      </form>
    </Dialog>
  );
}
