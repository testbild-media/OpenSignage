// Small UI kit in the OpenSignage dark design. All interactive pieces are keyboard and
// touch friendly; destructive actions go through ConfirmDialog (never window.confirm).
import {
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  type SelectHTMLAttributes,
  useEffect,
  useId,
  useRef,
} from 'react';
import { Loader2, X } from 'lucide-react';
import { useT } from '../lib/hooks';
import { errorText } from '../lib/api';

type Variant = 'primary' | 'secondary' | 'danger' | 'ghost';

export function Button({
  variant = 'secondary',
  busy,
  icon,
  children,
  className,
  disabled,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; busy?: boolean; icon?: ReactNode }) {
  return (
    <button
      type="button"
      className={`btn btn-${variant} ${className ?? ''}`}
      disabled={disabled || busy}
      aria-busy={busy || undefined}
      {...rest}
    >
      {busy ? <Loader2 className="spin" size={16} /> : icon}
      {children && <span>{children}</span>}
    </button>
  );
}

export function IconButton({
  label,
  children,
  className,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return (
    <button type="button" className={`icon-btn ${className ?? ''}`} title={label} aria-label={label} {...rest}>
      {children}
    </button>
  );
}

export function Field({ label, hint, error, children, htmlFor }: { label: string; hint?: string; error?: string | null; children: ReactNode; htmlFor?: string }) {
  return (
    <div className={`field ${error ? 'has-error' : ''}`}>
      <label htmlFor={htmlFor}>{label}</label>
      {children}
      {hint && !error && <div className="field-hint">{hint}</div>}
      {error && <div className="field-error">{error}</div>}
    </div>
  );
}

export function TextInput({ label, hint, error, ...rest }: InputHTMLAttributes<HTMLInputElement> & { label: string; hint?: string; error?: string | null }) {
  const id = useId();
  return (
    <Field label={label} hint={hint} error={error} htmlFor={id}>
      <input id={id} className="input" {...rest} />
    </Field>
  );
}

export function Select({
  label,
  hint,
  options,
  ...rest
}: SelectHTMLAttributes<HTMLSelectElement> & { label: string; hint?: string; options: { value: string; label: string }[] }) {
  const id = useId();
  return (
    <Field label={label} hint={hint} htmlFor={id}>
      <select id={id} className="input" {...rest}>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </Field>
  );
}

export function Checkbox({ label, checked, onChange, disabled }: { label: string; checked: boolean; onChange: (v: boolean) => void; disabled?: boolean }) {
  return (
    <label className="checkbox">
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span>{label}</span>
    </label>
  );
}

/** Password with confirmation, both visible immediately. */
export function PasswordPair({
  value,
  confirm,
  onChange,
  onConfirmChange,
  label,
  autoComplete = 'new-password',
}: {
  value: string;
  confirm: string;
  onChange: (v: string) => void;
  onConfirmChange: (v: string) => void;
  label?: string;
  autoComplete?: string;
}) {
  const t = useT();
  const mismatch = confirm !== '' && value !== confirm;
  const short = value !== '' && [...value].length < 8;
  return (
    <>
      <TextInput
        type="password"
        label={label ?? t('admin.password.new')}
        value={value}
        autoComplete={autoComplete}
        onChange={(e) => onChange(e.target.value)}
        hint={t('admin.password.rule', { min: 8 })}
        error={short ? t('admin.password.tooShort', { min: 8 }) : null}
      />
      <TextInput
        type="password"
        label={t('admin.password.repeat')}
        value={confirm}
        autoComplete={autoComplete}
        onChange={(e) => onConfirmChange(e.target.value)}
        error={mismatch ? t('admin.password.mismatch') : null}
      />
    </>
  );
}

export function passwordPairValid(a: string, b: string): boolean {
  return [...a].length >= 8 && a === b;
}

export function Spinner({ label }: { label?: string }) {
  return (
    <div className="spinner-row" role="status">
      <Loader2 className="spin" size={20} />
      {label && <span>{label}</span>}
    </div>
  );
}

/** Loading / error / empty states for every data view (no blank pages). */
export function DataState({
  loading,
  error,
  empty,
  emptyText,
  onRetry,
  children,
}: {
  loading: boolean;
  error: unknown;
  empty?: boolean;
  emptyText?: string;
  onRetry?: () => void;
  children: ReactNode;
}) {
  const t = useT();
  if (error) {
    return (
      <div className="state state-error" role="alert">
        <div>{errorText(error)}</div>
        {onRetry && (
          <Button onClick={onRetry} variant="secondary">
            {t('common.action.retry')}
          </Button>
        )}
      </div>
    );
  }
  if (loading) return <Spinner label={t('common.state.loading')} />;
  if (empty) return <div className="state state-empty">{emptyText ?? t('admin.empty')}</div>;
  return <>{children}</>;
}

export function ErrorText({ error }: { error: unknown }) {
  if (!error) return null;
  return (
    <div className="form-error" role="alert">
      {errorText(error)}
    </div>
  );
}

export function Dialog({
  title,
  onClose,
  children,
  footer,
  wide,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
}) {
  const t = useT();
  const ref = useRef<HTMLDivElement>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  // Runs once: a parent re-rendering with a new onClose must not steal the focus again.
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key !== 'Escape') return;
      // Stacked dialogs (confirm over details): only the topmost one closes.
      const backdrops = document.querySelectorAll('.dialog-backdrop');
      const mine = ref.current?.closest('.dialog-backdrop');
      if (mine && backdrops[backdrops.length - 1] !== mine) return;
      closeRef.current();
    };
    document.addEventListener('keydown', onKey);
    const first = ref.current?.querySelector<HTMLElement>('input, select, textarea, button.btn-primary');
    first?.focus();
    return () => document.removeEventListener('keydown', onKey);
  }, []);
  // Enter in a text field confirms the dialog with its primary button (like a form submit).
  // Deferred one tick so a field that commits its value on Enter is rendered first.
  const onKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>): void => {
    if (e.key !== 'Enter' || e.defaultPrevented || e.shiftKey || e.altKey || e.ctrlKey || e.metaKey || e.nativeEvent.isComposing) return;
    const target = e.target;
    if (!(target instanceof HTMLInputElement) || ['button', 'submit', 'reset', 'file', 'checkbox', 'radio'].includes(target.type)) return;
    const dialog = ref.current;
    if (!dialog?.querySelector('.dialog-foot .btn-primary:not(:disabled)')) return;
    e.preventDefault();
    e.stopPropagation(); // stacked dialogs: only the innermost one confirms
    setTimeout(() => dialog.querySelector<HTMLButtonElement>('.dialog-foot .btn-primary:not(:disabled)')?.click(), 0);
  };
  return (
    <div className="dialog-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={`dialog ${wide ? 'dialog-wide' : ''}`} role="dialog" aria-modal="true" aria-label={title} ref={ref} onKeyDown={onKeyDown}>
        <div className="dialog-head">
          <h2>{title}</h2>
          <IconButton label={t('common.action.close')} onClick={onClose}>
            <X size={18} />
          </IconButton>
        </div>
        <div className="dialog-body">{children}</div>
        {footer && <div className="dialog-foot">{footer}</div>}
      </div>
    </div>
  );
}

export function PageHeader({ title, subtitle, actions }: { title: string; subtitle?: string; actions?: ReactNode }) {
  return (
    <div className="page-header">
      <div>
        <h1>{title}</h1>
        {subtitle && <div className="page-subtitle">{subtitle}</div>}
      </div>
      <div className="page-actions">{actions}</div>
    </div>
  );
}

export function Badge({ tone = 'neutral', children }: { tone?: 'neutral' | 'success' | 'warning' | 'danger' | 'primary'; children: ReactNode }) {
  return <span className={`badge badge-${tone}`}>{children}</span>;
}

export function Tabs<T extends string>({ value, onChange, tabs }: { value: T; onChange: (v: T) => void; tabs: { value: T; label: string }[] }) {
  return (
    <div className="tabs" role="tablist">
      {tabs.map((tab) => (
        <button key={tab.value} role="tab" aria-selected={tab.value === value} className={tab.value === value ? 'active' : ''} onClick={() => onChange(tab.value)}>
          {tab.label}
        </button>
      ))}
    </div>
  );
}
