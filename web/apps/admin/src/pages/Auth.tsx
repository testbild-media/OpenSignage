// Login and first-run setup wizard (no default login on the server).
import { type FormEvent, useMemo, useState } from 'react';
import { LANGUAGES } from '@opensignage/i18n';
import { post } from '../lib/api';
import { useAction, useT } from '../lib/hooks';
import { useSession } from '../lib/session';
import { updatedTo } from '../lib/serverWatch';
import { Button, ErrorText, PasswordPair, Select, TextInput, passwordPairValid } from '../components/ui';
import logo from '../../../../../branding/logo_horizontal.svg';

function AuthCard({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="auth-page">
      <div className="auth-card">
        <img src={logo} alt="OpenSignage" className="auth-logo" />
        <h1>{title}</h1>
        {children}
      </div>
    </div>
  );
}

export function LoginPage() {
  const t = useT();
  const { refresh } = useSession();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [updated] = useState(updatedTo);
  const login = useAction(async () => {
    await post('/api/v1/auth/login', { username, password });
    await refresh();
  });
  const submit = (e: FormEvent): void => {
    e.preventDefault();
    void login.run();
  };
  return (
    <AuthCard title={t('admin.login.title')}>
      <form onSubmit={submit} className="form">
        {updated && (
          <p className="auth-notice" role="status">
            {t('common.update.reloaded', { version: updated })}
          </p>
        )}
        <TextInput label={t('admin.login.username')} value={username} autoComplete="username" autoFocus onChange={(e) => setUsername(e.target.value)} />
        <TextInput label={t('admin.login.password')} type="password" value={password} autoComplete="current-password" onChange={(e) => setPassword(e.target.value)} />
        <ErrorText error={login.error} />
        <Button type="submit" variant="primary" busy={login.busy} disabled={!username || !password}>
          {t('admin.login.submit')}
        </Button>
        <a className="auth-link" href="/ca.crt">
          {t('admin.login.certificate')}
        </a>
      </form>
    </AuthCard>
  );
}

function guessTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

function timezones(): string[] {
  const intl = Intl as unknown as { supportedValuesOf?: (k: string) => string[] };
  const list = intl.supportedValuesOf?.('timeZone') ?? [];
  return Array.from(new Set(['UTC', ...list]));
}

export function SetupPage() {
  const t = useT();
  const { refresh } = useSession();
  const zones = useMemo(timezones, []);
  const [form, setForm] = useState({
    serverName: 'OpenSignage',
    username: 'admin',
    displayName: '',
    password: '',
    passwordConfirm: '',
    language: 'en',
    timezone: guessTimezone(),
  });
  const set = (k: keyof typeof form, v: string): void => setForm((f) => ({ ...f, [k]: v }));
  const setup = useAction(async () => {
    await post('/api/v1/setup', { ...form, displayName: form.displayName || form.username });
    await refresh();
  });
  return (
    <AuthCard title={t('admin.setup.title')}>
      <p className="muted">{t('admin.setup.intro')}</p>
      <form
        className="form"
        onSubmit={(e) => {
          e.preventDefault();
          void setup.run();
        }}
      >
        <TextInput label={t('admin.setup.serverName')} value={form.serverName} onChange={(e) => set('serverName', e.target.value)} />
        <Select
          label={t('admin.settings.language')}
          value={form.language}
          onChange={(e) => set('language', e.target.value)}
          options={LANGUAGES.map((l) => ({ value: l.code, label: l.name }))}
        />
        <Select label={t('admin.settings.timezone')} value={form.timezone} onChange={(e) => set('timezone', e.target.value)} options={zones.map((z) => ({ value: z, label: z }))} />
        <TextInput label={t('admin.login.username')} value={form.username} autoComplete="username" onChange={(e) => set('username', e.target.value)} />
        <TextInput label={t('admin.users.displayName')} value={form.displayName} placeholder={form.username} onChange={(e) => set('displayName', e.target.value)} />
        <PasswordPair value={form.password} confirm={form.passwordConfirm} onChange={(v) => set('password', v)} onConfirmChange={(v) => set('passwordConfirm', v)} />
        <ErrorText error={setup.error} />
        <Button type="submit" variant="primary" busy={setup.busy} disabled={!form.username || !passwordPairValid(form.password, form.passwordConfirm)}>
          {t('admin.setup.submit')}
        </Button>
      </form>
    </AuthCard>
  );
}
