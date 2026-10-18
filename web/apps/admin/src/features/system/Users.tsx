import { useState } from 'react';
import { KeyRound, Pencil, Plus, Trash2 } from 'lucide-react';
import type { Role } from '@opensignage/contracts';
import { del, patch, post } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { useSession } from '../../lib/session';
import { useConfirm, useToast } from '../../components/feedback';
import { Badge, Button, Checkbox, DataState, Dialog, ErrorText, IconButton, PageHeader, PasswordPair, Select, TextInput, passwordPairValid } from '../../components/ui';
import type { MediaRight, ZoneAccess } from '../../lib/rights';

/** Rights as the users API stores them (server: internal/server/users/rights.go). */
export interface UserRights {
  scope: 'all' | 'zones';
  zones: { zoneId: string; access: ZoneAccess }[];
  createContent: boolean;
  media: MediaRight;
  products: 'view' | 'edit';
  devices: boolean;
}

export interface UserRow {
  id: string;
  username: string;
  displayName: string;
  role: Role;
  active: boolean;
  language: string;
  zoneIds: string[];
  rights?: UserRights;
}

const DEFAULT_RIGHTS: UserRights = { scope: 'all', zones: [], createContent: false, media: 'use', products: 'view', devices: false };

/** Zone access of a user row as one line ("Bar 2 (edit), Bar 3 (view)"). */
function zonesText(u: UserRow, zoneName: (id: string) => string, t: (k: string) => string): string {
  if (u.role === 'admin' || (u.role === 'operator' && u.rights?.scope !== 'zones')) return t('admin.users.allZones');
  const zones = u.rights?.zones ?? u.zoneIds.map((zoneId) => ({ zoneId, access: 'edit' as const }));
  if (u.role === 'user') return zones.map((z) => zoneName(z.zoneId)).join(', ') || '—';
  return zones.map((z) => `${zoneName(z.zoneId)} (${t(`admin.rights.access.${z.access}`)})`).join(', ') || '—';
}

/** Rights of an operator limited to zones: zones (view/edit), new content, media, products, displays. */
function RightsEditor({ rights, zones, onChange }: { rights: UserRights; zones: ZoneRow[]; onChange: (r: UserRights) => void }) {
  const t = useT();
  const access = (id: string): ZoneAccess | '' => rights.zones.find((z) => z.zoneId === id)?.access ?? '';
  const setAccess = (id: string, a: ZoneAccess | ''): void =>
    onChange({ ...rights, zones: a ? [...rights.zones.filter((z) => z.zoneId !== id), { zoneId: id, access: a }] : rights.zones.filter((z) => z.zoneId !== id) });
  return (
    <div className="rights">
      <Select
        label={t('admin.rights.scope')}
        value={rights.scope}
        options={(['all', 'zones'] as const).map((s) => ({ value: s, label: t(`admin.rights.scope.${s}`) }))}
        hint={t(`admin.rights.scope.${rights.scope}.hint`)}
        onChange={(e) => onChange({ ...rights, scope: e.target.value as UserRights['scope'] })}
      />
      {rights.scope === 'zones' && (
        <>
          <div className="field">
            <label>{t('admin.users.zones')}</label>
            {zones.length === 0 && <div className="field-hint">{t('admin.users.noZones')}</div>}
            <div className="rights-zones">
              {zones.map((z) => (
                <label key={z.id} className="rights-zone">
                  <span>{z.name}</span>
                  <select className="input input-sm" value={access(z.id)} onChange={(e) => setAccess(z.id, e.target.value as ZoneAccess | '')}>
                    <option value="">{t('admin.rights.access.none')}</option>
                    <option value="view">{t('admin.rights.access.view')}</option>
                    <option value="edit">{t('admin.rights.access.edit')}</option>
                  </select>
                </label>
              ))}
            </div>
            <div className="field-hint">{t('admin.rights.zonesHint')}</div>
          </div>
          <Checkbox label={t('admin.rights.createContent')} checked={rights.createContent} onChange={(v) => onChange({ ...rights, createContent: v })} />
          <Select
            label={t('admin.rights.media')}
            value={rights.media}
            options={(['use', 'upload', 'manage'] as const).map((m) => ({ value: m, label: t(`admin.rights.media.${m}`) }))}
            hint={t(`admin.rights.media.${rights.media}.hint`)}
            onChange={(e) => onChange({ ...rights, media: e.target.value as MediaRight })}
          />
          <Select
            label={t('admin.rights.products')}
            value={rights.products}
            options={(['view', 'edit'] as const).map((p) => ({ value: p, label: t(`admin.rights.products.${p}`) }))}
            hint={t(`admin.rights.products.${rights.products}.hint`)}
            onChange={(e) => onChange({ ...rights, products: e.target.value as UserRights['products'] })}
          />
          <Checkbox label={t('admin.rights.devices')} checked={rights.devices} onChange={(v) => onChange({ ...rights, devices: v })} />
        </>
      )}
    </div>
  );
}

interface ZoneRow {
  id: string;
  name: string;
}

function roleOptions(t: (k: string) => string) {
  return (['admin', 'operator', 'user'] as Role[]).map((r) => ({ value: r, label: t(`admin.role.${r}`) }));
}

function UserDialog({ user, zones, onClose, onSaved }: { user: UserRow | null; zones: ZoneRow[]; onClose: () => void; onSaved: () => void }) {
  const t = useT();
  const toast = useToast();
  const [form, setForm] = useState({
    username: user?.username ?? '',
    displayName: user?.displayName ?? '',
    role: (user?.role ?? 'user') as Role,
    active: user?.active ?? true,
    password: '',
    passwordConfirm: '',
  });
  const [rights, setRights] = useState<UserRights>(user?.rights ?? { ...DEFAULT_RIGHTS, zones: (user?.zoneIds ?? []).map((zoneId) => ({ zoneId, access: 'edit' as const })) });
  // Users (bar staff) operate their zones live: the zone list with edit access, nothing else.
  const zoneIds = rights.zones.map((z) => z.zoneId);
  const setUserZones = (ids: string[]): void => setRights({ ...rights, zones: ids.map((zoneId) => ({ zoneId, access: 'edit' as const })) });
  const save = useAction(async () => {
    const body = { displayName: form.displayName, role: form.role, active: form.active, rights };
    if (user) {
      await patch(`/api/v1/users/${user.id}`, body);
    } else {
      await post('/api/v1/users', { ...body, username: form.username, password: form.password, passwordConfirm: form.passwordConfirm });
    }
    toast(t('admin.saved'));
    onSaved();
  });
  const valid = form.displayName.trim() !== '' && (user ? true : form.username.trim() !== '' && passwordPairValid(form.password, form.passwordConfirm));
  return (
    <Dialog
      title={user ? t('admin.users.edit') : t('admin.users.add')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button variant="primary" busy={save.busy} disabled={!valid} onClick={() => void save.run()}>
            {t('common.action.save')}
          </Button>
        </>
      }
    >
      <div className="form">
        <TextInput label={t('admin.login.username')} value={form.username} disabled={!!user} onChange={(e) => setForm({ ...form, username: e.target.value })} />
        <TextInput label={t('admin.users.displayName')} value={form.displayName} onChange={(e) => setForm({ ...form, displayName: e.target.value })} />
        <Select label={t('admin.users.role')} value={form.role} options={roleOptions(t)} onChange={(e) => setForm({ ...form, role: e.target.value as Role })} hint={t(`admin.role.${form.role}.hint`)} />
        {form.role === 'user' && (
          <div className="field">
            <label>{t('admin.users.zones')}</label>
            {zones.length === 0 && <div className="field-hint">{t('admin.users.noZones')}</div>}
            {zones.map((z) => (
              <Checkbox key={z.id} label={z.name} checked={zoneIds.includes(z.id)} onChange={(on) => setUserZones(on ? [...zoneIds, z.id] : zoneIds.filter((x) => x !== z.id))} />
            ))}
          </div>
        )}
        {form.role === 'operator' && <RightsEditor rights={rights} zones={zones} onChange={setRights} />}
        <Checkbox label={t('admin.users.active')} checked={form.active} onChange={(v) => setForm({ ...form, active: v })} />
        {!user && (
          <PasswordPair value={form.password} confirm={form.passwordConfirm} onChange={(v) => setForm({ ...form, password: v })} onConfirmChange={(v) => setForm({ ...form, passwordConfirm: v })} />
        )}
        <ErrorText error={save.error} />
      </div>
    </Dialog>
  );
}

function PasswordDialog({ user, onClose }: { user: UserRow; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const save = useAction(async () => {
    await post(`/api/v1/users/${user.id}/password`, { password: pw, passwordConfirm: pw2 });
    toast(t('admin.users.passwordSet'));
    onClose();
  });
  return (
    <Dialog
      title={t('admin.users.setPassword', { name: user.displayName })}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button variant="primary" busy={save.busy} disabled={!passwordPairValid(pw, pw2)} onClick={() => void save.run()}>
            {t('common.action.save')}
          </Button>
        </>
      }
    >
      <div className="form">
        <PasswordPair value={pw} confirm={pw2} onChange={setPw} onConfirmChange={setPw2} />
        <ErrorText error={save.error} />
      </div>
    </Dialog>
  );
}

export function UsersPage() {
  const t = useT();
  const confirm = useConfirm();
  const toast = useToast();
  const { user: me } = useSession();
  const users = useQuery<{ users: UserRow[] }>('/api/v1/users');
  const zones = useQuery<{ zones: ZoneRow[] }>('/api/v1/zones');
  useEvents(['users', 'zones'], () => {
    users.reload();
    zones.reload();
  });
  const [editing, setEditing] = useState<UserRow | null | 'new'>(null);
  const [pwFor, setPwFor] = useState<UserRow | null>(null);
  const remove = useAction(async (u: UserRow) => {
    const ok = await confirm({ title: t('admin.users.delete'), message: t('admin.users.deleteConfirm', { name: u.displayName }), confirmLabel: t('common.action.delete'), danger: true });
    if (!ok) return;
    await del(`/api/v1/users/${u.id}`);
    toast(t('admin.users.deleted'));
    users.reload();
  });
  const zoneName = (id: string): string => zones.data?.zones.find((z) => z.id === id)?.name ?? id;
  return (
    <div className="page">
      <PageHeader
        title={t('admin.users.title')}
        actions={
          <Button variant="primary" icon={<Plus size={16} />} onClick={() => setEditing('new')}>
            {t('admin.users.add')}
          </Button>
        }
      />
      <ErrorText error={remove.error} />
      <DataState loading={users.loading && !users.data} error={users.error} onRetry={users.reload}>
        <table className="table">
          <thead>
            <tr>
              <th>{t('admin.users.displayName')}</th>
              <th>{t('admin.login.username')}</th>
              <th>{t('admin.users.role')}</th>
              <th>{t('admin.users.zones')}</th>
              <th>{t('admin.users.status')}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {users.data?.users.map((u) => (
              <tr key={u.id}>
                <td>
                  {u.displayName} {u.id === me?.id && <Badge tone="primary">{t('admin.users.you')}</Badge>}
                </td>
                <td className="muted">{u.username}</td>
                <td>{t(`admin.role.${u.role}`)}</td>
                <td className="muted">{zonesText(u, zoneName, t)}</td>
                <td>{u.active ? <Badge tone="success">{t('admin.users.active')}</Badge> : <Badge>{t('admin.users.inactive')}</Badge>}</td>
                <td className="row-actions">
                  <IconButton label={t('common.action.edit')} onClick={() => setEditing(u)}>
                    <Pencil size={16} />
                  </IconButton>
                  <IconButton label={t('admin.users.setPasswordShort')} onClick={() => setPwFor(u)}>
                    <KeyRound size={16} />
                  </IconButton>
                  <IconButton label={t('common.action.delete')} onClick={() => void remove.run(u)} disabled={remove.busy}>
                    <Trash2 size={16} />
                  </IconButton>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </DataState>
      {editing && (
        <UserDialog
          user={editing === 'new' ? null : editing}
          zones={zones.data?.zones ?? []}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            users.reload();
          }}
        />
      )}
      {pwFor && <PasswordDialog user={pwFor} onClose={() => setPwFor(null)} />}
    </div>
  );
}
