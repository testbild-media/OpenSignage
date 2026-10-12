// Additional form controls in the OpenSignage design: segmented choice, switch, colour
// field with optional opacity, and a font picker that shows every family in its own font.
import { type ReactNode, useEffect, useId, useRef, useState } from 'react';
import { Check, ChevronDown } from 'lucide-react';
import { Field } from './ui';
import './controls.css';

export function Segmented<T extends string>({
  value,
  options,
  onChange,
  label,
  disabled,
}: {
  value: T;
  options: { value: T; label: ReactNode; title?: string }[];
  onChange: (v: T) => void;
  label?: string;
  disabled?: boolean;
}) {
  return (
    <div className="segmented" role="group" aria-label={label}>
      {options.map((o) => (
        <button key={o.value} type="button" title={o.title} disabled={disabled} className={o.value === value ? 'active' : ''} aria-pressed={o.value === value} onClick={() => onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function Switch({ checked, onChange, label, disabled, busy }: { checked: boolean; onChange: (v: boolean) => void; label: string; disabled?: boolean; busy?: boolean }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-busy={busy || undefined}
      className={`switch ${checked ? 'on' : ''}`}
      disabled={disabled || busy}
      onClick={() => onChange(!checked)}
      title={label}
    >
      <span className="switch-track">
        <span className="switch-knob" />
      </span>
      <span className="switch-label">{label}</span>
    </button>
  );
}

const HEX6 = /^#[0-9a-fA-F]{6}$/;
const HEX8 = /^#[0-9a-fA-F]{8}$/;

export function validColor(c: string, alpha = true): boolean {
  return HEX6.test(c) || (alpha && HEX8.test(c));
}

/** Splits #rrggbb[aa] into base colour and opacity 0..100. */
export function splitColor(c: string): { base: string; opacity: number } {
  if (HEX8.test(c)) return { base: c.slice(0, 7).toLowerCase(), opacity: Math.round((parseInt(c.slice(7, 9), 16) / 255) * 100) };
  if (HEX6.test(c)) return { base: c.toLowerCase(), opacity: 100 };
  return { base: '#000000', opacity: 100 };
}

export function joinColor(base: string, opacity: number): string {
  if (opacity >= 100) return base.toLowerCase();
  const a = Math.max(0, Math.min(255, Math.round((opacity / 100) * 255)));
  return `${base.toLowerCase()}${a.toString(16).padStart(2, '0')}`;
}

/** Colour with swatch, hex input and (optionally) an opacity slider. */
export function ColorField({
  label,
  value,
  onChange,
  alpha,
  opacityLabel,
  invalidText,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  alpha?: boolean;
  opacityLabel?: string;
  invalidText: string;
}) {
  const id = useId();
  const [text, setText] = useState(value);
  const [editing, setEditing] = useState(false);
  const shown = editing ? text : value;
  const { base, opacity } = splitColor(value);
  const bad = editing && !validColor(text, !!alpha);
  return (
    <Field label={label} htmlFor={id} error={bad ? invalidText : null}>
      <div className="color-field">
        <span className="color-swatch" style={{ background: value }}>
          <input type="color" aria-label={label} value={base} onChange={(e) => onChange(alpha ? joinColor(e.target.value, opacity) : e.target.value.toLowerCase())} />
        </span>
        <input
          id={id}
          className="input mono color-hex"
          value={shown}
          spellCheck={false}
          maxLength={alpha ? 9 : 7}
          onFocus={() => {
            setText(value);
            setEditing(true);
          }}
          onChange={(e) => {
            const v = e.target.value.trim();
            setText(v);
            if (validColor(v, !!alpha)) onChange(v.toLowerCase());
          }}
          onBlur={() => setEditing(false)}
        />
        {alpha && (
          <label className="color-opacity" title={opacityLabel}>
            <input type="range" min={0} max={100} step={5} value={opacity} aria-label={opacityLabel} onChange={(e) => onChange(joinColor(base, Number(e.target.value)))} />
            <span className="mono">{opacity}%</span>
          </label>
        )}
      </div>
    </Field>
  );
}

export function fontStack(family: string): string {
  return `"${family.replace(/"/g, '')}", "Noto Sans", sans-serif`;
}

/** Font picker: each family is rendered in its own font; only listed families can be chosen. */
export function FontSelect({ label, value, families, onChange, hint }: { label: string; value: string; families: string[]; onChange: (f: string) => void; hint?: string }) {
  const id = useId();
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const wrap = useRef<HTMLDivElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const options = families.includes(value) || !value ? families : [value, ...families];

  useEffect(() => {
    if (!open) return;
    const onDown = (e: PointerEvent): void => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false);
    };
    // Escape closes only the list, not the surrounding dialog.
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        setOpen(false);
      }
    };
    document.addEventListener('pointerdown', onDown);
    window.addEventListener('keydown', onKey, true);
    return () => {
      document.removeEventListener('pointerdown', onDown);
      window.removeEventListener('keydown', onKey, true);
    };
  }, [open]);

  useEffect(() => {
    if (open) list.current?.querySelector<HTMLElement>(`[data-index="${active}"]`)?.scrollIntoView({ block: 'nearest' });
  }, [open, active]);

  const choose = (f: string | undefined): void => {
    if (f === undefined) return;
    onChange(f);
    setOpen(false);
  };

  return (
    <Field label={label} hint={hint} htmlFor={id}>
      <div className="font-select" ref={wrap}>
        <button
          id={id}
          type="button"
          className="input font-select-button"
          aria-haspopup="listbox"
          aria-expanded={open}
          onClick={() => {
            setActive(Math.max(0, options.indexOf(value)));
            setOpen(!open);
          }}
          onKeyDown={(e) => {
            if (!open && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
              e.preventDefault();
              setActive(Math.max(0, options.indexOf(value)));
              setOpen(true);
              return;
            }
            if (!open) return;
            if (e.key === 'ArrowDown') {
              e.preventDefault();
              setActive((a) => Math.min(options.length - 1, a + 1));
            } else if (e.key === 'ArrowUp') {
              e.preventDefault();
              setActive((a) => Math.max(0, a - 1));
            } else if (e.key === 'Enter') {
              e.preventDefault();
              choose(options[active]);
            }
          }}
        >
          <span className="ellipsis" style={{ fontFamily: fontStack(value) }}>
            {value}
          </span>
          <ChevronDown size={16} />
        </button>
        {open && (
          <ul className="font-select-list" role="listbox" aria-label={label} ref={list}>
            {options.map((f, i) => (
              <li
                key={f}
                role="option"
                data-index={i}
                aria-selected={f === value}
                className={`${i === active ? 'active' : ''} ${f === value ? 'selected' : ''}`}
                style={{ fontFamily: fontStack(f) }}
                onPointerEnter={() => setActive(i)}
                onClick={() => choose(f)}
              >
                <span className="ellipsis">{f}</span>
                {f === value && <Check size={14} />}
              </li>
            ))}
          </ul>
        )}
      </div>
    </Field>
  );
}
