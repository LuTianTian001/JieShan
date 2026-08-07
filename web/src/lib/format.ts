import type { AuthScheme, InferenceSurface, WireProtocol } from './types';

const fullDate = new Intl.DateTimeFormat('zh-CN', {
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hour12: false,
});

const shortDate = new Intl.DateTimeFormat('zh-CN', {
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  hour12: false,
});

export function formatDateTime(value: number | string | null | undefined, compact = false): string {
  if (value === null || value === undefined || value === '') return '尚无记录';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '时间无效';
  if (date.getUTCFullYear() <= 1) return '尚无记录';
  return (compact ? shortDate : fullDate).format(date);
}

export function formatRelativeTime(value: number | null | undefined): string {
  if (!value) return '尚无记录';
  const delta = value - Date.now();
  const absolute = Math.abs(delta);
  if (absolute < 60_000) return delta > 0 ? '即将' : '刚刚';
  if (absolute < 3_600_000) return `${Math.round(absolute / 60_000)} 分钟${delta > 0 ? '后' : '前'}`;
  if (absolute < 86_400_000) return `${Math.round(absolute / 3_600_000)} 小时${delta > 0 ? '后' : '前'}`;
  return `${Math.round(absolute / 86_400_000)} 天${delta > 0 ? '后' : '前'}`;
}

export function formatUSDFromNano(value: number | null | undefined): string {
  if (value === null || value === undefined) return '不限额';
  const usd = value / 1_000_000_000;
  return usd.toLocaleString('en-US', {
    style: 'currency',
    currency: 'USD',
    minimumFractionDigits: usd < 1 ? 4 : 2,
    maximumFractionDigits: usd < 1 ? 6 : 2,
  });
}

export function nanoUSDFromInput(value: string): number | null {
  const normalized = value.trim();
  if (!normalized) return null;
  const parsed = Number(normalized);
  if (!Number.isFinite(parsed) || parsed < 0) return null;
  return Math.round(parsed * 1_000_000_000);
}

export function formatTokens(value: number | null | undefined): string {
  if (value === null || value === undefined) return '-';
  return value.toLocaleString('en-US');
}

export function formatLatency(value: number | null | undefined): string {
  if (value === null || value === undefined) return '-';
  return `${(value / 1_000).toFixed(value < 10_000 ? 2 : 1)} s`;
}

export function protocolLabel(protocol: WireProtocol): string {
  return { openai: 'OpenAI', anthropic: 'Anthropic', gemini: 'Gemini' }[protocol];
}

export function surfaceLabel(surface: InferenceSurface): string {
  const labels: Record<InferenceSurface, string> = {
    'openai.chat_completions': 'Chat Completions',
    'openai.responses': 'Responses',
    'anthropic.messages': 'Messages',
    'gemini.generate_content': 'GenerateContent',
  };
  return labels[surface];
}

export function authSchemeLabel(scheme: AuthScheme): string {
  return {
    bearer: 'Bearer',
    'x-api-key': 'x-api-key',
    'x-goog-api-key': 'x-goog-api-key',
    'query-key': 'Query key',
  }[scheme];
}

export function percent(value: number | null | undefined): string {
  if (value === null || value === undefined) return '-';
  const normalized = value <= 1 ? value * 100 : value;
  return `${normalized.toFixed(normalized >= 99 ? 1 : 0)}%`;
}
