import { Check, FileJson, Search, Upload } from 'lucide-react';
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
  SearchField,
} from '../../components/ui';
import { ApiError, api } from '../../lib/api';
import { formatDateTime } from '../../lib/format';
import type { TokenJsonImportPreview } from '../../lib/types';

const MAX_TOKEN_JSON_BYTES = 256 * 1024;

const SAMPLE_TOKEN_JSON = `{
  "accounts": [
    {
      "name": "团队账号",
      "platform": "openai",
      "base_url": "https://relay.example.com/v1",
      "access_token": "token-value"
    }
  ]
}`;

export function TokenJsonImportDialog({
  open,
  siteId,
  onClose,
  onImported,
}: {
  open: boolean;
  siteId: number;
  onClose: () => void;
  onImported: () => void | Promise<void>;
}) {
  const toast = useToast();
  const fileRef = useRef<HTMLInputElement>(null);
  const [rawJson, setRawJson] = useState('');
  const [preview, setPreview] = useState<TokenJsonImportPreview | null>(null);
  const [selection, setSelection] = useState<Set<number>>(new Set());
  const [query, setQuery] = useState('');
  const [previewing, setPreviewing] = useState(false);
  const [importing, setImporting] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    if (!open) return;
    setRawJson('');
    setPreview(null);
    setSelection(new Set());
    setQuery('');
    setError('');
  }, [open]);

  useEffect(() => {
    if (!open || !preview) return;
    const expirePreview = () => {
      setPreview(null);
      setSelection(new Set());
      setError('导入预览已过期，请重新解析 Token JSON。');
    };
    const remaining = preview.expiresAt - Date.now();
    if (remaining <= 0) {
      expirePreview();
      return;
    }
    const timer = window.setTimeout(expirePreview, remaining + 50);
    return () => window.clearTimeout(timer);
  }, [open, preview]);

  const visibleItems = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return (preview?.items || []).filter((item) => !normalized || `${item.accountName} ${item.credentialName} ${item.platform} ${item.endpoint}`.toLowerCase().includes(normalized));
  }, [preview, query]);

  const previewJson = async () => {
    if (!rawJson.trim()) {
      setError('请粘贴 Token JSON 或选择一个 JSON 文件。');
      return;
    }
    if (new TextEncoder().encode(rawJson).byteLength > MAX_TOKEN_JSON_BYTES) {
      setError('Token JSON 不能超过 256 KiB。');
      return;
    }
    setPreviewing(true);
    setError('');
    try {
      const next = await api.previewTokenJsonImport(siteId, rawJson);
      if (next.siteId !== siteId) throw new ApiError('预览结果与当前站点不匹配，请重新打开导入窗口。', 409, 'token_preview_site_mismatch');
      setPreview(next);
      setSelection(new Set(next.items.filter((item) => item.status === 'ready').map((item) => item.index)));
    } catch (reason) {
      setPreview(null);
      setError(reason instanceof Error ? reason.message : 'Token JSON 解析失败');
    } finally {
      setPreviewing(false);
    }
  };

  const readFile = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (!file) return;
    try {
      if (file.size > MAX_TOKEN_JSON_BYTES) {
        setError('Token JSON 文件不能超过 256 KiB。');
        return;
      }
      setRawJson(await file.text());
      setPreview(null);
      setError('');
    } catch {
      setError('无法读取这个 JSON 文件。');
    } finally {
      event.target.value = '';
    }
  };

  const importSelected = async () => {
    if (!preview || !selection.size) return;
    if (preview.expiresAt <= Date.now()) {
      setPreview(null);
      setSelection(new Set());
      setError('导入预览已过期，请重新解析 Token JSON。');
      return;
    }
    setImporting(true);
    setError('');
    try {
      const indices = [...selection].sort((left, right) => left - right);
      const result = await api.importTokenJsonAccounts(siteId, preview.previewId, indices);
      await onImported();
      toast.show(`已导入 ${result.importedCount} 个账号，跳过 ${result.skippedCount} 个`, 'success');
      onClose();
    } catch (reason) {
      if (reason instanceof ApiError) {
        if (reason.code === 'token_preview_expired') {
          setPreview(null);
          setSelection(new Set());
          setError('预览已过期，或登录会话已经变化，请重新解析 Token JSON。');
        } else if (reason.code === 'token_preview_consumed' || reason.code === 'conflict') {
          setPreview(null);
          setSelection(new Set());
          setError(reason.code === 'conflict'
            ? '站点配置在预览后发生了变化，请重新解析并确认要导入的账号。'
            : '这份预览已经使用过，请重新解析后再导入。');
        } else if (reason.code === 'token_import_in_progress') {
          setError('同一份导入正在处理中，请稍候再试；重复提交不会创建重复账号。');
        } else {
          setError(reason.message);
        }
      } else {
        setError(reason instanceof Error ? reason.message : 'Token JSON 导入失败');
      }
    } finally {
      setImporting(false);
    }
  };

  return (
    <Dialog
      open={open}
      title="导入 Token JSON"
      description="先在本地预览账号、平台和目标地址；完整 Token 不会在预览结果中回显。"
      width="lg"
      onClose={onClose}
      footer={preview
        ? <><Button disabled={importing} onClick={() => { setPreview(null); setSelection(new Set()); setError(''); }}>返回编辑</Button><Button variant="primary" busy={importing} disabled={!selection.size} onClick={() => void importSelected()}>导入 {selection.size} 个账号</Button></>
        : <><Button onClick={onClose}>取消</Button><Button variant="primary" icon={Search} busy={previewing} onClick={() => void previewJson()}>解析并预览</Button></>}
    >
      {!preview ? <div className="form-stack token-json-editor">
        <input ref={fileRef} className="visually-hidden" type="file" accept="application/json,.json" onChange={(event) => void readFile(event)} />
        <div className="token-json-actions"><Button icon={Upload} onClick={() => fileRef.current?.click()}>选择 JSON 文件</Button><Button icon={FileJson} onClick={() => setRawJson(SAMPLE_TOKEN_JSON)}>填入示例</Button></div>
        <Field label="Token JSON" hint="支持单个账号、账号数组，以及 accounts / tokens 包装格式。">
          <textarea className="textarea code-input token-json-textarea" value={rawJson} onChange={(event) => { setRawJson(event.target.value); setError(''); }} placeholder={SAMPLE_TOKEN_JSON} spellCheck={false} />
        </Field>
        {previewing ? <LoadingState label="正在识别账号与字段" /> : error ? <ErrorState message={error} onRetry={() => void previewJson()} /> : <InlineNotice tone="info">导入前只展示 Token 首尾提示、账号名、平台、地址和权限范围。</InlineNotice>}
      </div> : <div className="token-json-preview">
        <div className="token-preview-summary">
          <div><span>可导入</span><strong>{preview.readyCount}</strong></div>
          <div><span>重复</span><strong>{preview.duplicateCount}</strong></div>
          <div><span>无效</span><strong>{preview.invalidCount}</strong></div>
          <div><span>识别格式</span><strong>{preview.detectedFormat}</strong></div>
        </div>
        <div className="token-preview-meta"><span>预览将在 {formatDateTime(preview.expiresAt, true)} 失效</span><span>导入请求仅提交所选账号的索引</span></div>
        <SearchField value={query} onChange={setQuery} placeholder="搜索账号、平台或地址" />
        {error && <InlineNotice tone="danger"><span aria-live="polite">{error}</span></InlineNotice>}
        {!visibleItems.length ? <EmptyState title="没有匹配的账号" description="调整搜索词或返回编辑 JSON。" /> : <div className="token-account-list">{visibleItems.map((item) => {
          const disabled = item.status !== 'ready';
          const checked = selection.has(item.index);
          return <label className={disabled ? 'is-disabled' : ''} key={item.index}>
            <input type="checkbox" disabled={disabled} checked={checked} onChange={() => setSelection((current) => { const next = new Set(current); if (next.has(item.index)) next.delete(item.index); else next.add(item.index); return next; })} />
            <span className="check-visual"><Check size={13} /></span>
            <div><strong>{item.accountName}</strong><span>{item.credentialName} · {item.platform}</span><code>{item.endpoint || '未提供地址'}</code></div>
            <div><code>{item.tokenHint}</code><small>{item.scopes.length ? item.scopes.join(' · ') : '未声明权限范围'}</small></div>
            <Badge tone={item.status === 'ready' ? 'success' : item.status === 'duplicate' ? 'warning' : 'danger'}>{item.status === 'ready' ? '可导入' : item.status === 'duplicate' ? '已存在' : '字段缺失'}</Badge>
            {item.warnings.length > 0 && <small className="token-account-warning">{item.warnings.join('；')}</small>}
          </label>;
        })}</div>}
      </div>}
    </Dialog>
  );
}
