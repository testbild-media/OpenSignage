import { useState } from 'react';
import { KeyRound, Pencil, Plus, Trash2 } from 'lucide-react';
import type { Role } from '@opensignage/contracts';
import { del, patch, post } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { useSession } from '../../lib/session';
import { useConfirm, useToast } from '../../components/feedback';
import { Badge, Button, Checkbox, DataState, Dialog, ErrorText, IconButton, PageHeader, PasswordPair, Select, TextInput, passwordPairValid } from '../../components/ui';

export interface UserRow {
  id: string;
  username: string;
  displayName: string;
  role: Role;
  active: boolean;
  language: string;
  zoneIds: string[];
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
    zoneIds: user?.zoneIds ?? [],
    password: '',
    passwordConfirm: '',
  });
  const save = useAction(async () => {
    if (user) {
      await patch(`/api/v1/users/${user.id}`, { displayName: form.displayName, role: form.role, active: form.active, zoneIds: form.zoneIds });
    } else {
      await post('/api/v1/users', { ...form });
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
              <Checkbox
                key={z.id}
                label={z.name}
                checked={form.zoneIds.includes(z.id)}
                onChange={(on) => setForm({ ...form, zoneIds: on ? [...form.zoneIds, z.id] : form.zoneIds.filter((x) => x !== z.id) })}
              />
            ))}
          </div>
        )}
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
                <td className="muted">{u.role === 'user' ? u.zoneIds.map(zoneName).join(', ') || '—' : t('admin.users.allZones')}</td>
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
