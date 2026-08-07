import { Check, Download, RefreshCw, Search } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { useToast } from '../../components/Toast';
import { Badge, Button, Dialog, EmptyState, InlineNotice, LoadingState } from '../../components/ui';
import { api } from '../../lib/api';
import { protocolLabel } from '../../lib/format';
import type { DiscoveredModel, SiteCredential, SiteEndpoint } from '../../lib/types';

export function ModelDiscoveryDialog({
  open,
  siteId,
  credential,
  endpoint,
  onClose,
  onImported,
}: {
  open: boolean;
  siteId: number;
  credential: SiteCredential | null;
  endpoint: SiteEndpoint | null;
  onClose: () => void;
  onImported: () => Promise<void>;
}) {
  const toast = useToast();
  const [items, setItems] = useState<DiscoveredModel[] | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [query, setQuery] = useState('');
  const [loading, setLoading] = useState(false);
  const [importing, setImporting] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    if (!open) return;
    setItems(null);
    setSelected(new Set());
    setQuery('');
    setError('');
  }, [credential?.id, endpoint?.id, open]);

  const visibleItems = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return (items || []).filter((item) => !normalized || item.sourceModel.toLowerCase().includes(normalized));
  }, [items, query]);

  const discover = async () => {
    if (!endpoint || !credential) return;
    setLoading(true);
    setError('');
    try {
      const result = await api.discoverModels(siteId, endpoint.id, credential.id);
      setItems(result);
      setSelected(new Set(result.filter((item) => !item.imported).map((item) => item.sourceModel)));
    } catch (reason) {
      setItems(null);
      setError(reason instanceof Error ? reason.message : '获取模型失败');
    } finally {
      setLoading(false);
    }
  };

  const toggle = (model: string) => {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(model)) next.delete(model);
      else next.add(model);
      return next;
    });
  };

  const importSelected = async () => {
    if (!endpoint || !credential || selected.size === 0) return;
    setImporting(true);
    try {
      await api.importModels(siteId, endpoint.id, credential.id, [...selected]);
      toast.show(`已导入 ${selected.size} 个模型`, 'success');
      await onImported();
      onClose();
    } catch (reason) {
      toast.show(reason instanceof Error ? reason.message : '导入失败', 'error');
    } finally {
      setImporting(false);
    }
  };

  return (
    <Dialog
      open={open && Boolean(credential && endpoint)}
      title={`获取模型 · ${credential?.name || ''}`}
      description="直接使用这把 API Key 读取上游支持的全部模型。"
      onClose={onClose}
      width="lg"
      footer={<>
        <Button onClick={onClose}>取消</Button>
        <Button variant="primary" icon={Download} busy={importing} disabled={!selected.size} onClick={() => void importSelected()}>导入已选 ({selected.size})</Button>
      </>}
    >
      <div className="key-discovery-source">
        <div><span>API 类型</span><strong>{endpoint ? protocolLabel(endpoint.wireProtocol) : '-'}</strong></div>
        <div><span>API 地址</span><code>{endpoint?.baseUrl || '-'}</code></div>
        <Button icon={RefreshCw} busy={loading} disabled={!credential?.enabled} onClick={() => void discover()}>{items ? '重新获取' : '获取上游模型'}</Button>
      </div>

      {!credential?.enabled && <InlineNotice tone="warning">这把 API Key 已停用，启用后才能获取模型。</InlineNotice>}
      {error && <InlineNotice tone="danger">{error}</InlineNotice>}
      {loading ? <LoadingState label="正在读取上游模型列表" /> : items === null ? (
        <EmptyState title="尚未获取模型" description="点击“获取上游模型”，系统会使用当前 API 类型、地址和 Key 请求模型列表。" />
      ) : items.length === 0 ? (
        <EmptyState title="上游没有返回模型" description="请检查 API 地址、类型和密钥权限；空结果不会被伪造成模型。" />
      ) : (
        <div className="discovery-results">
          <label className="model-search"><Search size={15} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索模型名称" /></label>
          <div className="discovery-summary">
            <span>发现 {items.length} 个模型 · {items.filter((item) => item.imported).length} 个已导入</span>
            <button type="button" onClick={() => setSelected(new Set(items.filter((item) => !item.imported).map((item) => item.sourceModel)))}>选择全部未导入</button>
          </div>
          <div className="model-picker-list">
            {visibleItems.map((item) => (
              <label className={item.imported ? 'is-imported' : ''} key={item.sourceModel}>
                <input type="checkbox" checked={item.imported || selected.has(item.sourceModel)} disabled={item.imported} onChange={() => toggle(item.sourceModel)} />
                <span className="check-visual"><Check size={13} /></span>
                <code>{item.sourceModel}</code>
                {item.imported && <Badge tone="success">已导入</Badge>}
              </label>
            ))}
            {!visibleItems.length && <InlineNotice>没有匹配的模型，请调整搜索词。</InlineNotice>}
          </div>
        </div>
      )}
    </Dialog>
  );
}
