export function formatUsd(value: number, maximumFractionDigits = 4): string {
  return new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
    minimumFractionDigits: value > 0 && value < 0.01 ? 4 : 2,
    maximumFractionDigits,
  }).format(value);
}

export function formatNumber(value: number): string {
  return new Intl.NumberFormat('zh-CN').format(value);
}

export function formatRelativeTime(value: string | null): string {
  if (!value) return '从未';
  const delta = new Date(value).getTime() - Date.now();
  const absolute = Math.abs(delta);
  const formatter = new Intl.RelativeTimeFormat('zh-CN', { numeric: 'auto' });
  if (absolute < 60_000) return formatter.format(Math.round(delta / 1_000), 'second');
  if (absolute < 3_600_000) return formatter.format(Math.round(delta / 60_000), 'minute');
  if (absolute < 86_400_000) return formatter.format(Math.round(delta / 3_600_000), 'hour');
  return formatter.format(Math.round(delta / 86_400_000), 'day');
}

export function formatDateTime(value: string | null): string {
  if (!value) return '-';
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).format(new Date(value));
}

export function formatLatency(value: number | null): string {
  return value == null ? '-' : `${Math.round(value)} ms`;
}

export function formatTokens(value: number): string {
  if (value < 1_000) return String(value);
  if (value < 1_000_000) return `${(value / 1_000).toFixed(value < 10_000 ? 1 : 0)}K`;
  return `${(value / 1_000_000).toFixed(1)}M`;
}
