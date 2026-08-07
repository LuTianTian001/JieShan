import { BookOpenCheck, Eye, RefreshCw } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { useToast } from '../../components/Toast';
import {
  Badge,
  Button,
  Dialog,
  EmptyState,
  ErrorState,
  LoadingState,
  Panel,
  SearchField,
  SectionHeader,
  SegmentedControl,
  Switch,
  UnavailableState,
} from '../../components/ui';
import { api, ApiUnavailableError } from '../../lib/api';
import { formatDateTime, formatUSDFromNano } from '../../lib/format';
import { useResource } from '../../lib/hooks';
import type { PriceCatalog, PriceCatalogEntry, PriceCatalogList, PriceRate, PriceTokenClass } from '../../lib/types';

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
  const [automatic, setAutomatic] = useState(true);
  const [detailOpen, setDetailOpen] = useState(false);
  const [catalog, setCatalog] = useState<PriceCatalog | null>(null);
  const [catalogLoading, setCatalogLoading] = useState(false);
  const [query, setQuery] = useState('');
  const [provider, setProvider] = useState('all');

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

  const checkUpdates = async () => {
    await resource.refresh();
    toast.show('已检查官方价格来源，当前目录无需更新', 'success');
  };

  return (
    <>
      <Panel className="pricing-catalog-panel">
        <SectionHeader
          title="官方价格"
          description="下游总额度和每小时额度统一按美元结算；上游站点余额保持站点原始显示。"
          actions={<Button size="sm" icon={RefreshCw} busy={resource.loading} onClick={() => void checkUpdates()}>检查更新</Button>}
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
            <Switch checked={automatic} label="自动更新" onChange={setAutomatic} />
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
    </>
  );
}
