import { Inbox, LoaderCircle, X, type LucideIcon } from 'lucide-react';
import { useEffect, useId, useRef, type ButtonHTMLAttributes, type HTMLAttributes, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from 'react';
import type { HealthState } from '../lib/types';

function join(...values: Array<string | false | null | undefined>): string {
  return values.filter(Boolean).join(' ');
}

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger';
  size?: 'sm' | 'md';
  icon?: LucideIcon;
  busy?: boolean;
}

export function Button({ variant = 'secondary', size = 'md', icon: Icon, busy, className, children, disabled, ...props }: ButtonProps) {
  return (
    <button className={join('button', `button-${variant}`, `button-${size}`, className)} disabled={disabled || busy} {...props}>
      {busy ? <LoaderCircle className="spin" size={16} /> : Icon ? <Icon size={16} aria-hidden="true" /> : null}
      {children}
    </button>
  );
}

export function IconButton({ label, children, className, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return (
    <button className={join('icon-button', className)} aria-label={label} title={label} {...props}>
      {children}
    </button>
  );
}

const stateLabels: Record<HealthState, string> = {
  healthy: '健康',
  suspect: '观察中',
  cooldown: '冷却中',
  credential_error: '凭据异常',
  probing: '探针中',
  unknown: '未知',
  disabled: '已停用',
};

export function StatusBadge({ state, compact = false }: { state: HealthState; compact?: boolean }) {
  return (
    <span className={join('status-badge', `status-${state}`, compact && 'status-compact')}>
      <span className="status-dot" aria-hidden="true" />
      {stateLabels[state]}
    </span>
  );
}

export function Badge({ children, tone = 'neutral' }: { children: ReactNode; tone?: 'neutral' | 'accent' | 'success' | 'warning' | 'danger' | 'info' }) {
  return <span className={`badge badge-${tone}`}>{children}</span>;
}

export function PageHeader({ title, description, actions }: { title: string; description: string; actions?: ReactNode }) {
  return (
    <header className="page-header">
      <div className="page-title-block">
        <h1>{title}</h1>
        <p>{description}</p>
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </header>
  );
}

export function Surface({ children, className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <section className={join('surface', className)} {...props}>{children}</section>;
}

export function SectionHeader({ title, description, actions }: { title: string; description?: string; actions?: ReactNode }) {
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

export function Metric({ label, value, hint, tone = 'neutral' }: { label: string; value: ReactNode; hint?: string; tone?: 'neutral' | 'success' | 'warning' | 'danger' }) {
  return (
    <div className={`metric metric-${tone}`}>
      <span className="metric-label">{label}</span>
      <strong className="metric-value">{value}</strong>
      {hint && <span className="metric-hint">{hint}</span>}
    </div>
  );
}

export function Switch({ checked, onChange, label, disabled = false, showLabel = true }: { checked: boolean; onChange: (checked: boolean) => void; label: string; disabled?: boolean; showLabel?: boolean }) {
  return (
    <span className={join('switch-row', disabled && 'is-disabled')}>
      <button type="button" className={join('switch', checked && 'is-on')} role="switch" aria-label={label} aria-checked={checked} disabled={disabled} onClick={() => onChange(!checked)}>
        <span />
      </button>
      {showLabel && <span>{label}</span>}
    </span>
  );
}

export function Field({ label, hint, error, children }: { label: string; hint?: string; error?: string; children: ReactNode }) {
  return (
    <label className="field">
      <span className="field-label">{label}</span>
      {children}
      {error ? <span className="field-error">{error}</span> : hint ? <span className="field-hint">{hint}</span> : null}
    </label>
  );
}

export function EmptyState({ title, description, action }: { title: string; description: string; action?: ReactNode }) {
  return (
    <div className="empty-state">
      <Inbox size={26} aria-hidden="true" />
      <strong>{title}</strong>
      <p>{description}</p>
      {action}
    </div>
  );
}

export function LoadingState({ label = '正在加载' }: { label?: string }) {
  return <div className="loading-state"><LoaderCircle className="spin" size={20} /><span>{label}</span></div>;
}

export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="error-state">
      <strong>暂时无法加载</strong>
      <span>{message}</span>
      {onRetry && <Button size="sm" onClick={onRetry}>重试</Button>}
    </div>
  );
}

function Overlay({ children, onClose, className, labelledBy, describedBy }: { children: ReactNode; onClose: () => void; className: string; labelledBy: string; describedBy?: string }) {
  const panelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const panel = panelRef.current;
    const focusableSelector = 'button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [href], [tabindex]:not([tabindex="-1"])';
    const focusFirst = () => {
      const first = panel?.querySelector<HTMLElement>(focusableSelector);
      (first || panel)?.focus();
    };
    const frame = window.requestAnimationFrame(focusFirst);
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== 'Tab' || !panel) return;
      const focusable = [...panel.querySelectorAll<HTMLElement>(focusableSelector)].filter((element) => !element.hidden && element.getAttribute('aria-hidden') !== 'true');
      if (focusable.length === 0) {
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
    document.addEventListener('keydown', handleKeyDown);
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      window.cancelAnimationFrame(frame);
      document.removeEventListener('keydown', handleKeyDown);
      document.body.style.overflow = previousOverflow;
      previousFocus?.focus();
    };
  }, [onClose]);

  return <div className="overlay" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}><div ref={panelRef} className={className} role="dialog" aria-modal="true" aria-labelledby={labelledBy} aria-describedby={describedBy} tabIndex={-1}>{children}</div></div>;
}

export function Dialog({ open, title, description, onClose, children, footer, width = 'md' }: { open: boolean; title: string; description?: string; onClose: () => void; children: ReactNode; footer?: ReactNode; width?: 'sm' | 'md' | 'lg' }) {
  const titleId = useId();
  const descriptionId = useId();
  if (!open) return null;
  return (
    <Overlay onClose={onClose} className={`dialog dialog-${width}`} labelledBy={titleId} describedBy={description ? descriptionId : undefined}>
      <div className="overlay-header">
        <div><h2 id={titleId}>{title}</h2>{description && <p id={descriptionId}>{description}</p>}</div>
        <IconButton label="关闭" onClick={onClose}><X size={18} /></IconButton>
      </div>
      <div className="dialog-body">{children}</div>
      {footer && <div className="dialog-footer">{footer}</div>}
    </Overlay>
  );
}

export function Drawer({ open, title, description, onClose, children, footer }: { open: boolean; title: string; description?: string; onClose: () => void; children: ReactNode; footer?: ReactNode }) {
  const titleId = useId();
  const descriptionId = useId();
  if (!open) return null;
  return (
    <Overlay onClose={onClose} className="drawer" labelledBy={titleId} describedBy={description ? descriptionId : undefined}>
      <div className="overlay-header">
        <div><h2 id={titleId}>{title}</h2>{description && <p id={descriptionId}>{description}</p>}</div>
        <IconButton label="关闭" onClick={onClose}><X size={18} /></IconButton>
      </div>
      <div className="drawer-body">{children}</div>
      {footer && <div className="drawer-footer">{footer}</div>}
    </Overlay>
  );
}

export function ProgressBar({ value, tone = 'accent' }: { value: number; tone?: 'accent' | 'warning' | 'danger' }) {
  const normalized = Math.min(100, Math.max(0, value));
  return <div className={`progress progress-${tone}`}><span style={{ width: `${normalized}%` }} /></div>;
}

export function Tabs<T extends string>({ items, value, onChange, label }: { items: Array<{ value: T; label: string }>; value: T; onChange: (value: T) => void; label: string }) {
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
      {items.map((item, index) => <button ref={(element) => { refs.current[index] = element; }} type="button" role="tab" aria-selected={item.value === value} tabIndex={item.value === value ? 0 : -1} className={item.value === value ? 'is-active' : ''} key={item.value} onKeyDown={(event) => move(event, index)} onClick={() => onChange(item.value)}>{item.label}</button>)}
    </div>
  );
}
