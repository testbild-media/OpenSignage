// Zones: list with device counts, create/rename, playlist when CLOSED, ticker template,
// delete with a warning that names the devices that become zone-less.
import { useCallback, useState } from 'react';
import { Link } from 'react-router';
import { CalendarClock, MonitorSmartphone, Pencil, Plus, Trash2 } from 'lucide-react';
import { del, get, patch, post } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { useConfirm, useToast } from '../../components/feedback';
import { Badge, Button, DataState, Dialog, ErrorText, IconButton, PageHeader, Select, TextInput } from '../../components/ui';
import { useRights } from '../../lib/rights';
import { playlistOptions, type PlaylistSummary, type TickerTemplateList, type Zone, type ZoneUsage } from './types';
import { ZoneScreensDialog } from './ZoneScreens';

function ZoneDialog({
  zone,
  playlists,
  templates,
  onClose,
  onSaved,
}: {
  zone: Zone | null;
  playlists: PlaylistSummary[] | undefined;
  templates: TickerTemplateList | undefined;
  onClose: () => void;
  onSaved: () => void;
}) {
  const t = useT();
  const toast = useToast();
  // Operators limited to zones set the closed playlist and the ticker template, not the name.
  const nameLocked = !useRights().canManageGlobal;
  const [name, setName] = useState(zone?.name ?? '');
  const [closed, setClosed] = useState(zone?.closedPlaylistId ?? '');
  const [ticker, setTicker] = useState(zone?.tickerTemplateId ?? '');
  const save = useAction(async () => {
    const body = { name: name.trim(), closedPlaylistId: closed || null, tickerTemplateId: ticker || null };
    if (zone) await patch(`/api/v1/zones/${zone.id}`, { ...body, revision: zone.revision });
    else await post('/api/v1/zones', body);
    toast(t('admin.saved'));
    onSaved();
  });
  const tickerOptions = [
    { value: '', label: t('admin.zones.tickerDefault') },
    ...(templates?.tickerTemplates ?? []).map((x) => ({ value: x.id, label: x.name })),
  ];
  if (ticker && !templates?.tickerTemplates.some((x) => x.id === ticker)) tickerOptions.push({ value: ticker, label: '…' });
  return (
    <Dialog
      title={zone ? t('admin.zones.edit') : t('admin.zones.add')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button variant="primary" type="submit" form="zone-form" busy={save.busy} disabled={!name.trim()}>
            {t('common.action.save')}
          </Button>
        </>
      }
    >
      <form
        id="zone-form"
        className="form"
        onSubmit={(e) => {
          e.preventDefault();
          if (name.trim()) void save.run();
        }}
      >
        <TextInput label={t('admin.zones.name')} value={name} maxLength={200} disabled={nameLocked} placeholder={t('admin.zones.namePlaceholder')} onChange={(e) => setName(e.target.value)} />
        <Select
          label={t('admin.zones.closedPlaylist')}
          value={closed}
          hint={t('admin.zones.closedPlaylistHint')}
          onChange={(e) => setClosed(e.target.value)}
          options={playlistOptions(playlists, closed, t('admin.zones.closedPlaylistNone'), (n) => t('admin.zones.notPublished', { name: n }))}
        />
        <Select label={t('admin.zones.tickerTemplate')} value={ticker} hint={t('admin.zones.tickerTemplateHint')} onChange={(e) => setTicker(e.target.value)} options={tickerOptions} />
        <ErrorText error={save.error} />
      </form>
    </Dialog>
  );
}

export function ZonesPage() {
  const t = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const rights = useRights();
  const zones = useQuery<{ zones: Zone[] }>('/api/v1/zones');
  const playlists = useQuery<{ playlists: PlaylistSummary[] }>('/api/v1/playlists');
  const templates = useQuery<TickerTemplateList>('/api/v1/ticker-templates');
  useEvents(['zones', 'devices'], () => zones.reload());
  useEvents(['live'], (e) => {
    if (e.topic === '*' || e.kind?.startsWith('zone.')) zones.reload();
  });
  useEvents(['content'], () => playlists.reload());
  const [editing, setEditing] = useState<Zone | 'new' | null>(null);
  const [screensOf, setScreensOf] = useState<string | null>(null);
  const close = useCallback(() => setEditing(null), []);

  const remove = useAction(async (z: Zone) => {
    const usage = await get<ZoneUsage>(`/api/v1/zones/${z.id}/usage`);
    const lines = [t('admin.zones.deleteConfirm', { name: z.name })];
    if (usage.devices > 0) lines.push(t('admin.zones.deleteDevices', { count: usage.devices }));
    if (usage.rules > 0) lines.push(t('admin.zones.deleteRules', { count: usage.rules }));
    if (usage.users > 0) lines.push(t('admin.zones.deleteUsers', { count: usage.users }));
    const ok = await confirm({ title: t('admin.zones.delete'), message: lines.join('\n\n'), confirmLabel: t('common.action.delete'), danger: true });
    if (!ok) return;
    const res = await del<{ devicesAffected: number; rulesDeleted: number }>(`/api/v1/zones/${z.id}`);
    toast(res?.devicesAffected ? t('admin.zones.deletedDevices', { name: z.name, count: res.devicesAffected }) : t('admin.zones.deleted', { name: z.name }));
    zones.reload();
  });

  const playlistName = (id?: string): string => {
    if (!id) return '—';
    return playlists.data?.playlists.find((p) => p.id === id)?.name ?? '…';
  };
  const templateName = (id?: string): string => {
    if (!id) return t('admin.zones.tickerDefault');
    return templates.data?.tickerTemplates.find((x) => x.id === id)?.name ?? '…';
  };
  const list = zones.data?.zones ?? [];

  return (
    <div className="page">
      <PageHeader
        title={t('admin.zones.title')}
        subtitle={t('admin.zones.subtitle')}
        actions={
          rights.canManageGlobal && (
            <Button variant="primary" icon={<Plus size={16} />} onClick={() => setEditing('new')}>
              {t('admin.zones.add')}
            </Button>
          )
        }
      />
      <ErrorText error={remove.error} />
      <DataState loading={zones.loading && !zones.data} error={zones.error} onRetry={zones.reload} empty={list.length === 0} emptyText={t('admin.zones.empty')}>
        <table className="table">
          <thead>
            <tr>
              <th>{t('admin.zones.name')}</th>
              <th>{t('admin.zones.state')}</th>
              <th className="num">{t('admin.zones.devices')}</th>
              <th>{t('admin.zones.groupsColumn')}</th>
              <th>{t('admin.zones.closedPlaylist')}</th>
              <th>{t('admin.zones.tickerTemplate')}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {list.map((z) => (
              <tr key={z.id}>
                <td className="strong">{z.name}</td>
                <td>{z.state === 'OPEN' ? <Badge tone="success">{t('admin.zones.open')}</Badge> : <Badge tone="warning">{t('admin.zones.closed')}</Badge>}</td>
                <td className="num">{z.deviceCount}</td>
                <td className={(z.groups ?? []).length ? '' : 'faint'}>{(z.groups ?? []).map((g) => g.name).join(', ') || '—'}</td>
                <td className="muted">{playlistName(z.closedPlaylistId)}</td>
                <td className="muted">{templateName(z.tickerTemplateId)}</td>
                <td className="row-actions">
                  {!rights.canEditZone(z.id) && <Badge>{t('admin.readOnly.badge')}</Badge>}
                  {rights.canManageGlobal && (
                    <IconButton label={t('admin.zones.screens')} onClick={() => setScreensOf(z.id)}>
                      <MonitorSmartphone size={16} />
                    </IconButton>
                  )}
                  <Link to={`/schedules#zone-${z.id}`} className="icon-btn" title={t('admin.zones.schedules')} aria-label={t('admin.zones.schedules')}>
                    <CalendarClock size={16} />
                  </Link>
                  {rights.canEditZone(z.id) && (
                    <IconButton label={t('common.action.edit')} onClick={() => setEditing(z)}>
                      <Pencil size={16} />
                    </IconButton>
                  )}
                  {rights.canManageGlobal && (
                    <IconButton label={t('common.action.delete')} disabled={remove.busy} onClick={() => void remove.run(z)}>
                      <Trash2 size={16} />
                    </IconButton>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        <p className="faint zones-hint">{t('admin.zones.hint')}</p>
      </DataState>
      {screensOf && list.some((z) => z.id === screensOf) && (
        <ZoneScreensDialog
          zone={{ ...list.find((z) => z.id === screensOf)!, groups: list.find((z) => z.id === screensOf)!.groups ?? [] }}
          playlists={playlists.data?.playlists}
          onClose={() => setScreensOf(null)}
          onChanged={zones.reload}
        />
      )}
      {editing && (
        <ZoneDialog
          zone={editing === 'new' ? null : editing}
          playlists={playlists.data?.playlists}
          templates={templates.data}
          onClose={close}
          onSaved={() => {
            setEditing(null);
            zones.reload();
          }}
        />
      )}
    </div>
  );
}
