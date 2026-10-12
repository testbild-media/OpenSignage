// Compact property controls for the editor panels.
import { useEffect, useState, type ReactNode } from 'react';
import type { FontOption } from './env';

export function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="prop-row">
      <span>{label}</span>
      <div className="prop-ctrl">{children}</div>
    </label>
  );
}

/** Number input that commits on blur/Enter (one undo step), not on every keystroke. */
export function Num({
  value,
  onChange,
  min,
  max,
  step = 1,
  suffix,
  integer,
  disabled,
}: {
  value: number;
  onChange: (v: number) => void;
  min?: number;
  max?: number;
  step?: number;
  suffix?: string;
  /** Round to whole numbers (fields stored as integers). */
  integer?: boolean;
  /** Disabled fields show no value (nothing to edit). */
  disabled?: boolean;
}) {
  const [text, setText] = useState(String(round(value)));
  useEffect(() => setText(String(round(value))), [value]);
  const commit = (): void => {
    const n = Number(text.replace(',', '.'));
    if (!Number.isFinite(n)) {
      setText(String(round(value)));
      return;
    }
    const clamped = Math.min(max ?? Infinity, Math.max(min ?? -Infinity, integer ? Math.round(n) : n));
    if (clamped !== value) onChange(clamped);
    setText(String(round(clamped)));
  };
  return (
    <span className="ed-num">
      <input
        className="input input-sm"
        inputMode="decimal"
        value={disabled ? '' : text}
        disabled={disabled}
        step={step}
        onChange={(e) => setText(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === 'Enter') commit();
          if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
            e.preventDefault();
            const n = Number(text) + (e.key === 'ArrowUp' ? step : -step) * (e.shiftKey ? 10 : 1);
            onChange(Math.min(max ?? Infinity, Math.max(min ?? -Infinity, n)));
          }
        }}
      />
      {suffix && <em>{suffix}</em>}
    </span>
  );
}

function round(n: number): number {
  return Math.round(n * 100) / 100;
}

/** Color input with optional alpha and "none". */
export function ColorInput({
  value,
  onChange,
  allowNone,
  noneLabel,
  disabled,
}: {
  value: string | undefined;
  onChange: (v: string | undefined) => void;
  allowNone?: boolean;
  noneLabel?: string;
  disabled?: boolean;
}) {
  const hex = (value ?? '#000000').slice(0, 7);
  const alpha = value && value.length === 9 ? parseInt(value.slice(7, 9), 16) : 255;
  return (
    <span className="color-input">
      <input type="color" value={hex} onChange={(e) => onChange(e.target.value + (alpha < 255 ? alpha.toString(16).padStart(2, '0') : ''))} disabled={disabled || (allowNone && !value)} />
      <input
        type="range"
        min={0}
        max={255}
        value={value ? alpha : 255}
        disabled={disabled || !value}
        title="α"
        onChange={(e) => {
          const a = Number(e.target.value);
          onChange(hex + (a < 255 ? a.toString(16).padStart(2, '0') : ''));
        }}
      />
      {allowNone && (
        <label className="checkbox small">
          <input type="checkbox" checked={!value} disabled={disabled} onChange={(e) => onChange(e.target.checked ? undefined : '#000000')} />
          <span>{noneLabel}</span>
        </label>
      )}
    </span>
  );
}

/** Font picker: every name rendered in its own font; warns about missing characters. */
export function FontSelect({
  value,
  fonts,
  onChange,
  sampleText,
  missingLabel,
  disabled,
}: {
  value: string;
  fonts: FontOption[];
  onChange: (f: string) => void;
  sampleText?: string;
  missingLabel: string;
  disabled?: boolean;
}) {
  // The list is fixed to the viewport: the side panel clips overflow.
  const [open, setOpen] = useState<{ left: number; top?: number; bottom?: number; maxHeight: number } | null>(null);
  const toggle = (btn: HTMLElement): void => {
    if (open) {
      setOpen(null);
      return;
    }
    const r = btn.getBoundingClientRect();
    const below = window.innerHeight - r.bottom - 8;
    const above = r.top - 8;
    const left = Math.max(4, Math.min(r.left, window.innerWidth - 264));
    if (below >= 240 || below >= above) setOpen({ left, top: r.bottom + 2, maxHeight: Math.min(360, below) });
    else setOpen({ left, bottom: window.innerHeight - r.top + 2, maxHeight: Math.min(360, above) });
  };
  const needs = scriptsOf(sampleText ?? '');
  const current = fonts.find((f) => f.family === value);
  const lacks = (f: FontOption): boolean => !f.custom && needs.some((s) => !f.scripts.includes(s));
  return (
    <div className="font-select">
      <button type="button" className="input input-sm font-current" style={{ fontFamily: `"${value}"` }} disabled={disabled} onClick={(e) => toggle(e.currentTarget)}>
        {value}
        {current && lacks(current) && <span className="warn-dot" title={missingLabel} />}
      </button>
      {open && !disabled && (
        <div className="font-list" style={open} onMouseLeave={() => setOpen(null)}>
          {fonts.map((f) => (
            <button
              type="button"
              key={f.family}
              className={`font-opt ${f.family === value ? 'active' : ''}`}
              style={{ fontFamily: `"${f.family}"` }}
              onClick={() => {
                onChange(f.family);
                setOpen(null);
              }}
            >
              {f.family}
              {lacks(f) && <span className="warn-dot" title={missingLabel} />}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

/** Scripts a text needs beyond basic Latin. */
export function scriptsOf(text: string): string[] {
  const out = new Set<string>();
  for (const ch of text) {
    const c = ch.codePointAt(0) ?? 0;
    if (c >= 0x370 && c <= 0x3ff) out.add('greek');
    else if (c >= 0x400 && c <= 0x4ff) out.add('cyrillic');
    else if (c >= 0x100 && c <= 0x24f) out.add('latin-ext');
  }
  return [...out];
}
