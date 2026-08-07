import { AlertTriangle, CheckCircle2, CircleHelp, RefreshCw, ScanSearch } from 'lucide-react';
import {
  Badge,
  Button,
  Disclosure,
  EmptyState,
  ErrorState,
  InlineNotice,
  LoadingState,
  Panel,
  SectionHeader,
} from '../../components/ui';
import { formatDateTime } from '../../lib/format';
import type { PlatformDetectionConfidence, SitePlatformDetection } from '../../lib/types';

function confidencePresentation(confidence: PlatformDetectionConfidence): { label: string; tone: 'success' | 'warning' | 'neutral' } {
  if (confidence === 'high') return { label: '高置信度', tone: 'success' };
  if (confidence === 'medium') return { label: '中等置信度', tone: 'warning' };
  if (confidence === 'low') return { label: '低置信度', tone: 'warning' };
  return { label: '置信度未知', tone: 'neutral' };
}

function evidenceSourceLabel(source: SitePlatformDetection['evidence'][number]['source']): string {
  return {
    well_known: '标准端点',
    response_header: '响应头',
    html_marker: '页面标记',
    api_shape: 'API 结构',
    manual: '人工指定',
  }[source];
}

function detectionPresentation(detection: SitePlatformDetection): {
  label: string;
  tone: 'success' | 'warning' | 'danger' | 'accent' | 'neutral';
} {
  if (detection.state === 'manual') return { label: '人工指定', tone: 'accent' };
  if (detection.verdict === 'trusted') return { label: '已识别', tone: 'success' };
  if (detection.verdict === 'possible') return { label: '待确认', tone: 'warning' };
  if (detection.errors.length > 0) return { label: '检测失败', tone: 'danger' };
  return { label: '未识别', tone: 'neutral' };
}

export function PlatformDetectionPanel({
  detection,
  loading,
  error,
  onRetry,
}: {
  detection: SitePlatformDetection | null;
  loading: boolean;
  error: string | null;
  onRetry: () => void;
}) {
  const confidence = confidencePresentation(detection?.confidence || 'unknown');
  const status = detection ? detectionPresentation(detection) : null;
  const selectedLabel = detection?.verdict === 'trusted' || detection?.verdict === 'possible'
    ? detection.selectedPlatformLabel
    : '';

  return (
    <Panel className="site-platform-panel">
      <SectionHeader
        title="平台识别"
        description="显示系统为何选择当前适配器；低置信度或证据冲突时不会伪装成确定结果。"
        actions={<div className="platform-detection-actions">
          {detection && <Badge tone={confidence.tone}>{confidence.label} · {detection.score}%</Badge>}
          <Button size="sm" icon={RefreshCw} busy={loading} onClick={onRetry}>重新检测</Button>
        </div>}
      />
      {loading && !detection ? <LoadingState label="正在读取平台识别证据" />
        : error && !detection ? <ErrorState message={error} onRetry={onRetry} />
          : !detection ? <EmptyState title="尚无平台识别结果" description="保存站点地址并完成首次连接后，系统会记录候选平台与判断证据。" />
            : <div className="platform-detection-content">
              {error && <InlineNotice tone="danger">最新平台识别请求失败。下面保留的是上一次结果：{error}</InlineNotice>}
              {detection.verdict === 'unknown' && detection.errors.length > 0 && <InlineNotice tone="danger">
                探测端点没有返回可确认的平台证据，本次结果不能用于自动选择适配器。
              </InlineNotice>}
              {detection.verdict === 'possible' && <InlineNotice tone="warning">
                当前只有可能候选，系统不会把它当作已确认平台；请核对下面的证据或人工指定适配器。
              </InlineNotice>}
              <div className="platform-detection-summary">
                <span className="platform-detection-icon"><ScanSearch size={19} /></span>
                <div><span>{detection.verdict === 'possible' ? '最佳候选' : '当前识别'}</span><strong>{selectedLabel || '尚未确认平台'}</strong><small>检查于 {formatDateTime(detection.checkedAt, true)}</small></div>
                <div><span>候选平台</span><strong>{detection.candidates.length}</strong><small>{detection.state === 'ambiguous' ? '存在相近候选，请核对证据' : '按证据权重排序'}</small></div>
                {status && <Badge tone={status.tone}>{status.label}</Badge>}
              </div>
              <Disclosure summary={<span className="platform-evidence-summary"><CircleHelp size={16} /><span><strong>查看识别证据</strong><small>{detection.evidence.length} 条证据 · {detection.errors.length} 个探测问题</small></span></span>}>
                {detection.candidates.length > 0 && <div className="platform-candidate-list">
                  {detection.candidates.map((candidate) => <div key={candidate.platform}>
                    <span className="platform-candidate-score">{candidate.score}%</span>
                    <div><strong>{candidate.label}</strong><small>{candidate.evidenceIds.length} 条证据支持</small></div>
                    <Badge tone={candidate.supported ? 'info' : 'neutral'}>{candidate.supported ? '可用适配器' : '尚不支持'}</Badge>
                  </div>)}
                </div>}
                {detection.evidence.length > 0 && <div className="platform-evidence-list">
                  {detection.evidence.map((item) => <div key={item.id}>
                    <span className={item.matched ? 'is-matched' : ''}>{item.matched ? <CheckCircle2 size={15} /> : <CircleHelp size={15} />}</span>
                    <div><strong>{item.signal}</strong><code>{item.observedValue}</code></div>
                    <small>{evidenceSourceLabel(item.source)} · 权重 {item.weight}</small>
                  </div>)}
                </div>}
                {detection.errors.length > 0 && <div className="platform-probe-error-list">
                  {detection.errors.map((item) => <div key={`${item.probeId}:${item.path}`}>
                    <span><AlertTriangle size={15} /></span>
                    <div><strong>{item.path || item.probeId}</strong><code>{item.code}{item.status ? ` · HTTP ${item.status}` : ''}</code></div>
                    <small>{item.message}</small>
                  </div>)}
                </div>}
              </Disclosure>
            </div>}
    </Panel>
  );
}
