// Per-file upload progress with cancel and retry (queue.ts holds the state).
import { useState } from 'react';
import { ChevronDown, ChevronUp, RotateCcw, X } from 'lucide-react';
import { useT } from '../../lib/hooks';
import { IconButton } from '../../components/ui';
import { formatBytes, formatPercent, mediaErrorText } from './model';
import {
  cancelUpload,
  clearFinished,
  isActive,
  removeUpload,
  retryUpload,
  type UploadItem,
  useUploads,
} from './queue';

function UploadRow({ item }: { item: UploadItem }) {
  const t = useT();
  const pct = item.size > 0 ? (item.received / item.size) * 100 : 0;
  const tone = item.state === 'error' ? 'error' : item.state === 'done' ? 'done' : '';
  const label =
    item.state === 'queued'
      ? t('media.upload.queued')
      : item.state === 'retrying'
        ? t('media.upload.retrying', { attempt: item.attempt })
        : item.state === 'done'
          ? item.replaceAssetId
            ? t('media.upload.replaced')
            : t('media.upload.done')
          : item.state === 'canceled'
            ? t('media.upload.canceled')
            : item.state === 'error'
              ? t('media.upload.failed')
              : formatPercent(Math.floor(pct));
  return (
    <div className="mm-upload">
      <div className="mm-upload-name" title={item.name}>
        {item.replaceAssetId ? t('media.upload.replacing', { name: item.name }) : item.name}
      </div>
      <div className="mm-upload-actions">
        {isActive(item) && (
          <IconButton label={t('media.upload.cancel')} onClick={() => cancelUpload(item.key)}>
            <X size={15} />
          </IconButton>
        )}
        {(item.state === 'error' || item.state === 'canceled') && (
          <IconButton label={t('media.upload.retry')} onClick={() => retryUpload(item.key)}>
            <RotateCcw size={15} />
          </IconButton>
        )}
        {!isActive(item) && (
          <IconButton label={t('media.upload.remove')} onClick={() => removeUpload(item.key)}>
            <X size={15} />
          </IconButton>
        )}
      </div>
      <div className="mm-upload-line">
        <div
          className={`mm-progress ${tone}`}
          role="progressbar"
          aria-label={item.name}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={Math.floor(pct)}
        >
          <div style={{ width: `${item.state === 'done' ? 100 : pct}%` }} />
        </div>
        <span className="nowrap">
          {formatBytes(item.received)} / {formatBytes(item.size)} · {label}
        </span>
      </div>
      {item.state === 'error' && <div className="mm-upload-error">{mediaErrorText(item.error)}</div>}
      {item.state === 'retrying' && !!item.error && (
        <div className="mm-upload-error">{mediaErrorText(item.error)}</div>
      )}
    </div>
  );
}

/** Upload rows; restricted to some queue keys when given (e.g. uploads started in a dialog). */
export function UploadList({ keys, inline }: { keys?: number[]; inline?: boolean }) {
  const all = useUploads();
  const list = keys ? all.filter((i) => keys.includes(i.key)) : all;
  if (!list.length) return null;
  return (
    <div className={`mm-upload-list ${inline ? 'inline' : ''}`} aria-live="polite">
      {list.map((i) => (
        <UploadRow key={i.key} item={i} />
      ))}
    </div>
  );
}

/** Floating panel on the media pages while uploads exist. */
export function UploadPanel() {
  const t = useT();
  const all = useUploads();
  const [open, setOpen] = useState(true);
  if (!all.length) return null;
  const active = all.filter(isActive);
  const failed = all.filter((i) => i.state === 'error').length;
  const sent = active.reduce((s, i) => s + i.received, 0);
  const size = active.reduce((s, i) => s + i.size, 0);
  return (
    <section className="mm-uploads" aria-label={t('media.upload.title')}>
      <div className="mm-uploads-head">
        <h2>
          {active.length
            ? t('media.upload.running', {
                count: active.length,
                percent: formatPercent(size ? Math.floor((sent / size) * 100) : 0),
              })
            : failed
              ? t('media.upload.someFailed', { count: failed })
              : t('media.upload.allDone')}
        </h2>
        {active.length < all.length && (
          <button type="button" className="btn btn-ghost btn-sm" onClick={clearFinished}>
            {t('media.upload.clear')}
          </button>
        )}
        <IconButton
          label={open ? t('media.upload.collapse') : t('media.upload.expand')}
          onClick={() => setOpen(!open)}
        >
          {open ? <ChevronDown size={16} /> : <ChevronUp size={16} />}
        </IconButton>
      </div>
      {open && (
        <div className="mm-uploads-body">
          <UploadList />
        </div>
      )}
    </section>
  );
}
