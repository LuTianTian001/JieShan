import { BookOpenCheck, Eye, GitCompareArrows, RefreshCw, Upload } from 'lucide-react';
import { useEffect, useMemo, useRef, useState, type ChangeEvent } from 'react';
import { useToast } from '../../components/Toast';
import {
  Badge,
  Button,
  Dialog,
  EmptyState,
  ErrorState,
  Field,
  InlineNotice,
  LoadingState,
  Panel,
  SearchField,
  SectionHeader,
  SegmentedControl,
  UnavailableState,
} from '../../components/ui';
import { api, ApiUnavailableError } from '../../lib/api';
import { formatDateTime, formatUSDFromNano } from '../../lib/format';
import { useResource } from '../../lib/hooks';
import type { PriceCatalog, PriceCatalogEntry, PriceCatalogList, PriceCatalogPreview, PriceRate, PriceTokenClass } from '../../lib/types';

const tokenLabels: Record<PriceTokenClass, string> = {
  input: '输入',
  output: '输出',
  cache_read: '缓存读取',
  cache_write: '缓存写入',
  cache_write_5m: '缓存写入 5m',
  cache_write_1h: '缓存写入 1h',
  reasoning: '推理',
};

async function loadCatalogs(): Promise<PriceCatalogList | null> {
  try {
    return await api.listPriceCatalogs();
  } catch (error) {
    if (error instanceof ApiUnavailableError) return null;
    throw error;
  }
}

function pricePerMillion(rate: PriceRate): string {
  return `${tokenLabels[rate.class]} ${formatUSDFromNano(rate.nano_usd_per_million)} / 1M`;
}

function PriceRow({ entry }: { entry: PriceCatalogEntry }) {
  return (
    <tr>
      <td><div className="cell-stack"><code>{entry.sku}</code><span>{entry.model_pattern}</span></div></td>
      <td>{entry.provider}</td>
      <td><div className="pricing-rate-list">{entry.rates.map((rate) => <span key={rate.class}>{pricePerMillion(rate)}</span>)}</div></td>
      <td><Badge tone={entry.verification_status === 'verified' ? 'success' : 'warning'}>{entry.verification_status === 'verified' ? '已校验' : '待校验'}</Badge></td>
    </tr>
  );
}

export function PricingCatalogPanel() {
  const toast = useToast();
  const resource = useResource(loadCatalogs, []);
  const [checkingBuiltin, setCheckingBuiltin] = useState(false);
  const [detailOpen, setDetailOpen] = useState(false);
  const [catalog, setCatalog] = useState<PriceCatalog | null>(null);
  const [catalogLoading, setCatalogLoading] = useState(false);
  const [query, setQuery] = useState('');
  const [provider, setProvider] = useState('all');
  const candidateFileRef = useRef<HTMLInputElement>(null);
  const [candidateOpen, setCandidateOpen] = useState(false);
  const [candidateJson, setCandidateJson] = useState('');
  const [candidatePreview, setCandidatePreview] = useState<PriceCatalogPreview | null>(null);
  const [candidateQuery, setCandidateQuery] = useState('');
  const [candidateError, setCandidateError] = useState('');
  const [previewingCandidate, setPreviewingCandidate] = useState(false);
  const [importingCandidate, setImportingCandidate] = useState(false);
  const [activatingCandidate, setActivatingCandidate] = useState(false);
  const [candidateImported, setCandidateImported] = useState(false);

  const active = resource.data?.items.find((item) => item.active) || null;

  useEffect(() => {
    if (!detailOpen || !active) return;
    let current = true;
    setCatalogLoading(true);
    void api.getPriceCatalog(active.version)
      .then((value) => { if (current) setCatalog(value); })
      .catch((reason) => { if (current) toast.show(reason instanceof Error ? reason.message : '价格目录加载失败', 'error'); })
      .finally(() => { if (current) setCatalogLoading(false); });
    return () => { current = false; };
  }, [active?.version, detailOpen, toast]);

  const providers = useMemo(() => Array.from(new Set((catalog?.entries || []).map((item) => item.provider))).sort(), [catalog]);
  const filtered = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return (catalog?.entries || []).filter((entry) => {
      if (provider !== 'all' && entry.provider !== provider) return false;
      return !normalized || `${entry.sku} ${entry.provider} ${entry.model_pattern}`.toLowerCase().includes(normalized);
    });
  }, [catalog, provider, query]);

  const filteredDiffs = useMemo(() => {
    const normalized = candidateQuery.trim().toLowerCase();
    return (candidatePreview?.diff.entries || []).filter((item) => !normalized || item.sku.toLowerCase().includes(normalized));
  }, [candidatePreview?.diff.entries, candidateQuery]);

  useEffect(() => {
    if (!candidateOpen) return;
    setCandidateJson('');
    setCandidatePreview(null);
    setCandidateQuery('');
    setCandidateError('');
    setCandidateImported(false);
  }, [candidateOpen]);

  const checkUpdates = async () => {
    setCheckingBuiltin(true);
    try {
      const result = await api.ensureBuiltinPriceCatalog();
      await resource.refresh();
      const messages = {
        already_current: `内置价格目录 ${result.catalog_version} 已是当前版本`,
        installed: `已安装并启用内置价格目录 ${result.catalog_version}`,
        upgraded: `已升级并启用内置价格目录 ${result.catalog_version}`,
        activated_existing: `已启用现有内置价格目录 ${result.catalog_version}`,
        operator_catalog_preserved: '当前使用手动选择的价格目录，未自动覆盖',
      } satisfies Record<typeof result.outcome, string>;
      toast.show(messages[result.outcome], result.outcome === 'operator_catalog_preserved' ? 'info' : 'success');
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '内置价格目录检查失败', 'error');
    } finally {
      setCheckingBuiltin(false);
    }
  };

  const readCandidateFile = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (!file) return;
    try {
      setCandidateJson(await file.text());
      setCandidatePreview(null);
      setCandidateError('');
    } catch {
      setCandidateError('无法读取这个价格目录文件。');
    } finally {
      event.target.value = '';
    }
  };

  const previewCandidate = async () => {
    setPreviewingCandidate(true);
    setCandidateError('');
    try {
      const parsed = JSON.parse(candidateJson) as PriceCatalog;
      setCandidatePreview(await api.previewPriceCatalog(parsed));
      setCandidateImported(false);
    } catch (reason) {
      setCandidatePreview(null);
      setCandidateError(reason instanceof Error ? reason.message : '候选目录解析失败');
    } finally {
      setPreviewingCandidate(false);
    }
  };

  const importCandidate = async () => {
    if (!candidatePreview) return;
    setImportingCandidate(true);
    setCandidateError('');
    try {
      const result = await api.importPriceCatalog(candidatePreview.candidate, candidatePreview.diff.candidate_digest);
      setCandidateImported(true);
      await resource.refresh();
      toast.show(result.imported ? '候选价格目录已导入，等待激活' : '这个版本已存在，可直接激活', 'success');
    } catch (reason) {
      setCandidateError(reason instanceof Error ? reason.message : '候选目录导入失败');
    } finally {
      setImportingCandidate(false);
    }
  };

  const activateCandidate = async () => {
    if (!candidatePreview) return;
    setActivatingCandidate(true);
    setCandidateError('');
    try {
      await api.activatePriceCatalog(candidatePreview.candidate.version, resource.data?.state.revision ?? candidatePreview.state.revision);
      await resource.refresh();
      setCandidateOpen(false);
      toast.show(`价格目录 ${candidatePreview.candidate.version} 已激活`, 'success');
    } catch (reason) {
      setCandidateError(reason instanceof Error ? reason.message : '候选目录激活失败');
    } finally {
      setActivatingCandidate(false);
    }
  };

  return (
    <>
      <Panel className="pricing-catalog-panel">
        <SectionHeader
          title="官方价格"
          description="下游额度按美元结算；已校验价格快照随 JieShan 版本提供，不在线抓取或静默覆盖手动目录。"
          actions={<><Button size="sm" icon={Upload} onClick={() => setCandidateOpen(true)}>导入候选</Button><Button size="sm" icon={RefreshCw} busy={checkingBuiltin} onClick={() => void checkUpdates()}>检查内置目录</Button></>}
        />
        {resource.loading && !resource.data ? (
          <LoadingState label="正在读取价格目录" />
        ) : resource.error && !resource.data ? (
          <ErrorState message={resource.error} onRetry={() => void resource.refresh()} />
        ) : !resource.data ? (
          <UnavailableState title="价格目录暂不可用" description="当前实例尚未挂载官方价格目录。" />
        ) : (
          <div className="pricing-catalog-overview pricing-catalog-simple">
            <span className="pricing-icon"><BookOpenCheck size={20} /></span>
            <div>
              <span>当前生效版本</span>
              <strong>{active?.version || '尚未激活'}</strong>
              <small>{active ? `${active.entry_count} 个模型价格 · ${active.source}` : '等待后台首次同步'}</small>
            </div>
            <div>
              <span>最近校验</span>
              <strong>{active ? formatDateTime(active.verified_at, true) : '-'}</strong>
              <small>新请求固定使用发起时的价格版本</small>
            </div>
            <div>
              <span>更新方式</span>
              <strong>随版本发布</strong>
              <small>仅采用已校验、可追溯的官方价格快照</small>
            </div>
            <Button size="sm" icon={Eye} disabled={!active} onClick={() => setDetailOpen(true)}>查看价格</Button>
          </div>
        )}
      </Panel>

      <Dialog
        open={detailOpen}
        title="官方美元价格"
        description={active ? `目录 ${active.version} · 历史请求不会随价格更新而重算` : undefined}
        width="lg"
        onClose={() => setDetailOpen(false)}
        footer={<Button onClick={() => setDetailOpen(false)}>关闭</Button>}
      >
        {catalogLoading ? <LoadingState label="正在读取模型价格" /> : !catalog ? <EmptyState title="暂无价格数据" description="当前目录没有可展示的模型价格。" /> : (
          <div className="pricing-detail">
            <div className="pricing-detail-toolbar">
              <SearchField value={query} onChange={setQuery} placeholder="搜索模型或供应商" />
              <SegmentedControl
                value={provider}
                label="价格供应商"
                onChange={setProvider}
                items={[{ value: 'all', label: '全部', count: catalog.entries.length }, ...providers.map((item) => ({ value: item, label: item }))]}
              />
              <span>{filtered.length} 个模型</span>
            </div>
            <div className="data-scroller pricing-entry-table">
              <table className="data-table">
                <thead><tr><th>模型 SKU</th><th>供应商</th><th>官方价格（USD）</th><th>状态</th></tr></thead>
                <tbody>{filtered.map((entry) => <PriceRow entry={entry} key={entry.sku} />)}</tbody>
              </table>
            </div>
          </div>
        )}
      </Dialog>

      <Dialog
        open={candidateOpen}
        title="候选价格目录"
        description="先校验版本与摘要，再审阅模型价格差异；导入和激活是两个独立动作。"
        width="lg"
        onClose={() => setCandidateOpen(false)}
        footer={!candidatePreview
          ? <><Button onClick={() => setCandidateOpen(false)}>取消</Button><Button variant="primary" icon={GitCompareArrows} busy={previewingCandidate} onClick={() => void previewCandidate()}>校验并比较</Button></>
          : candidateImported
            ? <><Button onClick={() => setCandidateOpen(false)}>稍后激活</Button><Button variant="primary" busy={activatingCandidate} disabled={!candidatePreview.can_activate} onClick={() => void activateCandidate()}>激活此版本</Button></>
            : <><Button onClick={() => { setCandidatePreview(null); setCandidateError(''); }}>返回编辑</Button><Button variant="primary" busy={importingCandidate} onClick={() => void importCandidate()}>导入候选</Button></>}
      >
        {!candidatePreview ? <div className="form-stack pricing-candidate-editor">
          <input ref={candidateFileRef} className="visually-hidden" type="file" accept="application/json,.json" onChange={(event) => void readCandidateFile(event)} />
          <Button icon={Upload} onClick={() => candidateFileRef.current?.click()}>选择价格目录 JSON</Button>
          <Field label="候选目录 JSON" hint="版本号不可变；摘要、来源和生效时间会在下一步校验。">
            <textarea className="textarea code-input pricing-candidate-textarea" value={candidateJson} onChange={(event) => { setCandidateJson(event.target.value); setCandidateError(''); }} spellCheck={false} placeholder="粘贴完整 PriceCatalog JSON" />
          </Field>
          {previewingCandidate ? <LoadingState label="正在校验摘要并比较价格" /> : candidateError ? <ErrorState message={candidateError} onRetry={() => void previewCandidate()} /> : <InlineNotice tone="info">预览不会修改当前生效价格；只有显式激活后，新请求才会使用新版本。</InlineNotice>}
        </div> : <div className="pricing-candidate-preview">
          <div className="pricing-diff-summary">
            <div><span>新增</span><strong>{candidatePreview.diff.summary.added_entries}</strong></div>
            <div><span>变更</span><strong>{candidatePreview.diff.summary.changed_entries}</strong></div>
            <div><span>移除</span><strong>{candidatePreview.diff.summary.removed_entries}</strong></div>
            <div><span>未变化</span><strong>{candidatePreview.diff.summary.unchanged_entries}</strong></div>
          </div>
          <div className="pricing-candidate-meta"><div><span>候选版本</span><strong>{candidatePreview.candidate.version}</strong><code>{candidatePreview.diff.candidate_digest}</code></div><Badge tone={candidatePreview.can_activate ? 'success' : 'warning'}>{candidatePreview.can_activate ? '可以激活' : '尚未到生效时间'}</Badge></div>
          {candidateError && <ErrorState message={candidateError} />}
          {candidateImported && <InlineNotice tone="success">候选目录已导入并锁定摘要。激活只影响之后发起的请求。</InlineNotice>}
          <SearchField value={candidateQuery} onChange={setCandidateQuery} placeholder="搜索有差异的模型 SKU" />
          {!filteredDiffs.length ? <EmptyState title="没有匹配的价格差异" description={candidatePreview.diff.entries.length ? '调整搜索词查看其他 SKU。' : '候选目录与当前生效版本没有模型级差异。'} /> : <div className="pricing-diff-list">{filteredDiffs.map((item) => <div key={item.sku}>
            <span className={`pricing-diff-mark diff-${item.kind}`} />
            <div><code>{item.sku}</code><span>{item.rates?.length ? `${item.rates.length} 项费率变化` : item.metadata_changed ? '来源或校验元数据变化' : '模型条目变化'}</span></div>
            <Badge tone={item.kind === 'added' ? 'success' : item.kind === 'removed' ? 'danger' : 'warning'}>{item.kind === 'added' ? '新增' : item.kind === 'removed' ? '移除' : '变更'}</Badge>
          </div>)}</div>}
        </div>}
      </Dialog>
    </>
  );
}
