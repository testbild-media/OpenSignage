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
}: {
  value: ProductStatus;
  busy: boolean;
  onChange: (s: ProductStatus) => void;
  label: string;
  canHide?: boolean;
}) {
  const t = useT();
  const locked = !canHide && value === 'UNAVAILABLE';
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

/** Sets global status (zoneId undefined) or a zone override (status null clears it). */
export function useStatusChange(onDone: (r: StatusResult) => void) {
  const toast = useToast();
  const lock = useRef(new Set<string>());
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set());
  const done = useRef(onDone);
  done.current = onDone;
  const run = useCallback(
    async (productId: string, status: ProductStatus | null, zoneId?: string) => {
      if (lock.current.has(productId)) return;
      lock.current.add(productId);
      setBusy(new Set(lock.current));
      try {
        const res = await post<StatusResult>(`/api/v1/live/products/${encodeURIComponent(productId)}/status`, zoneId ? { status, zoneId } : { status });
        done.current(res);
      } catch (e) {
        toast(errorText(e), 'error');
      } finally {
        lock.current.delete(productId);
        setBusy(new Set(lock.current));
      }
    },
    [toast],
  );
  return { run, busy };
}
