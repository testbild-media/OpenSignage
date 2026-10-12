// Trash: age, size, original folder and dependencies per item; restore; permanent delete
// only after a preview of what and how much.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router';
import { AlertTriangle, ArchiveRestore, Clock, Folder, Trash2 } from 'lucide-react';
import { post } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { roleAtLeast, useSession } from '../../lib/session';
import { useToast } from '../../components/feedback';
import { Badge, Button, DataState, Dialog, IconButton, PageHeader } from '../../components/ui';
import { MediaThumb } from '../../components/MediaPicker';
import {
  formatBytes,
  formatDate,
  groupUsage,
  mediaErrorText,
  pathText,
  type PurgePlan,
  type TrashItem,
} from './model';
import { UploadPanel } from './UploadPanel';

interface TrashResponse {
  items: TrashItem[];
  totalSize: number;
  retentionDays: number;
}

/** Reloads a query on media changes (debounced); progress events are ignored. */
export function useMediaReload(reload: () => void): void {
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  useEvents(['media'], (e) => {
    if (e.kind === 'progress') return;
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(reload, 300);
  });
}

function PurgeDialog({
  plan,
  onConfirm,
  onClose,
}: {
  plan: PurgePlan;
  onConfirm: () => Promise<void>;
  onClose: () => void;
}) {
  const t = useT();
  const go = useAction(onConfirm);
  const blocked = plan.blocked.length > 0;
  return (
    <Dialog
      title={t('media.purge.title')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button
            variant="danger"
            busy={go.busy}
            disabled={blocked || plan.assets + plan.folders === 0}
            onClick={() => void go.run()}
          >
            {t('media.purge.action')}
          </Button>
        </>
      }
    >
      <div className="mm-plan">
        <div className="mm-plan-figure">
          {t('media.purge.summary', { files: plan.assets, folders: plan.folders })}
        </div>
        <div className="muted">{t('media.purge.bytes', { size: formatBytes(plan.bytes) })}</div>
        <ul>
          {plan.items.slice(0, 30).map((i) => (
            <li key={i.id}>
              {i.name}
              {i.type === 'folder' && (
                <span className="faint"> · {t('media.trash.folderFiles', { files: i.count })}</span>
              )}
            </li>
          ))}
          {plan.items.length > 30 && (
            <li className="faint">{t('media.more', { count: plan.items.length - 30 })}</li>
          )}
        </ul>
        {blocked ? (
          <div className="mm-banner danger" role="alert">
            <AlertTriangle size={16} />
            <div className="mm-banner-body">
              {t('media.purge.blocked', { count: plan.blocked.length })}
              <ul>
                {plan.blocked.slice(0, 10).map((b) => (
                  <li key={b.id}>{b.name}</li>
                ))}
              </ul>
            </div>
          </div>
        ) : (
          <div className="mm-banner warning">
            <AlertTriangle size={16} />
            <div className="mm-banner-body">{t('media.purge.final')}</div>
          </div>
        )}
        {!!go.error && (
          <div className="form-error" role="alert">
            {mediaErrorText(go.error)}
          </div>
        )}
      </div>
    </Dialog>
  );
}

export function TrashPage() {
  const t = useT();
  const toast = useToast();
  const { user } = useSession();
  const q = useQuery<TrashResponse>('/api/v1/media/trash');
  useMediaReload(q.reload);
  const items = useMemo(() => q.data?.items ?? [], [q.data]);
  const [sel, setSel] = useState<Set<string>>(new Set());
  const [plan, setPlan] = useState<{ ids: string[]; plan: PurgePlan } | null>(null);
  useEffect(() => {
    setSel((s) => {
      const ids = new Set(items.map((i) => i.id));
      const next = new Set([...s].filter((x) => ids.has(x)));
      return next.size === s.size ? s : next;
    });
  }, [items]);

  const restore = useAction(async (ids: string[]) => {
    const r = await post<{ assets: number; folders: number }>('/api/v1/media/trash/restore', { ids });
    toast(t('media.trash.restored', { files: r.assets, folders: r.folders }));
    setSel(new Set());
    q.reload();
  });
  const preview = useAction(async (ids: string[]) => {
    if (!ids.length) {
      toast(t('media.trash.nothingToPurge'), 'info');
      return;
    }
    const p = await post<PurgePlan>('/api/v1/media/trash/purge?preview=1', { ids });
    setPlan({ ids, plan: p });
  });
  const closePlan = useCallback(() => setPlan(null), []);
  const purge = async (): Promise<void> => {
    if (!plan) return;
    const r = await post<PurgePlan>('/api/v1/media/trash/purge', { ids: plan.ids });
    toast(t('media.purge.done', { files: r.assets, folders: r.folders }));
    setPlan(null);
    setSel(new Set());
    q.reload();
  };
  const toggle = (id: string): void =>
    setSel((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  const allChecked = items.length > 0 && sel.size === items.length;
  const days = q.data?.retentionDays ?? 0;
  const purgeable = items.filter((i) => !i.blocked).map((i) => i.id);
  const pending = restore.busy || preview.busy;

  return (
    <div className="page">
      <PageHeader
        title={t('media.trash.title')}
        subtitle={
          q.data
            ? t('media.trash.subtitle', { count: items.length, size: formatBytes(q.data.totalSize) })
            : undefined
        }
        actions={
          <Button
            variant="danger"
            icon={<Trash2 size={16} />}
            disabled={!items.length || pending}
            busy={preview.busy && !sel.size}
            onClick={() => void preview.run(purgeable)}
          >
            {t('media.trash.empty')}
          </Button>
        }
      />
      {q.data && (
        <div className="mm-banner">
          <Clock size={16} />
          <div className="mm-banner-body">
            {days > 0 ? t('media.trash.retention', { days }) : t('media.trash.retentionNever')}{' '}
            {roleAtLeast(user?.role, 'admin') && (
              <Link to="/settings/defaults">{t('media.trash.retentionChange')}</Link>
            )}
          </div>
        </div>
      )}
      {sel.size > 0 && (
        <div className="mm-selbar">
          <span className="mm-selbar-count">{t('media.trash.selected', { count: sel.size })}</span>
          <Button
            className="btn-sm"
            icon={<ArchiveRestore size={14} />}
            busy={restore.busy}
            disabled={pending}
            onClick={() => void restore.run([...sel])}
          >
            {t('media.trash.restore')}
          </Button>
          <Button
            className="btn-sm"
            variant="danger"
            icon={<Trash2 size={14} />}
            busy={preview.busy}
            disabled={pending}
            onClick={() => void preview.run([...sel])}
          >
            {t('media.purge.action')}
          </Button>
          <Button className="btn-sm" variant="ghost" onClick={() => setSel(new Set())}>
            {t('media.clearSelection')}
          </Button>
        </div>
      )}
      {!!(restore.error ?? preview.error) && (
        <div className="form-error" role="alert" style={{ marginBottom: 12 }}>
          {mediaErrorText(restore.error ?? preview.error)}
        </div>
      )}
      <DataState
        loading={q.loading && !q.data}
        error={q.error}
        onRetry={q.reload}
        empty={!!q.data && items.length === 0}
        emptyText={t('media.trash.isEmpty')}
      >
        <div className="mm-table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th className="mm-check-cell">
                  <input
                    type="checkbox"
                    checked={allChecked}
                    aria-label={t('media.selectAll')}
                    onChange={() => setSel(allChecked ? new Set() : new Set(items.map((i) => i.id)))}
                  />
                </th>
                <th>{t('media.trash.name')}</th>
                <th>{t('media.trash.origin')}</th>
                <th>{t('media.trash.deleted')}</th>
                <th>{t('media.details.size')}</th>
                <th>{t('media.details.usage')}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {items.map((i) => {
                const usage = groupUsage(i.usage);
                const left = days > 0 ? Math.max(0, days - i.ageDays) : null;
                return (
                  <tr key={i.id}>
                    <td className="mm-check-cell">
                      <input
                        type="checkbox"
                        checked={sel.has(i.id)}
                        onChange={() => toggle(i.id)}
                        aria-label={t('media.selectOne', { name: i.name })}
                      />
                    </td>
                    <td>
                      <div className="mm-name-cell">
                        <span className="mm-table-thumb">
                          {i.type === 'folder' ? (
                            <span className="media-thumb">
                              <span className="media-thumb-fallback mm-folder-icon">
                                <Folder size={22} />
                              </span>
                            </span>
                          ) : (
                            <MediaThumb assetId={i.id} kind={i.kind} />
                          )}
                        </span>
                        <div>
                          <div>{i.name}</div>
                          {i.type === 'folder' && (
                            <div className="faint">
                              {t('media.trash.folderContent', { files: i.itemCount, folders: i.folderCount })}
                            </div>
                          )}
                        </div>
                      </div>
                    </td>
                    <td className="muted">{pathText(i.originalPath)}</td>
                    <td>
                      <div className="nowrap">{formatDate(i.trashedAt)}</div>
                      <div className="faint">
                        {i.ageDays === 0 ? t('media.trash.today') : t('media.trash.age', { days: i.ageDays })}
                        {left !== null && ` · ${t('media.trash.left', { days: left })}`}
                      </div>
                    </td>
                    <td className="nowrap">{formatBytes(i.size)}</td>
                    <td>
                      <div className="mm-chips">
                        {i.blocked && <Badge tone="danger">{t('media.trash.blocked')}</Badge>}
                        {usage.map((u) => (
                          <Badge key={`${u.type}:${u.id}`}>
                            {t(`media.usage.${u.type}`)}: {u.name || u.id}
                          </Badge>
                        ))}
                        {!usage.length && !i.blocked && <span className="faint">—</span>}
                      </div>
                    </td>
                    <td className="row-actions">
                      <IconButton
                        label={t('media.trash.restore')}
                        disabled={pending}
                        onClick={() => void restore.run([i.id])}
                      >
                        <ArchiveRestore size={16} />
                      </IconButton>
                      <IconButton
                        label={t('media.purge.action')}
                        disabled={pending || i.blocked}
                        onClick={() => void preview.run([i.id])}
                      >
                        <Trash2 size={16} />
                      </IconButton>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </DataState>
      <UploadPanel />
      {plan && <PurgeDialog plan={plan.plan} onConfirm={purge} onClose={closePlan} />}
    </div>
  );
}
