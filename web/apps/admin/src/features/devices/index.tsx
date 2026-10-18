import { useEffect, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router';
import { EyeOff, Monitor, Plus, Trash2, Link2 } from 'lucide-react';
import type { DisplayConfig } from '@opensignage/contracts';
import type { Feature } from '../types';
import { del, patch, post } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { useConfirm, useToast } from '../../components/feedback';
import {
  Badge,
  Button,
  Checkbox,
  DataState,
  Dialog,
  ErrorText,
  IconButton,
  PageHeader,
  Select,
  TextInput,
} from '../../components/ui';
import { formatDate } from '../media/model';
import { ActionsCard, AppliedLine } from './Actions';
import { AlertsPage, ProblemBadges } from './Alerts';
import { DisplayPicker } from './DisplayPicker';
import { MonitorCard, StatusBadge, StorageCard, SyncCell } from './Monitor';
import { MAX_PLACE } from '../zones/ZoneScreens';
import { customModeValid, openAlerts, sameDisplay, type Device, type DeviceConfig } from './model';
import { useRights } from '../../lib/rights';
import './devices.css';

interface Pairing {
  id: string;
  pin: string;
  hostname: string;
  name?: string; // given at installation
  version: string;
  versionMatch: boolean;
  os: string;
  arch: string;
  ips: string[];
  createdAt: string;
}
interface Zone {
  id: string;
  name: string;
  groups?: { id: string; name: string }[];
}

function ClaimDialog({
  pairing,
  fingerprint,
  zones,
  onClose,
}: {
  pairing: Pairing;
  fingerprint: string;
  zones: Zone[];
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const navigate = useNavigate();
  const [pin, setPin] = useState('');
  const [name, setName] = useState(pairing.name || pairing.hostname);
  const [zoneId, setZoneId] = useState('');
  const [scaleMode, setScaleMode] = useState('');
  const claim = useAction(async () => {
    const d = await post<Device>('/api/v1/devices/claim', {
      pin: pin.trim(),
      name: name.trim(),
      ...(zoneId ? { zoneId } : {}),
      ...(scaleMode ? { scaleMode } : {}),
    });
    toast(t('devices.claimed', { name: d.name }));
    navigate(`/devices/${d.id}`);
  });
  return (
    <Dialog
      title={t('devices.claimTitle')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button
            variant="primary"
            busy={claim.busy}
            disabled={pin.trim().length !== 6 || !name.trim()}
            onClick={() => void claim.run()}
          >
            {t('devices.claim')}
          </Button>
        </>
      }
    >
      <div className="form">
        <p className="muted">{t('devices.claimHint')}</p>
        {fingerprint && <p className="muted">{t('devices.fingerprintHint', { fingerprint })}</p>}
        <TextInput
          label={t('devices.pin')}
          value={pin}
          inputMode="numeric"
          maxLength={6}
          autoFocus
          onChange={(e) => setPin(e.target.value.replace(/\D/g, ''))}
        />
        <TextInput label={t('fields.name')} value={name} onChange={(e) => setName(e.target.value)} />
        <Select
          label={t('devices.zone')}
          value={zoneId}
          onChange={(e) => setZoneId(e.target.value)}
          options={[
            { value: '', label: t('devices.noZone') },
            ...zones.map((z) => ({ value: z.id, label: z.name })),
          ]}
        />
        <Select
          label={t('admin.defaults.scaleMode')}
          value={scaleMode}
          onChange={(e) => setScaleMode(e.target.value)}
          options={[
            { value: '', label: t('devices.scaleDefault') },
            ...['contain', 'cover', 'none'].map((v) => ({ value: v, label: t(`admin.scale.${v}`) })),
          ]}
        />
        {!pairing.versionMatch && (
          <div className="form-error">{t('devices.versionMismatchClaim', { version: pairing.version })}</div>
        )}
        <ErrorText error={claim.error} />
      </div>
    </Dialog>
  );
}

function DevicesPage() {
  const t = useT();
  const navigate = useNavigate();
  const list = useQuery<{ devices: Device[] }>('/api/v1/devices');
  // Pairing new screens stays with admins and operators of all zones.
  const manage = useRights().canManageGlobal;
  const pairing = useQuery<{ pairingRequests: Pairing[]; fingerprint: string }>(manage ? '/api/v1/devices/pairing' : null);
  const zones = useQuery<{ zones: Zone[] }>('/api/v1/zones');
  const [claim, setClaim] = useState<Pairing | null>(null);
  const toast = useToast();
  // Hides an unknown screen's request (it shows up again when the screen asks anew).
  const dismiss = useAction(async (p: Pairing) => {
    await del(`/api/v1/devices/pairing/${p.id}`);
    pairing.reload();
    toast(t('devices.dismissed', { name: p.name || p.hostname }));
  });
  useEvents(['devices', 'zones'], () => {
    list.reload();
    pairing.reload();
  });
  useEffect(() => {
    if (!manage) return;
    const id = window.setInterval(() => pairing.reload(), 10000);
    return () => clearInterval(id);
  }, [pairing, manage]);
  const pending = pairing.data?.pairingRequests ?? [];
  return (
    <div className="page">
      <PageHeader title={t('admin.nav.devices')} />
      {pending.length > 0 && (
        <div className="card">
          <h2>{t('devices.pending')}</h2>
          <table className="table">
            <tbody>
              {pending.map((p) => (
                <tr key={p.id}>
                  <td>
                    <b>{p.name || p.hostname}</b>{' '}
                    <span className="faint">
                      {p.name ? `${p.hostname} · ` : ''}
                      {p.os}/{p.arch}
                    </span>
                  </td>
                  <td className="mono">{p.ips.join(', ')}</td>
                  <td>
                    {p.versionMatch ? <Badge>{p.version}</Badge> : <Badge tone="warning">{p.version}</Badge>}
                  </td>
                  <td className="row-actions">
                    <IconButton label={t('devices.dismiss')} onClick={() => void dismiss.run(p)}>
                      <EyeOff size={16} />
                    </IconButton>
                    <Button variant="primary" icon={<Plus size={16} />} onClick={() => setClaim(p)}>
                      {t('devices.claim')}
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <DataState
        loading={list.loading && !list.data}
        error={list.error}
        onRetry={list.reload}
        empty={list.data?.devices.length === 0}
        emptyText={t('devices.empty')}
      >
        <table className="table">
          <thead>
            <tr>
              <th>{t('fields.name')}</th>
              <th>{t('devices.zone')}</th>
              <th>{t('devices.state')}</th>
              <th>{t('admin.dashboard.alerts')}</th>
              <th>{t('devices.version')}</th>
              <th>{t('devices.sync')}</th>
              <th>{t('client.status.ips')}</th>
              <th>{t('devices.lastSeen')}</th>
            </tr>
          </thead>
          <tbody>
            {list.data?.devices.map((d) => (
              <tr key={d.id} className="clickable" onClick={() => navigate(`/devices/${d.id}`)}>
                <td>
                  <Link to={`/devices/${d.id}`} onClick={(e) => e.stopPropagation()}>
                    {d.name}
                  </Link>
                </td>
                <td className={d.zoneName ? '' : 'faint'}>
                  {d.zoneName || t('devices.noZone')}
                  {d.groupName ? ` / ${d.groupName}` : ''}
                </td>
                <td>
                  <StatusBadge d={d} />
                </td>
                <td>
                  {openAlerts(d.alerts).some((a) => a.kind !== 'device_offline') ? (
                    <ProblemBadges alerts={(d.alerts ?? []).filter((a) => a.kind !== 'device_offline')} />
                  ) : (
                    <span className="faint">—</span>
                  )}
                </td>
                <td>{d.versionMatch ? d.version : <Badge tone="warning">{d.version || '—'}</Badge>}</td>
                <td>
                  <SyncCell d={d} />
                </td>
                <td className="mono">{d.ips.join(', ')}</td>
                <td className="faint">{d.lastSeenAt ? formatDate(d.lastSeenAt) : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </DataState>
      {claim && (
        <ClaimDialog
          pairing={claim}
          fingerprint={pairing.data?.fingerprint ?? ''}
          zones={zones.data?.zones ?? []}
          onClose={() => setClaim(null)}
        />
      )}
    </div>
  );
}

// groupId/position: display group and place; "" = none / next free place.
type Form = { name: string; zoneId: string; groupId: string; position: string; config: DeviceConfig };

function DeviceDetail() {
  const t = useT();
  const rights = useRights();
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const confirm = useConfirm();
  const q = useQuery<Device>(`/api/v1/devices/${encodeURIComponent(id)}`);
  const zones = useQuery<{ zones: Zone[] }>('/api/v1/zones');
  const [form, setForm] = useState<Form | null>(null);
  const [shot, setShot] = useState(0);
  useEvents(['devices'], (e) => {
    if (!e.id || e.id === id) q.reload();
  });
  const [dirty, setDirty] = useState(false);
  useEffect(() => {
    // Status events arrive every 30 s: never overwrite unsaved edits.
    if (q.data && !dirty)
      setForm({
        name: q.data.name,
        zoneId: q.data.zoneId ?? '',
        groupId: q.data.groupId ?? '',
        position: q.data.position ? String(q.data.position) : '',
        config: q.data.config,
      });
  }, [q.data, dirty]);
  const edit = (next: Form): void => {
    setDirty(true);
    setForm(next);
  };
  const setDisplay = (display: DisplayConfig): void => {
    if (form) edit({ ...form, config: { ...form.config, display } });
  };
  const save = useAction(async () => {
    if (!form) return;
    const c = form.config;
    const place = Number(form.position);
    const d = await patch<Device>(`/api/v1/devices/${encodeURIComponent(id)}`, {
      name: form.name,
      zoneId: form.zoneId || null,
      ...(form.zoneId ? { groupId: form.groupId || null } : {}),
      ...(form.zoneId && Number.isInteger(place) && place >= 1 && place <= MAX_PLACE ? { position: place } : {}),
      config: {
        scaleMode: c.scaleMode,
        display: c.display,
        perf: c.perf,
        hwVideo: c.hwVideo,
        plasmaShift: c.plasmaShift,
        showIpOverlay: c.showIpOverlay,
      },
    });
    setDirty(false);
    q.setData(d);
    toast(t('admin.saved'));
  });
  const repair = useAction(async () => {
    if (
      !(await confirm({
        title: t('devices.repair'),
        message: t('devices.repairConfirm'),
        confirmLabel: t('devices.repair'),
        danger: true,
      }))
    )
      return;
    await post(`/api/v1/devices/${encodeURIComponent(id)}/repair`);
    q.reload();
  });
  const remove = useAction(async () => {
    if (
      !(await confirm({
        title: t('devices.delete'),
        message: t('devices.deleteConfirm'),
        confirmLabel: t('common.action.delete'),
        danger: true,
      }))
    )
      return;
    await del(`/api/v1/devices/${encodeURIComponent(id)}`);
    navigate('/devices');
  });
  const d = q.data;
  const disp = form?.config.display;
  const displayInvalid = !!disp && disp.width > 0 && disp.custom && !customModeValid(disp);
  const displayEdited = !!d && !!disp && !sameDisplay(disp, d.config.display);
  return (
    <div className="page">
      <PageHeader
        title={d?.name ?? ''}
        subtitle={
          d
            ? [d.zoneName || t('devices.noZone'), d.groupName, d.zoneId && d.position ? t('devices.placeN', { place: d.position }) : '', d.ips.join(', ')]
                .filter(Boolean)
                .join(' · ')
            : ''
        }
        actions={
          <Link className="btn btn-secondary" to="/devices">
            {t('common.action.back')}
          </Link>
        }
      />
      <DataState loading={!d && !q.error} error={q.error} onRetry={q.reload}>
        {d && form && disp && (
          <div className="device-grid">
            <MonitorCard d={d} />
            <ActionsCard d={d} onChanged={q.reload} onScreenshot={() => setShot(Date.now())} shot={shot} control={rights.canControlDevice(d.zoneId)} />
            <StorageCard d={d} onChanged={q.reload} control={rights.canControlDevice(d.zoneId)} />
            {rights.canManageGlobal && (
            <>
            <div className="card form">
              <h2>{t('devices.settings')}</h2>
              <TextInput
                label={t('fields.name')}
                value={form.name}
                onChange={(e) => edit({ ...form, name: e.target.value })}
              />
              <Select
                label={t('devices.zone')}
                value={form.zoneId}
                onChange={(e) => edit({ ...form, zoneId: e.target.value, groupId: '', position: '' })}
                options={[
                  { value: '', label: t('devices.noZone') },
                  ...(zones.data?.zones ?? []).map((z) => ({ value: z.id, label: z.name })),
                ]}
              />
              {form.zoneId && (
                <div className="device-place">
                  <Select
                    label={t('devices.group')}
                    value={form.groupId}
                    onChange={(e) => edit({ ...form, groupId: e.target.value, position: '' })}
                    options={[
                      { value: '', label: t('devices.noGroup') },
                      ...((zones.data?.zones ?? []).find((z) => z.id === form.zoneId)?.groups ?? []).map((g) => ({ value: g.id, label: g.name })),
                    ]}
                  />
                  <TextInput
                    label={t('devices.place')}
                    type="number"
                    inputMode="numeric"
                    min={1}
                    max={MAX_PLACE}
                    value={form.position}
                    placeholder={t('devices.placeNext')}
                    hint={t('devices.placeHint')}
                    onChange={(e) => edit({ ...form, position: e.target.value })}
                  />
                </div>
              )}
              <Select
                label={t('admin.defaults.scaleMode')}
                value={form.config.scaleMode}
                onChange={(e) =>
                  edit({
                    ...form,
                    config: { ...form.config, scaleMode: e.target.value as DeviceConfig['scaleMode'] },
                  })
                }
                options={['contain', 'cover', 'none'].map((v) => ({
                  value: v,
                  label: t(`admin.scale.${v}`),
                }))}
              />
              <DisplayPicker value={disp} modes={d.displayModes} onChange={setDisplay} />
              <Select
                label={t('devices.rotation')}
                value={String(disp.rotation)}
                onChange={(e) =>
                  setDisplay({ ...disp, rotation: Number(e.target.value) as DisplayConfig['rotation'] })
                }
                options={['0', '90', '180', '270'].map((v) => ({ value: v, label: `${v}°` }))}
              />
              {!displayEdited && (
                <AppliedLine
                  state={d.applied?.display}
                  online={d.online}
                  error={d.applied?.displayError}
                  localMode={d.applied?.displayMode}
                />
              )}
              <Select
                label={t('devices.perf')}
                value={form.config.perf}
                onChange={(e) => edit({ ...form, config: { ...form.config, perf: e.target.value } })}
                options={['auto', 'low', 'standard', 'high'].map((v) => ({
                  value: v,
                  label: t(`devices.perfLevel.${v}`),
                }))}
              />
              <Select
                label={t('devices.hwVideo')}
                value={form.config.hwVideo}
                onChange={(e) => edit({ ...form, config: { ...form.config, hwVideo: e.target.value } })}
                options={['auto', 'on', 'off'].map((v) => ({ value: v, label: t(`devices.hw.${v}`) }))}
              />
              <Checkbox
                label={t('devices.ipOverlay')}
                checked={form.config.showIpOverlay}
                onChange={(v) => edit({ ...form, config: { ...form.config, showIpOverlay: v } })}
              />
              <Checkbox
                label={t('devices.plasma')}
                checked={form.config.plasmaShift}
                onChange={(v) => edit({ ...form, config: { ...form.config, plasmaShift: v } })}
              />
              <ErrorText error={save.error} />
              <div className="form-actions">
                <Button
                  variant="primary"
                  busy={save.busy}
                  disabled={displayInvalid}
                  onClick={() => void save.run()}
                >
                  {t('common.action.save')}
                </Button>
              </div>
            </div>
            <div className="card">
              <h2>{t('devices.maintenance')}</h2>
              <div className="action-grid">
                <Button icon={<Link2 size={16} />} busy={repair.busy} onClick={() => void repair.run()}>
                  {t('devices.repair')}
                </Button>
                <Button
                  icon={<Trash2 size={16} />}
                  variant="danger"
                  busy={remove.busy}
                  onClick={() => void remove.run()}
                >
                  {t('devices.delete')}
                </Button>
              </div>
              <ErrorText error={repair.error ?? remove.error} />
            </div>
            </>
            )}
          </div>
        )}
      </DataState>
    </div>
  );
}

export const devicesFeature: Feature = {
  routes: [
    { path: '/devices', element: <DevicesPage />, minRole: 'operator' },
    { path: '/devices/:id', element: <DeviceDetail />, minRole: 'operator' },
    { path: '/alerts', element: <AlertsPage />, minRole: 'operator' },
  ],
  nav: [
    {
      key: 'devices',
      label: 'admin.nav.devices',
      icon: Monitor,
      path: '/devices',
      minRole: 'operator',
      order: 70,
    },
  ],
};
