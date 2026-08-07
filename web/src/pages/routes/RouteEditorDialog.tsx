import { ArrowDown, ArrowUp, Check, Search } from 'lucide-react';
import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { Badge, Button, Dialog, Disclosure, Field, IconButton, InlineNotice, Switch, submitForm } from '../../components/ui';
import { protocolLabel } from '../../lib/format';
import type { CreateDefaultRouteInput, ModelRoute, ModelTarget } from '../../lib/types';

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
  return '';
}

function credentialSummary(target: ModelTarget): string {
  const names = ((target as ModelTarget & { credentialNames?: string[] }).credentialNames || []).filter(Boolean);
  if (names.length <= 2 && names.length) return names.join('、');
  if (names.length > 2) return `${names.slice(0, 2).join('、')} +${names.length - 2}`;
  return `${protocolLabel(target.wireProtocol)} · ${target.usableCredentialCount} 个 API Key`;
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

  const visibleTargets = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return targets.filter((target) => {
      if (!showAllModels && target.sourceModel !== sourceModel) return false;
      return !normalized || `${target.siteName} ${target.wireProtocol} ${target.sourceModel}`.toLowerCase().includes(normalized);
    });
  }, [query, showAllModels, sourceModel, targets]);

  const toggleTarget = (target: ModelTarget) => {
    if (!target.routable || target.usableCredentialCount <= 0) return;
    setSelectedIds((current) => current.includes(target.id) ? current.filter((id) => id !== target.id) : [...current, target.id]);
  };

  const move = (id: number, direction: -1 | 1) => {
    setSelectedIds((current) => {
      const index = current.indexOf(id);
      const nextIndex = index + direction;
      if (index < 0 || nextIndex < 0 || nextIndex >= current.length) return current;
      const next = [...current];
      [next[index], next[nextIndex]] = [next[nextIndex], next[index]];
      return next;
    });
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!publicModel.trim() || !selectedIds.length) {
      setError('下游模型名和至少一个可路由上游不能为空');
      return;
    }
    setError('');
    if (route) await onReplaceTargets(route, selectedIds);
    else await onCreate({
      publicName: publicModel.trim(),
      officialPriceSku: priceSKU.trim() || publicModel.trim(),
      enabled,
      providerTargetIds: selectedIds,
    });
  };

  const selectedTargets = selectedIds.map((id) => targets.find((target) => target.id === id)).filter((target): target is ModelTarget => Boolean(target));

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
          <div className="route-selection-heading"><div><h3>选择上游 <span>{selectedIds.length} 个已选</span></h3><p>按站点展示可用 API Key；只有具备完整协议能力的上游可以加入路由。</p></div></div>
          <div className="route-selection-filters">
            <label className="inline-search"><Search size={15} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索站点、API 类型或模型" /></label>
            <div className="segmented" role="group" aria-label="上游模型范围"><button type="button" className={!showAllModels ? 'is-active' : ''} onClick={() => setShowAllModels(false)}>当前模型</button><button type="button" className={showAllModels ? 'is-active' : ''} onClick={() => setShowAllModels(true)}>全部映射</button></div>
            <span className="route-target-result-count">{visibleTargets.length} 个上游</span>
          </div>
          <div className="target-picker-list">
            {visibleTargets.map((target) => {
              const selected = selectedIds.includes(target.id);
              const unavailable = missingCapabilities(target);
              return <button type="button" aria-pressed={selected} className={`${selected ? 'is-selected' : ''} ${unavailable ? 'is-unavailable' : ''}`} key={target.id} onClick={() => toggleTarget(target)} disabled={Boolean(unavailable)}><span className="target-check"><Check size={13} /></span><div><strong>{target.siteName}</strong><span>{credentialSummary(target)} · {protocolLabel(target.wireProtocol)}</span></div><code>{target.sourceModel}</code>{unavailable ? <Badge tone="warning">{unavailable}</Badge> : <Badge tone="success">可路由</Badge>}</button>;
            })}
          </div>
          {!visibleTargets.length && <InlineNotice tone="warning">没有匹配的模型目标。请先在上游站点获取并导入模型。</InlineNotice>}
        </section>

        <section className="selected-route-order">
          <div><h3>故障切换顺序</h3><p>最上方优先。保存后仍可在模型卡片上直接拖动。</p></div>
          {selectedTargets.length ? <div>{selectedTargets.map((target, index) => <div key={target.id}><span>{index + 1}</span><div className="selected-route-target"><strong>{target.siteName}</strong><small>{protocolLabel(target.wireProtocol)}</small></div><code>{target.sourceModel}</code><IconButton label="上移" disabled={index === 0} onClick={() => move(target.id, -1)}><ArrowUp size={15} /></IconButton><IconButton label="下移" disabled={index === selectedTargets.length - 1} onClick={() => move(target.id, 1)}><ArrowDown size={15} /></IconButton></div>)}</div> : <InlineNotice>尚未选择上游。</InlineNotice>}
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
