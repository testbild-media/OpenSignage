// Fast three-state product status switch (AVAILABLE / SOLD_OUT / UNAVAILABLE). The three
// states are never reduced to yes/no. Each product is locked while its request runs, so a
// double click or double tap cannot send twice.
import { useCallback, useRef, useState } from 'react';
import { Loader2 } from 'lucide-react';
import type { ProductStatus } from '@opensignage/contracts';
import { errorText, post } from '../../lib/api';
import { useT } from '../../lib/hooks';
import { useToast } from '../../components/feedback';
import { STATUSES, type StatusResult } from './types';

/** canHide: only administrators set or lift "not available" (= the product is not carried);
 * for everyone else that button is shown but locked. */
export function StatusToggle({
  value,
  busy,
  onChange,
  label,
  canHide = true,
  readOnly = false,
}: {
  value: ProductStatus;
  busy: boolean;
  onChange: (s: ProductStatus) => void;
  label: string;
  canHide?: boolean;
  /** Shows the status only (operators limited to zones without the products right). */
  readOnly?: boolean;
}) {
  const t = useT();
  const locked = readOnly || (!canHide && value === 'UNAVAILABLE');
  return (
    <div className="status-toggle" role="group" aria-label={label} aria-busy={busy || undefined} title={!canHide ? t('admin.products.status.adminOnly') : undefined}>
      {STATUSES.map((s) => (
        <button
          key={s}
          type="button"
          className={`st st-${s.toLowerCase()} ${value === s ? 'active' : ''}`}
          aria-pressed={value === s}
          disabled={busy || locked || (!canHide && s === 'UNAVAILABLE')}
          onClick={() => {
            if (value !== s) onChange(s);
          }}
        >
          {busy && value === s && <Loader2 size={12} className="spin" />}
          {t(`admin.products.status.${s}`)}
        </button>
      ))}
    </div>
  );
}

/** What a status belongs to: a product or one of its variants. */
export type StatusKind = 'product' | 'variant';

/** Sets global status (zoneId undefined) or a zone override (status null clears it) of a
 *  product or a variant. */
export function useStatusChange(onDone: (r: StatusResult) => void) {
  const toast = useToast();
  const lock = useRef(new Set<string>());
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set());
  const done = useRef(onDone);
  done.current = onDone;
  const run = useCallback(
    async (id: string, status: ProductStatus | null, zoneId?: string, kind: StatusKind = 'product') => {
      if (lock.current.has(id)) return;
      lock.current.add(id);
      setBusy(new Set(lock.current));
      try {
        const res = await post<StatusResult>(`/api/v1/live/${kind === 'variant' ? 'variants' : 'products'}/${encodeURIComponent(id)}/status`, zoneId ? { status, zoneId } : { status });
        done.current(res);
      } catch (e) {
        toast(errorText(e), 'error');
      } finally {
        lock.current.delete(id);
        setBusy(new Set(lock.current));
      }
    },
    [toast],
  );
  return { run, busy };
}
