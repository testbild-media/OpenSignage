// Version history of a presentation: restore an earlier published version into the draft.
import type { Presentation } from '@opensignage/contracts';
import { post } from '../../../lib/api';
import { useAction, useQuery, useT } from '../../../lib/hooks';
import { useConfirm } from '../../../components/feedback';
import { Badge, Button, DataState, Dialog, ErrorText } from '../../../components/ui';

interface Version {
  version: number;
  publishedAt: string;
  publishedBy: string;
  slideCount: number;
}

export interface RestoredDetail {
  draft: Presentation;
  draftRevision: number;
  publishedVersion: number | null;
  hasChanges: boolean;
  warnings?: string[];
}

export function HistoryDialog({ id, onRestored, onClose }: { id: string; onRestored: (d: RestoredDetail) => void; onClose: () => void }) {
  const t = useT();
  const confirm = useConfirm();
  const q = useQuery<{ versions: Version[]; publishedVersion: number | null }>(`/api/v1/presentations/${id}/versions`);
  const restore = useAction(async (v: Version) => {
    const ok = await confirm({ title: t('editor.history.restore'), message: t('editor.history.restoreConfirm', { v: v.version }), confirmLabel: t('editor.history.restore') });
    if (!ok) return;
    onRestored(await post<RestoredDetail>(`/api/v1/presentations/${id}/versions/${v.version}/restore`));
  });
  return (
    <Dialog title={t('editor.history.title')} onClose={onClose} wide>
      <DataState loading={q.loading && !q.data} error={q.error} onRetry={q.reload} empty={q.data?.versions.length === 0} emptyText={t('editor.history.empty')}>
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
            {q.data?.versions.map((v) => (
              <tr key={v.version}>
                <td>
                  {v.version} {v.version === q.data?.publishedVersion && <Badge tone="primary">{t('editor.history.current')}</Badge>}
                </td>
                <td>{new Date(v.publishedAt).toLocaleString()}</td>
                <td>{v.publishedBy || '—'}</td>
                <td>{v.slideCount}</td>
                <td className="row-actions">
                  <Button busy={restore.busy} onClick={() => void restore.run(v)}>
                    {t('editor.history.restore')}
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </DataState>
      <ErrorText error={restore.error} />
    </Dialog>
  );
}
