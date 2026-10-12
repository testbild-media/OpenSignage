// Unused media: files nothing uses and old revisions, filtered by
// age and kind. Every cleanup shows a preview (what, how much) and asks for confirmation.
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router';
import { AlertTriangle, Eraser, History, Trash2 } from 'lucide-react';
import { post } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useConfirm, useToast } from '../../components/feedback';
import { Button, DataState, Dialog, PageHeader, Select, Tabs } from '../../components/ui';
import { AssetThumb, KindIcon } from '../../components/MediaPicker';
import {
  type CleanupPlan,
  folderRoute,
  formatBytes,
  formatDate,
  type MediaAsset,
  mediaErrorText,
} from './model';
import { useMediaReload } from './TrashPage';
import { UploadPanel } from './UploadPanel';

interface UnusedAsset extends MediaAsset {
  variantBytes: number;
  variantCount: number;
}

interface OldRevision {
  assetId: string;
  name: string;
  revision: number;
  currentRevision: number;
  size: number;
  createdAt: string;
}

interface UnusedResponse {
  assets: UnusedAsset[];
  oldRevisions: OldRevision[];
}

interface RevisionGroup {
  assetId: string;
  name: string;
  revisions: number[];
  current: number;
  size: number;
  oldest: string;
}

type PlanState = { kind: 'variants' | 'revisions'; ids: string[]; plan: CleanupPlan };

function PlanDialog({
  state,
  onConfirm,
  onClose,
}: {
  state: PlanState;
  onConfirm: () => Promise<void>;
  onClose: () => void;
}) {
  const t = useT();
  const go = useAction(onConfirm);
  const { plan, kind } = state;
  const nothing = plan.assets === 0;
  return (
    <Dialog
      title={kind === 'variants' ? t('media.unused.variantsTitle') : t('media.unused.revisionsTitle')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button variant="danger" busy={go.busy} disabled={nothing} onClick={() => void go.run()}>
            {kind === 'variants' ? t('media.unused.deleteVariants') : t('media.unused.deleteRevisions')}
          </Button>
        </>
      }
    >
      <div className="mm-plan">
        {nothing ? (
          <div className="muted">{t('media.unused.nothing')}</div>
        ) : (
          <>
            <div className="mm-plan-figure">
              {kind === 'variants'
                ? t('media.unused.variantsSummary', { items: plan.items, files: plan.assets })
                : t('media.unused.revisionsSummary', { items: plan.items, files: plan.assets })}
            </div>
            <div className="muted">{t('media.purge.bytes', { size: formatBytes(plan.bytes) })}</div>
            <div className="faint">
              {kind === 'variants' ? t('media.unused.variantsHint') : t('media.unused.revisionsHint')}
            </div>
          </>
        )}
        {plan.blocked.length > 0 && (
          <div className="mm-banner warning">
            <AlertTriangle size={16} />
            <div className="mm-banner-body">
              {t('media.unused.skipped', { count: plan.blocked.length })}
              <ul>
                {plan.blocked.slice(0, 10).map((b) => (
                  <li key={b.id}>{b.name || b.id}</li>
                ))}
              </ul>
            </div>
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

function Check({ checked, onChange, label }: { checked: boolean; onChange: () => void; label: string }) {
  return <input type="checkbox" checked={checked} onChange={onChange} aria-label={label} />;
}

function toggled(s: Set<string>, id: string): Set<string> {
  const n = new Set(s);
  if (n.has(id)) n.delete(id);
  else n.add(id);
  return n;
}

export function UnusedPage() {
  const t = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const navigate = useNavigate();
  const [tab, setTab] = useState<'files' | 'revisions'>('files');
  const [days, setDays] = useState('');
  const [kind, setKind] = useState('');
  const params = new URLSearchParams();
  if (days) params.set('olderThanDays', days);
  if (kind) params.set('kind', kind);
  const q = useQuery<UnusedResponse>(`/api/v1/media/unused?${params.toString()}`);
  useMediaReload(q.reload);
  const assets = useMemo(() => q.data?.assets ?? [], [q.data]);
  const groups = useMemo<RevisionGroup[]>(() => {
    const m = new Map<string, RevisionGroup>();
    for (const r of q.data?.oldRevisions ?? []) {
      const g = m.get(r.assetId) ?? {
        assetId: r.assetId,
        name: r.name,
        revisions: [],
        current: r.currentRevision,
        size: 0,
        oldest: r.createdAt,
      };
      g.revisions.push(r.revision);
      g.size += r.size;
      if (r.createdAt < g.oldest) g.oldest = r.createdAt;
      m.set(r.assetId, g);
    }
    return [...m.values()]
      .map((g) => ({ ...g, revisions: g.revisions.sort((a, b) => a - b) }))
      .sort((a, b) => b.size - a.size);
  }, [q.data]);

  const [sel, setSel] = useState<Set<string>>(new Set());
  const [selRev, setSelRev] = useState<Set<string>>(new Set());
  useEffect(() => {
    const ids = new Set(assets.map((a) => a.id));
    setSel((s) => new Set([...s].filter((x) => ids.has(x))));
  }, [assets]);
  useEffect(() => {
    const ids = new Set(groups.map((g) => g.assetId));
    setSelRev((s) => new Set([...s].filter((x) => ids.has(x))));
  }, [groups]);

  const [plan, setPlan] = useState<PlanState | null>(null);
  const closePlan = useCallback(() => setPlan(null), []);
  const selectedAssets = assets.filter((a) => sel.has(a.id));

  const trash = useAction(async () => {
    const ids = [...sel];
    const size = selectedAssets.reduce((s, a) => s + a.size, 0);
    const ok = await confirm({
      title: t('media.trash.titleMany', { count: ids.length }),
      message: t('media.unused.trashConfirm', { count: ids.length, size: formatBytes(size) }),
      confirmLabel: t('media.trash.action'),
      danger: true,
    });
    if (!ok) return;
    const r = await post<{ trashed: number }>('/api/v1/media/trash', { ids });
    toast(t('media.trash.doneMany', { count: r.trashed }));
    setSel(new Set());
    q.reload();
  });
  const preview = useAction(async (k: 'variants' | 'revisions', ids: string[]) => {
    const url =
      k === 'variants'
        ? '/api/v1/media/unused/variants/delete?preview=1'
        : '/api/v1/media/old-revisions/delete?preview=1';
    setPlan({ kind: k, ids, plan: await post<CleanupPlan>(url, { ids }) });
  });
  const execute = async (): Promise<void> => {
    if (!plan) return;
    const url =
      plan.kind === 'variants'
        ? '/api/v1/media/unused/variants/delete'
        : '/api/v1/media/old-revisions/delete';
    const r = await post<CleanupPlan>(url, { ids: plan.ids });
    toast(
      plan.kind === 'variants'
        ? t('media.unused.variantsDone', { items: r.items, files: r.assets })
        : t('media.unused.revisionsDone', { items: r.items, files: r.assets }),
    );
    if (plan.kind === 'variants') setSel(new Set());
    else setSelRev(new Set());
    setPlan(null);
    q.reload();
  };
  const pending = trash.busy || preview.busy;
  const allFiles = assets.length > 0 && sel.size === assets.length;
  const allRevs = groups.length > 0 && selRev.size === groups.length;

  return (
    <div className="page">
      <PageHeader title={t('media.unused.title')} subtitle={t('media.unused.subtitle')} />
      <div className="filters">
        <Select
          label={t('media.unused.olderThan')}
          value={days}
          onChange={(e) => setDays(e.target.value)}
          options={[
            { value: '', label: t('media.unused.anyAge') },
            ...[7, 30, 90, 180, 365].map((d) => ({
              value: String(d),
              label: t('media.unused.days', { days: d }),
            })),
          ]}
        />
        <Select
          label={t('media.kind.label')}
          value={kind}
          onChange={(e) => setKind(e.target.value)}
          options={(['', 'image', 'video', 'audio'] as const).map((k) => ({
            value: k,
            label: t(`media.kind.plural.${k || 'all'}`),
          }))}
        />
      </div>
      <Tabs
        value={tab}
        onChange={setTab}
        tabs={[
          { value: 'files', label: t('media.unused.tabFiles', { count: assets.length }) },
          { value: 'revisions', label: t('media.unused.tabRevisions', { count: groups.length }) },
        ]}
      />
      {tab === 'files' && sel.size > 0 && (
        <div className="mm-selbar">
          <span className="mm-selbar-count">
            {t('media.selected', {
              count: sel.size,
              size: formatBytes(selectedAssets.reduce((s, a) => s + a.size, 0)),
            })}
          </span>
          <Button
            className="btn-sm"
            icon={<Eraser size={14} />}
            busy={preview.busy}
            disabled={pending}
            onClick={() => void preview.run('variants', [...sel])}
          >
            {t('media.unused.deleteVariants')}
          </Button>
          <Button
            className="btn-sm"
            variant="danger"
            icon={<Trash2 size={14} />}
            busy={trash.busy}
            disabled={pending}
            onClick={() => void trash.run()}
          >
            {t('media.trash.action')}
          </Button>
          <Button className="btn-sm" variant="ghost" onClick={() => setSel(new Set())}>
            {t('media.clearSelection')}
          </Button>
        </div>
      )}
      {tab === 'revisions' && selRev.size > 0 && (
        <div className="mm-selbar">
          <span className="mm-selbar-count">{t('media.unused.selectedFiles', { count: selRev.size })}</span>
          <Button
            className="btn-sm"
            variant="danger"
            icon={<History size={14} />}
            busy={preview.busy}
            disabled={pending}
            onClick={() => void preview.run('revisions', [...selRev])}
          >
            {t('media.unused.deleteRevisions')}
          </Button>
          <Button className="btn-sm" variant="ghost" onClick={() => setSelRev(new Set())}>
            {t('media.clearSelection')}
          </Button>
        </div>
      )}
      {!!(trash.error ?? preview.error) && (
        <div className="form-error" role="alert" style={{ marginBottom: 12 }}>
          {mediaErrorText(trash.error ?? preview.error)}
        </div>
      )}
      <DataState
        loading={q.loading && !q.data}
        error={q.error}
        onRetry={q.reload}
        empty={!!q.data && (tab === 'files' ? assets.length === 0 : groups.length === 0)}
        emptyText={tab === 'files' ? t('media.unused.noFiles') : t('media.unused.noRevisions')}
      >
        {tab === 'files' ? (
          <div className="mm-table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th className="mm-check-cell">
                    <Check
                      checked={allFiles}
                      label={t('media.selectAll')}
                      onChange={() => setSel(allFiles ? new Set() : new Set(assets.map((a) => a.id)))}
                    />
                  </th>
                  <th className="mm-table-thumb" />
                  <th>{t('media.trash.name')}</th>
                  <th>{t('media.details.size')}</th>
                  <th>{t('media.unused.variants')}</th>
                  <th>{t('media.details.changed')}</th>
                </tr>
              </thead>
              <tbody>
                {assets.map((a) => (
                  <tr key={a.id}>
                    <td className="mm-check-cell">
                      <Check
                        checked={sel.has(a.id)}
                        label={t('media.selectOne', { name: a.name })}
                        onChange={() => setSel((s) => toggled(s, a.id))}
                      />
                    </td>
                    <td className="mm-table-thumb">
                      <AssetThumb asset={a} badge={false} />
                    </td>
                    <td>
                      <button
                        type="button"
                        className="mm-row"
                        style={{ padding: 0 }}
                        onClick={() =>
                          navigate(`${folderRoute(a.folderId)}?asset=${encodeURIComponent(a.id)}`)
                        }
                      >
                        <KindIcon kind={a.kind} size={14} />
                        <span className="mm-row-name">{a.name}</span>
                      </button>
                    </td>
                    <td className="nowrap">{formatBytes(a.size)}</td>
                    <td className="nowrap">
                      {a.variantCount ? `${a.variantCount} · ${formatBytes(a.variantBytes)}` : '—'}
                    </td>
                    <td className="nowrap">{formatDate(a.updatedAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <div className="mm-table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th className="mm-check-cell">
                    <Check
                      checked={allRevs}
                      label={t('media.selectAll')}
                      onChange={() => setSelRev(allRevs ? new Set() : new Set(groups.map((g) => g.assetId)))}
                    />
                  </th>
                  <th>{t('media.trash.name')}</th>
                  <th>{t('media.unused.oldRevisions')}</th>
                  <th>{t('media.unused.currentRevision')}</th>
                  <th>{t('media.details.size')}</th>
                  <th>{t('media.unused.oldest')}</th>
                </tr>
              </thead>
              <tbody>
                {groups.map((g) => (
                  <tr key={g.assetId}>
                    <td className="mm-check-cell">
                      <Check
                        checked={selRev.has(g.assetId)}
                        label={t('media.selectOne', { name: g.name })}
                        onChange={() => setSelRev((s) => toggled(s, g.assetId))}
                      />
                    </td>
                    <td>{g.name}</td>
                    <td>{g.revisions.join(', ')}</td>
                    <td>{g.current}</td>
                    <td className="nowrap">{formatBytes(g.size)}</td>
                    <td className="nowrap">{formatDate(g.oldest)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </DataState>
      <UploadPanel />
      {plan && <PlanDialog state={plan} onConfirm={execute} onClose={closePlan} />}
    </div>
  );
}
