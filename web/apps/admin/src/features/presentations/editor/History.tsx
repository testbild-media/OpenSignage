// Version history of a presentation: editing phases (publishes in quick succession), named
// versions kept for good, and restoring an earlier published version into the draft.
import { Fragment, useMemo, useState } from 'react';
import { Bookmark, ChevronDown, ChevronRight } from 'lucide-react';
import type { Presentation } from '@opensignage/contracts';
import { getLanguage } from '@opensignage/i18n';
import { post, put } from '../../../lib/api';
import { useAction, useQuery, useT } from '../../../lib/hooks';
import { useConfirm } from '../../../components/feedback';
import { Badge, Button, Checkbox, DataState, Dialog, ErrorText, IconButton, TextInput } from '../../../components/ui';
import { groupPhases, phaseTime, type HistoryRules, type Phase, type Version } from './versions';

export interface RestoredDetail {
  draft: Presentation;
  draftRevision: number;
  publishedVersion: number | null;
  hasChanges: boolean;
  warnings?: string[];
}

export function HistoryDialog({ id, readOnly, onRestored, onClose }: { id: string; readOnly?: boolean; onRestored: (d: RestoredDetail) => void; onClose: () => void }) {
  const t = useT();
  const confirm = useConfirm();
  const q = useQuery<{ versions: Version[]; publishedVersion: number | null; rules: HistoryRules }>(`/api/v1/presentations/${id}/versions`);
  const [namedOnly, setNamedOnly] = useState(false);
  const [open, setOpen] = useState<ReadonlySet<number>>(new Set());
  const [naming, setNaming] = useState<Version | null>(null);
  const restore = useAction(async (v: Version) => {
    const ok = await confirm({ title: t('editor.history.restore'), message: t('editor.history.restoreConfirm', { v: v.version }), confirmLabel: t('editor.history.restore') });
    if (!ok) return;
    onRestored(await post<RestoredDetail>(`/api/v1/presentations/${id}/versions/${v.version}/restore`));
  });
  const fmt = useMemo(() => new Intl.DateTimeFormat(getLanguage(), { dateStyle: 'medium', timeStyle: 'short' }), []);
  const versions = useMemo(() => q.data?.versions ?? [], [q.data]);
  const phases = useMemo(() => groupPhases(versions), [versions]);
  const named = versions.filter((v) => v.label);
  const rules = q.data?.rules;
  const toggle = (p: number): void =>
    setOpen((s) => {
      const n = new Set(s);
      if (!n.delete(p)) n.add(p);
      return n;
    });

  const row = (v: Version, opts: { phase?: Phase; child?: boolean; time: string }) => {
    const p = opts.phase;
    const many = p !== undefined && p.versions.length > 1;
    const inner = p ? p.versions.filter((x) => x.label && x !== v) : [];
    return (
      <tr key={v.version} className={opts.child ? 'history-child' : undefined}>
        <td>
          <div className="history-version">
            {many ? (
              <IconButton label={t(open.has(p.id) ? 'editor.history.collapse' : 'editor.history.expand')} aria-expanded={open.has(p.id)} onClick={() => toggle(p.id)}>
                {open.has(p.id) ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
              </IconButton>
            ) : (
              !opts.child && <span className="history-spacer" />
            )}
            <div>
              {v.label && (
                <div className="history-label">
                  <Bookmark size={14} /> {v.label}
                </div>
              )}
              <div>
                {v.version} {v.version === q.data?.publishedVersion && <Badge tone="primary">{t('editor.history.current')}</Badge>}
              </div>
              {many && <div className="faint">{t('editor.history.phase', { count: p.versions.length })}</div>}
              {inner.length > 0 && (
                <div className="history-inner">
                  {inner.map((x) => (
                    <span key={x.version} className="history-label faint">
                      <Bookmark size={12} /> {x.label}
                    </span>
                  ))}
                </div>
              )}
            </div>
          </div>
        </td>
        <td>{opts.time}</td>
        <td>{v.publishedBy || '—'}</td>
        <td>{v.slideCount}</td>
        <td className="row-actions">
          {!readOnly && (
            <>
              <IconButton label={t(v.label ? 'editor.history.rename' : 'editor.history.name')} onClick={() => setNaming(v)}>
                <Bookmark size={16} fill={v.label ? 'currentColor' : 'none'} />
              </IconButton>
              <Button busy={restore.busy} onClick={() => void restore.run(v)}>
                {t('editor.history.restore')}
              </Button>
            </>
          )}
        </td>
      </tr>
    );
  };

  return (
    <Dialog title={t('editor.history.title')} onClose={onClose} wide>
      <DataState loading={q.loading && !q.data} error={q.error} onRetry={q.reload} empty={versions.length === 0} emptyText={t('editor.history.empty')}>
        {rules && <p className="muted history-rules">{t('editor.history.rules', { gap: rules.phaseGapMinutes, days: rules.thinAfterDays })}</p>}
        <div className="history-filter">
          <Checkbox label={t('editor.history.namedOnly', { count: named.length })} checked={namedOnly} onChange={setNamedOnly} />
        </div>
        {namedOnly && named.length === 0 ? (
          <p className="muted">{t('editor.history.noNamed')}</p>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>{t('devices.version')}</th>
                <th>{t('editor.history.publishedAt')}</th>
                <th>{t('editor.history.by')}</th>
                <th>{t('editor.history.slides')}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {namedOnly
                ? named.map((v) => row(v, { time: fmt.format(new Date(v.publishedAt)) }))
                : phases.map((p) => (
                    <Fragment key={p.id}>
                      {row(p.versions[0], { phase: p, time: phaseTime(p, fmt) })}
                      {open.has(p.id) && p.versions.slice(1).map((v) => row(v, { child: true, time: fmt.format(new Date(v.publishedAt)) }))}
                    </Fragment>
                  ))}
            </tbody>
          </table>
        )}
      </DataState>
      <ErrorText error={restore.error} />
      {naming && (
        <NameDialog
          id={id}
          v={naming}
          max={rules?.maxLabel ?? 80}
          onClose={() => setNaming(null)}
          onDone={() => {
            setNaming(null);
            q.reload();
          }}
        />
      )}
    </Dialog>
  );
}

/** Names a version (or removes its name). Named versions are never thinned out. */
function NameDialog({ id, v, max, onClose, onDone }: { id: string; v: Version; max: number; onClose: () => void; onDone: () => void }) {
  const t = useT();
  const [label, setLabel] = useState(v.label);
  const save = useAction(async (value: string) => {
    await put(`/api/v1/presentations/${id}/versions/${v.version}/label`, { label: value });
    onDone();
  });
  const clean = label.trim();
  return (
    <Dialog
      title={t('editor.history.nameTitle', { v: v.version })}
      onClose={onClose}
      footer={
        <>
          {v.label && (
            <Button variant="ghost" busy={save.busy} onClick={() => void save.run('')}>
              {t('editor.history.removeName')}
            </Button>
          )}
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button variant="primary" busy={save.busy} disabled={!clean || clean === v.label} onClick={() => void save.run(clean)}>
            {t('common.action.save')}
          </Button>
        </>
      }
    >
      <TextInput
        label={t('editor.history.nameLabel')}
        hint={t('editor.history.nameHint')}
        placeholder={t('editor.history.namePlaceholder')}
        value={label}
        maxLength={max}
        onChange={(e) => setLabel(e.target.value)}
      />
      <ErrorText error={save.error} />
    </Dialog>
  );
}
