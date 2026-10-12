// Backup & restore: downloads stream from the server; a restore is
// validated, staged and applied by a controlled restart.
import { useRef, useState } from 'react';
import { Download, RotateCcw, Upload } from 'lucide-react';
import { api, del, post } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useConfirm, useToast } from '../../components/feedback';
import { Badge, Button, DataState, ErrorText, PageHeader } from '../../components/ui';

export function BackupPage() {
  const t = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const status = useQuery<{ staged: boolean }>('/api/v1/backup/restore');
  const fileRef = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | null>(null);
  const [restarting, setRestarting] = useState(false);
  const upload = useAction(async () => {
    if (!file) return;
    const ok = await confirm({ title: t('backup.restore'), message: t('backup.restoreConfirm', { name: file.name }), confirmLabel: t('backup.restoreUpload'), danger: true });
    if (!ok) return;
    await api('/api/v1/backup/restore', { method: 'POST', body: file, headers: { 'Content-Type': 'application/octet-stream' } });
    setFile(null);
    if (fileRef.current) fileRef.current.value = '';
    toast(t('backup.staged'));
    status.reload();
  });
  const cancel = useAction(async () => {
    await del('/api/v1/backup/restore');
    status.reload();
  });
  const restart = useAction(async () => {
    const ok = await confirm({ title: t('backup.restart'), message: t('backup.restartConfirm'), confirmLabel: t('backup.restart'), danger: true });
    if (!ok) return;
    await post('/api/v1/system/restart');
    setRestarting(true);
    // Wait until the server answers again, then reload the app.
    for (let i = 0; i < 120; i++) {
      await new Promise((r) => setTimeout(r, 2000));
      try {
        const r = await fetch('/api/v1/system/info', { cache: 'no-store' });
        if (r.ok) {
          location.reload();
          return;
        }
      } catch {
        // server still restarting
      }
    }
    setRestarting(false);
  });
  return (
    <div className="page page-narrow">
      <PageHeader title={t('backup.title')} />
      <div className="card">
        <h2>{t('backup.download')}</h2>
        <p className="muted">{t('backup.downloadText')}</p>
        <div className="action-grid">
          <a className="btn btn-primary" href="/api/v1/backup/full" download>
            <Download size={16} /> {t('backup.full')}
          </a>
          <a className="btn btn-secondary" href="/api/v1/backup/database" download>
            <Download size={16} /> {t('backup.database')}
          </a>
          <a className="btn btn-secondary" href="/api/v1/backup/media.zip" download>
            <Download size={16} /> {t('backup.media')}
          </a>
        </div>
      </div>
      <div className="card form">
        <h2>{t('backup.restore')}</h2>
        <p className="muted">{t('backup.restoreText')}</p>
        <DataState loading={status.loading && !status.data} error={status.error} onRetry={status.reload}>
          {status.data?.staged ? (
            <>
              <div>
                <Badge tone="warning">{t('backup.pending')}</Badge>
              </div>
              <p className="muted">{t('backup.pendingText')}</p>
              <div className="form-actions">
                <Button busy={cancel.busy} onClick={() => void cancel.run()}>
                  {t('backup.cancel')}
                </Button>
                <Button variant="danger" icon={<RotateCcw size={16} />} busy={restart.busy || restarting} onClick={() => void restart.run()}>
                  {t('backup.restart')}
                </Button>
              </div>
              {restarting && <p className="muted">{t('backup.restarting')}</p>}
            </>
          ) : (
            <>
              <input ref={fileRef} type="file" accept=".db,.tar" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
              <div className="form-actions">
                <Button variant="primary" icon={<Upload size={16} />} disabled={!file} busy={upload.busy} onClick={() => void upload.run()}>
                  {t('backup.restoreUpload')}
                </Button>
              </div>
            </>
          )}
        </DataState>
        <ErrorText error={upload.error ?? cancel.error ?? restart.error} />
      </div>
    </div>
  );
}
