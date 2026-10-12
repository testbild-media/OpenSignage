// Screens of a zone: display groups – each optionally with its own playlist
// for CLOSED – and the place of every screen. A playlist for several screens plays track n
// on place n; screens without group form a row of their own. Every screen of the zone
// switches in step, with or without groups.
import { useEffect, useState } from 'react';
import { Pencil, Plus, Trash2 } from 'lucide-react';
import { del, patch, post } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { useConfirm, useToast } from '../../components/feedback';
import { Badge, Button, DataState, Dialog, ErrorText, IconButton, TextInput } from '../../components/ui';
import type { Device } from '../devices/model';
import { StatusBadge } from '../devices/Monitor';
import { playlistOptions, type DisplayGroup, type PlaylistSummary, type Zone } from './types';

/** Most places a screen can have (server: devices.MaxPosition). */
export const MAX_PLACE = 99;

function GroupNameDialog({ zoneId, group, onClose, onSaved }: { zoneId: string; group: DisplayGroup | null; onClose: () => void; onSaved: () => void }) {
  const t = useT();
  const [name, setName] = useState(group?.name ?? '');
  const save = useAction(async () => {
    if (group) await patch(`/api/v1/zones/${zoneId}/groups/${group.id}`, { revision: group.revision, name: name.trim() });
    else await post(`/api/v1/zones/${zoneId}/groups`, { name: name.trim() });
    onSaved();
  });
  return (
    <Dialog
      title={group ? t('admin.zones.groups.rename') : t('admin.zones.groups.add')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button variant="primary" busy={save.busy} disabled={!name.trim()} onClick={() => void save.run()}>
            {t('common.action.save')}
          </Button>
        </>
      }
    >
      <div className="form">
        <TextInput label={t('admin.zones.groups.name')} value={name} maxLength={200} placeholder={t('admin.zones.groups.namePlaceholder')} onChange={(e) => setName(e.target.value)} />
        <ErrorText error={save.error} />
      </div>
    </Dialog>
  );
}

/** Place of a screen: saved on Enter or when leaving the field. */
function PlaceInput({ value, label, disabled, onCommit }: { value: number; label: string; disabled?: boolean; onCommit: (v: number) => void }) {
  const [text, setText] = useState(value > 0 ? String(value) : '');
  useEffect(() => setText(value > 0 ? String(value) : ''), [value]);
  const commit = (): void => {
    const n = Number(text);
    if (Number.isInteger(n) && n >= 1 && n <= MAX_PLACE && n !== value) onCommit(n);
    else setText(value > 0 ? String(value) : '');
  };
  return (
    <input
      className="input screens-place"
      type="number"
      inputMode="numeric"
      min={1}
      max={MAX_PLACE}
      aria-label={label}
      title={label}
      value={text}
      disabled={disabled}
      onChange={(e) => setText(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === 'Enter') {
          e.preventDefault();
          commit();
        }
      }}
    />
  );
}

export function ZoneScreensDialog({ zone, playlists, onClose, onChanged }: { zone: Zone; playlists: PlaylistSummary[] | undefined; onClose: () => void; onChanged: () => void }) {
  const t = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const devices = useQuery<{ devices: Device[] }>('/api/v1/devices');
  useEvents(['devices'], () => devices.reload());
  const [naming, setNaming] = useState<DisplayGroup | 'new' | null>(null);
  const mine = (devices.data?.devices ?? []).filter((d) => d.zoneId === zone.id);
  const inGroup = (gid: string): Device[] =>
    mine.filter((d) => (d.groupId ?? '') === gid).sort((a, b) => (a.position || MAX_PLACE + 1) - (b.position || MAX_PLACE + 1) || a.name.localeCompare(b.name));

  const change = useAction(async (d: Device, body: Record<string, unknown>) => {
    const next = await patch<Device>(`/api/v1/devices/${encodeURIComponent(d.id)}`, body);
    if (devices.data) devices.setData({ ...devices.data, devices: devices.data.devices.map((x) => (x.id === next.id ? next : x)) });
    onChanged(); // device counts of the groups
  });
  const setClosed = useAction(async (g: DisplayGroup, playlistId: string) => {
    await patch(`/api/v1/zones/${zone.id}/groups/${g.id}`, { revision: g.revision, closedPlaylistId: playlistId || null });
    onChanged();
  });
  const remove = useAction(async (g: DisplayGroup) => {
    const ok = await confirm({
      title: t('admin.zones.groups.delete'),
      message: t('admin.zones.groups.deleteConfirm', { name: g.name }),
      confirmLabel: t('common.action.delete'),
      danger: true,
    });
    if (!ok) return;
    await del(`/api/v1/zones/${zone.id}/groups/${g.id}`);
    toast(t('admin.zones.groups.deleted', { name: g.name }));
    onChanged();
    devices.reload();
  });
  const groupOptions = [...zone.groups.map((g) => ({ value: g.id, label: g.name })), { value: '', label: t('admin.zones.groups.ungrouped') }];
  const sections: { id: string; group: DisplayGroup | null }[] = [...zone.groups.map((g) => ({ id: g.id, group: g })), { id: '', group: null }];
  const busy = change.busy || setClosed.busy || remove.busy;

  return (
    <Dialog title={t('admin.zones.screensTitle', { name: zone.name })} onClose={onClose} wide footer={<Button onClick={onClose}>{t('common.action.close')}</Button>}>
      <p className="muted screens-hint">{t('admin.zones.screensHint')}</p>
      <ErrorText error={change.error ?? setClosed.error ?? remove.error} />
      <DataState loading={devices.loading && !devices.data} error={devices.error} onRetry={devices.reload}>
        {mine.length === 0 && <p className="faint">{t('admin.zones.groups.noDevices')}</p>}
        <div className="screens-sections">
          {sections.map(({ id, group }) => {
            const list = inGroup(id);
            if (!group && list.length === 0 && zone.groups.length > 0) return null;
            const places = new Map<number, number>();
            for (const d of list) if (d.position) places.set(d.position, (places.get(d.position) ?? 0) + 1);
            return (
              <section key={id || 'ungrouped'} className="screens-section">
                <div className="screens-head">
                  <h3>{group ? group.name : t('admin.zones.groups.ungrouped')}</h3>
                  <Badge>{t('admin.zones.groups.count', { count: list.length })}</Badge>
                  {group && (
                    <span className="screens-head-actions">
                      <IconButton label={t('admin.zones.groups.rename')} onClick={() => setNaming(group)}>
                        <Pencil size={16} />
                      </IconButton>
                      <IconButton label={t('admin.zones.groups.delete')} disabled={busy} onClick={() => void remove.run(group)}>
                        <Trash2 size={16} />
                      </IconButton>
                    </span>
                  )}
                </div>
                {group && (
                  <label className="screens-closed">
                    <span>{t('admin.zones.groups.closedPlaylist')}</span>
                    <select
                      className="input"
                      value={group.closedPlaylistId ?? ''}
                      disabled={busy}
                      onChange={(e) => void setClosed.run(group, e.target.value)}
                    >
                      {playlistOptions(playlists, group.closedPlaylistId, t('admin.zones.groups.closedZone'), (n) => t('admin.zones.notPublished', { name: n })).map((o) => (
                        <option key={o.value} value={o.value}>
                          {o.label}
                        </option>
                      ))}
                    </select>
                  </label>
                )}
                {list.length === 0 ? (
                  <p className="faint">{t('admin.zones.groups.empty')}</p>
                ) : (
                  <ul className="screens-list">
                    {list.map((d) => (
                      <li key={d.id} className="screens-row">
                        <span className="screens-name">{d.name}</span>
                        <StatusBadge d={d} />
                        {d.position > 0 && (places.get(d.position) ?? 0) > 1 && <Badge tone="warning">{t('admin.zones.groups.placeShared')}</Badge>}
                        <span className="screens-controls">
                          <PlaceInput value={d.position} label={t('admin.zones.groups.place')} disabled={busy} onCommit={(position) => void change.run(d, { position })} />
                          <select
                            className="input screens-group"
                            aria-label={t('admin.zones.groups.group')}
                            value={d.groupId ?? ''}
                            disabled={busy}
                            onChange={(e) => void change.run(d, { groupId: e.target.value || null })}
                          >
                            {groupOptions.map((o) => (
                              <option key={o.value} value={o.value}>
                                {o.label}
                              </option>
                            ))}
                          </select>
                        </span>
                      </li>
                    ))}
                  </ul>
                )}
              </section>
            );
          })}
        </div>
        <Button icon={<Plus size={16} />} onClick={() => setNaming('new')}>
          {t('admin.zones.groups.add')}
        </Button>
      </DataState>
      {naming && (
        <GroupNameDialog
          zoneId={zone.id}
          group={naming === 'new' ? null : naming}
          onClose={() => setNaming(null)}
          onSaved={() => {
            setNaming(null);
            onChanged();
          }}
        />
      )}
    </Dialog>
  );
}
