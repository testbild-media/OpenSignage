// "Copy for my zone": a presentation or playlist that also runs in zones the user may not
// change becomes an own copy for one of the user's zones (server: content/rights.go). The
// screens keep showing the same until the copy is changed and published.
import { useState } from 'react';
import { Copy } from 'lucide-react';
import { post } from '../lib/api';
import { useAction, useQuery, useT } from '../lib/hooks';
import { Button, Dialog, ErrorText, Select } from './ui';

export function CopyForZoneButton({ kind, id, zoneIds, onCopied }: { kind: 'presentations' | 'playlists'; id: string; zoneIds: string[]; onCopied: (newId: string) => void }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  if (zoneIds.length === 0) return null;
  return (
    <>
      <Button icon={<Copy size={16} />} onClick={() => setOpen(true)}>
        {t('admin.copyForZone.action')}
      </Button>
      {open && <CopyForZoneDialog kind={kind} id={id} zoneIds={zoneIds} onClose={() => setOpen(false)} onCopied={onCopied} />}
    </>
  );
}

function CopyForZoneDialog({ kind, id, zoneIds, onClose, onCopied }: { kind: 'presentations' | 'playlists'; id: string; zoneIds: string[]; onClose: () => void; onCopied: (newId: string) => void }) {
  const t = useT();
  const zones = useQuery<{ zones: { id: string; name: string }[] }>('/api/v1/zones');
  const [zone, setZone] = useState(zoneIds[0] ?? '');
  const name = (z: string): string => zones.data?.zones.find((x) => x.id === z)?.name ?? '…';
  const copy = useAction(async () => {
    const r = await post<{ id: string }>(`/api/v1/${kind}/${id}/copy-for-zone`, { zoneId: zone });
    onCopied(r.id);
  });
  return (
    <Dialog
      title={t('admin.copyForZone.title')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button variant="primary" busy={copy.busy} disabled={!zone} onClick={() => void copy.run()}>
            {t('admin.copyForZone.confirm')}
          </Button>
        </>
      }
    >
      <div className="form">
        <p className="muted">{t('admin.copyForZone.text', { zone: name(zone) })}</p>
        {zoneIds.length > 1 && <Select label={t('admin.copyForZone.zone')} value={zone} options={zoneIds.map((z) => ({ value: z, label: name(z) }))} onChange={(e) => setZone(e.target.value)} />}
        <ErrorText error={copy.error} />
      </div>
    </Dialog>
  );
}
