import {
  AlertCircle,
  Inbox,
  LoaderCircle,
  Search,
  ServerOff,
  X,
  type LucideIcon,
} from 'lucide-react';
import {
  useEffect,
  useId,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type HTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from 'react';
import type { MonitorHealth } from '../lib/types';

export function cn(...values: Array<string | false | null | undefined>): string {
  return values.filter(Boolean).join(' ');
}

export function submitForm(id: string): void {
  const form = document.getElementById(id);
  if (form instanceof HTMLFormElement) form.requestSubmit();
}

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger';
  size?: 'sm' | 'md';
  icon?: LucideIcon;
  busy?: boolean;
}

export function Button({
  variant = 'secondary',
  size = 'md',
  icon: Icon,
  busy = false,
  className,
  children,
  disabled,
  ...props
}: ButtonProps) {
  return (
    <button
      type="button"
      className={cn('button', `button-${variant}`, `button-${size}`, className)}
      disabled={disabled || busy}
      aria-busy={busy || undefined}
      {...props}
    >
      {busy ? <LoaderCircle className="spin" size={16} /> : Icon ? <Icon size={16} aria-hidden="true" /> : null}
      {children}
    </button>
  );
}

export function IconButton({
  label,
  size = 'md',
  className,
  children,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { label: string; size?: 'sm' | 'md' }) {
  return (
    <button type="button" className={cn('icon-button', `icon-button-${size}`, className)} aria-label={label} title={label} {...props}>
      {children}
    </button>
  );
}

export function Badge({
  children,
  tone = 'neutral',
}: {
  children: ReactNode;
  tone?: 'neutral' | 'accent' | 'success' | 'warning' | 'danger' | 'info';
}) {
  return <span className={`badge badge-${tone}`}>{children}</span>;
}

const healthLabels: Record<MonitorHealth, string> = {
  healthy: '探针通过',
  degraded: '部分异常',
  unavailable: '探针失败',
  unprobed: '未探测',
  suspect: '观察中',
  cooling: '冷却中',
  recovering: '恢复探测',
  disabled: '已停用',
  model_disabled: '模型已停用',
  no_credentials: '无可用密钥',
  unsupported: '协议不支持',
  skipped: '本次跳过',
};

export function HealthBadge({ state }: { state: MonitorHealth }) {
  const label = healthLabels[state];
  return (
    <span className={cn('health-badge', `health-${state}`)} title={`探针状态：${label}`}>
      <span aria-hidden="true" />
      {label}
    </span>
  );
}

export function PageHeader({
  title,
  description,
  actions,
  meta,
}: {
  title: string;
  description: string;
  actions?: ReactNode;
  meta?: ReactNode;
}) {
  return (
    <header className="page-header">
      <div className="page-heading">
        <div className="page-heading-line">
          <h1>{title}</h1>
          {meta}
        </div>
        <p>{description}</p>
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </header>
  );
}

export function Panel({ children, className, ...props }: HTMLAttributes<HTMLElement>) {
  return (
    <section className={cn('panel', className)} {...props}>
      {children}
    </section>
  );
}

export function SectionHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description?: string;
  actions?: ReactNode;
}) {
  return (
    <div className="section-header">
      <div>
        <h2>{title}</h2>
        {description && <p>{description}</p>}
      </div>
      {actions && <div className="section-actions">{actions}</div>}
    </div>
  );
}

export function MetricCard({
  label,
  value,
  detail,
  icon: Icon,
  tone = 'neutral',
}: {
  label: string;
  value: ReactNode;
  detail: ReactNode;
  icon: LucideIcon;
  tone?: 'neutral' | 'success' | 'warning' | 'danger' | 'info';
}) {
  return (
    <article className={cn('metric-card', `metric-${tone}`)}>
      <span className="metric-icon"><Icon size={18} aria-hidden="true" /></span>
      <span className="metric-label">{label}</span>
      <strong>{value}</strong>
      <span className="metric-detail">{detail}</span>
    </article>
  );
}

export function Field({
  label,
  hint,
  error,
  required,
  children,
}: {
  label: string;
  hint?: string;
  error?: string;
  required?: boolean;
  children: ReactNode;
}) {
  return (
    <label className="field">
      <span className="field-label">{label}{required && <span aria-hidden="true"> *</span>}</span>
      {children}
      {error ? <span className="field-error" role="alert">{error}</span> : hint ? <span className="field-hint">{hint}</span> : null}
    </label>
  );
}

export function Switch({
  checked,
  label,
  onChange,
  disabled = false,
}: {
  checked: boolean;
  label: string;
  onChange: (next: boolean) => void;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      className={cn('switch-control', checked && 'is-on')}
      onClick={() => onChange(!checked)}
      disabled={disabled}
    >
      <span className="switch-track" aria-hidden="true"><span /></span>
      <span>{label}</span>
    </button>
  );
}

export function SearchField({
  value,
  onChange,
  placeholder = '搜索',
  label = '搜索',
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  label?: string;
}) {
  return (
    <div className="search-field" role="search">
      <Search size={16} aria-hidden="true" />
      <input type="search" autoComplete="off" value={value} onChange={(event) => onChange(event.target.value)} placeholder={placeholder} aria-label={label} />
      {value && <IconButton size="sm" label="清除搜索" onClick={() => onChange('')}><X size={14} /></IconButton>}
    </div>
  );
}

export function SegmentedControl<T extends string>({
  value,
  items,
  onChange,
  label,
}: {
  value: T;
  items: Array<{ value: T; label: string; count?: number }>;
  onChange: (value: T) => void;
  label: string;
}) {
  const refs = useRef<Array<HTMLButtonElement | null>>([]);
  const move = (event: ReactKeyboardEvent<HTMLButtonElement>, index: number) => {
    let next = index;
    if (event.key === 'ArrowRight') next = (index + 1) % items.length;
    else if (event.key === 'ArrowLeft') next = (index - 1 + items.length) % items.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = items.length - 1;
    else return;
    event.preventDefault();
    onChange(items[next].value);
    refs.current[next]?.focus();
  };
  return (
    <div className="segmented" role="group" aria-label={label}>
      {items.map((item, index) => (
        <button
          ref={(element) => { refs.current[index] = element; }}
          type="button"
          className={item.value === value ? 'is-active' : ''}
          aria-pressed={item.value === value}
          onKeyDown={(event) => move(event, index)}
          onClick={() => onChange(item.value)}
          key={item.value}
        >
          {item.label}{item.count !== undefined && <span className="segmented-count">{item.count}</span>}
        </button>
      ))}
    </div>
  );
}

export function FilterBar({ children, trailing }: { children: ReactNode; trailing?: ReactNode }) {
  return <div className="filter-bar"><div className="filter-main">{children}</div>{trailing && <div className="filter-trailing">{trailing}</div>}</div>;
}

export function EmptyState({
  title,
  description,
  action,
}: {
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <div className="state-block empty-state">
      <Inbox size={24} aria-hidden="true" />
      <strong>{title}</strong>
      <p>{description}</p>
      {action}
    </div>
  );
}

export function LoadingState({ label = '正在加载' }: { label?: string }) {
  return <div className="state-block loading-state"><LoaderCircle className="spin" size={20} /><span>{label}</span></div>;
}

export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="state-block error-state">
      <AlertCircle size={22} aria-hidden="true" />
      <strong>数据没有加载成功</strong>
      <p>{message}</p>
      {onRetry && <Button size="sm" onClick={onRetry}>重新加载</Button>}
    </div>
  );
}

export function UnavailableState({
  title,
  description,
}: {
  title: string;
  description: string;
}) {
  return (
    <div className="state-block unavailable-state">
      <ServerOff size={22} aria-hidden="true" />
      <strong>{title}</strong>
      <p>{description}</p>
    </div>
  );
}

export function InlineNotice({
  children,
  tone = 'info',
}: {
  children: ReactNode;
  tone?: 'info' | 'warning' | 'danger' | 'success';
}) {
  return <div className={cn('inline-notice', `notice-${tone}`)}>{children}</div>;
}

function Overlay({
  children,
  onClose,
  className,
  labelledBy,
  describedBy,
}: {
  children: ReactNode;
  onClose: () => void;
  className: string;
  labelledBy: string;
  describedBy?: string;
}) {
  const panelRef = useRef<HTMLDivElement>(null);
  const onCloseRef = useRef(onClose);

  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  useEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const panel = panelRef.current;
    const selector = 'button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), a[href], [tabindex]:not([tabindex="-1"])';
    const preferredSelector = '[data-autofocus="true"], input:not(:disabled), select:not(:disabled), textarea:not(:disabled)';
    const frame = window.requestAnimationFrame(() => {
      const preferred = panel?.querySelector<HTMLElement>(preferredSelector);
      const fallback = panel?.querySelector<HTMLElement>(selector);
      (preferred || fallback || panel)?.focus();
    });
    const keydown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onCloseRef.current();
      }
      if (event.key !== 'Tab' || !panel) return;
      const focusable = [...panel.querySelectorAll<HTMLElement>(selector)]
        .filter((item) => !item.hidden && item.getClientRects().length > 0);
      if (!focusable.length) {
        event.preventDefault();
        panel.focus();
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    const oldOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    document.addEventListener('keydown', keydown);
    return () => {
      window.cancelAnimationFrame(frame);
      document.removeEventListener('keydown', keydown);
      document.body.style.overflow = oldOverflow;
      previous?.focus();
    };
  }, []);

  return (
    <div className="overlay" onMouseDown={(event) => event.target === event.currentTarget && onCloseRef.current()}>
      <div
        ref={panelRef}
        className={className}
        role="dialog"
        aria-modal="true"
        aria-labelledby={labelledBy}
        aria-describedby={describedBy}
        tabIndex={-1}
      >
        {children}
      </div>
    </div>
  );
}

export function Dialog({
  open,
  title,
  description,
  onClose,
  children,
  footer,
  width = 'md',
}: {
  open: boolean;
  title: string;
  description?: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  width?: 'sm' | 'md' | 'lg';
}) {
  const titleId = useId();
  const descriptionId = useId();
  if (!open) return null;
  return (
    <Overlay
      onClose={onClose}
      className={cn('dialog', `dialog-${width}`)}
      labelledBy={titleId}
      describedBy={description ? descriptionId : undefined}
    >
      <header className="dialog-header">
        <div><h2 id={titleId}>{title}</h2>{description && <p id={descriptionId}>{description}</p>}</div>
        <IconButton label="关闭" onClick={onClose}><X size={18} /></IconButton>
      </header>
      <div className="dialog-body">{children}</div>
      {footer && <footer className="dialog-footer">{footer}</footer>}
    </Overlay>
  );
}

export function Sheet({
  open,
  title,
  description,
  onClose,
  children,
  footer,
  width = 'md',
}: {
  open: boolean;
  title: string;
  description?: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  width?: 'sm' | 'md' | 'lg';
}) {
  const titleId = useId();
  const descriptionId = useId();
  if (!open) return null;
  return (
    <Overlay
      onClose={onClose}
      className={cn('sheet', `sheet-${width}`)}
      labelledBy={titleId}
      describedBy={description ? descriptionId : undefined}
    >
      <header className="sheet-header">
        <div><h2 id={titleId}>{title}</h2>{description && <p id={descriptionId}>{description}</p>}</div>
        <IconButton label="关闭" onClick={onClose}><X size={18} /></IconButton>
      </header>
      <div className="sheet-body">{children}</div>
      {footer && <footer className="sheet-footer">{footer}</footer>}
    </Overlay>
  );
}

export function Tabs<T extends string>({
  items,
  value,
  onChange,
  label,
}: {
  items: Array<{ value: T; label: string; count?: number }>;
  value: T;
  onChange: (value: T) => void;
  label: string;
}) {
  const refs = useRef<Array<HTMLButtonElement | null>>([]);
  const move = (event: ReactKeyboardEvent<HTMLButtonElement>, index: number) => {
    let next = index;
    if (event.key === 'ArrowRight') next = (index + 1) % items.length;
    else if (event.key === 'ArrowLeft') next = (index - 1 + items.length) % items.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = items.length - 1;
    else return;
    event.preventDefault();
    onChange(items[next].value);
    refs.current[next]?.focus();
  };
  return (
    <div className="tabs" role="tablist" aria-label={label}>
      {items.map((item, index) => (
        <button
          ref={(element) => { refs.current[index] = element; }}
          type="button"
          role="tab"
          aria-selected={item.value === value}
          tabIndex={item.value === value ? 0 : -1}
          className={item.value === value ? 'is-active' : ''}
          key={item.value}
          onKeyDown={(event) => move(event, index)}
          onClick={() => onChange(item.value)}
        >
          {item.label}{item.count !== undefined && <span>{item.count}</span>}
        </button>
      ))}
    </div>
  );
}

export function Disclosure({ summary, children, open = false }: { summary: ReactNode; children: ReactNode; open?: boolean }) {
  const [expanded, setExpanded] = useState(open);
  return (
    <details className="disclosure" open={expanded} onToggle={(event) => setExpanded(event.currentTarget.open)}>
      <summary>{summary}</summary>
      <div className="disclosure-body">{children}</div>
    </details>
  );
}
