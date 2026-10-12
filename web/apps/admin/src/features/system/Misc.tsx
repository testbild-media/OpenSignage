import { useState } from 'react';
import { Link } from 'react-router';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useSession } from '../../lib/session';
import { changeOwnPassword, languageOptions, saveOwnLanguage } from '../../lib/account';
import { useEvents } from '../../lib/events';
import { useToast } from '../../components/feedback';
import { Button, DataState, ErrorText, PageHeader, PasswordPair, Select, TextInput, passwordPairValid } from '../../components/ui';
import { AlertList, useAlertsQuery } from '../devices/Alerts';
import { openAlerts } from '../devices/model';
import '../devices/devices.css';

/** Own password and personal language (applied on save, not on selection). */
export function ProfilePage() {
  const t = useT();
  const toast = useToast();
  const { user, refresh } = useSession();
  const [current, setCurrent] = useState('');
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const [lang, setLang] = useState(user?.language || '');
  const savePw = useAction(async () => {
    await changeOwnPassword(current, pw, pw2);
    setCurrent('');
    setPw('');
    setPw2('');
    toast(t('admin.profile.passwordChanged'));
  });
  const saveLang = useAction(async () => {
    await saveOwnLanguage(lang, refresh);
    toast(t('admin.saved'));
  });
  return (
    <div className="page page-narrow">
      <PageHeader title={t('admin.profile.title')} subtitle={`${user?.displayName} · ${t(`admin.role.${user?.role ?? 'user'}`)}`} />
      <div className="card form">
        <h2>{t('admin.settings.language')}</h2>
        <Select
          label={t('admin.settings.language')}
          value={lang}
          onChange={(e) => setLang(e.target.value)}
          options={languageOptions(t('admin.profile.serverDefault'))}
        />
        <ErrorText error={saveLang.error} />
        <div className="form-actions">
          <Button variant="primary" busy={saveLang.busy} onClick={() => void saveLang.run()}>
            {t('common.action.save')}
          </Button>
        </div>
      </div>
      <div className="card form">
        <h2>{t('admin.profile.password')}</h2>
        <TextInput type="password" label={t('admin.profile.current')} value={current} autoComplete="current-password" onChange={(e) => setCurrent(e.target.value)} />
        <PasswordPair value={pw} confirm={pw2} onChange={setPw} onConfirmChange={setPw2} />
        <ErrorText error={savePw.error} />
        <div className="form-actions">
          <Button variant="primary" busy={savePw.busy} disabled={!current || !passwordPairValid(pw, pw2)} onClick={() => void savePw.run()}>
            {t('admin.profile.changePassword')}
          </Button>
        </div>
      </div>
    </div>
  );
}

interface AuditEntry {
  id: number;
  at: string;
  username: string;
  action: string;
  objectType: string;
  objectName: string;
  zoneId: string;
  old: string | null;
  new: string | null;
  ip: string;
}

export function AuditPage() {
  const t = useT();
  const [filter, setFilter] = useState({ user: '', action: '', objectType: '' });
  const [applied, setApplied] = useState(filter);
  const params = new URLSearchParams(Object.entries(applied).filter(([, v]) => v) as [string, string][]);
  const q = useQuery<{ entries: AuditEntry[] }>(`/api/v1/audit?${params.toString()}`);
  const [open, setOpen] = useState<number | null>(null);
  return (
    <div className="page">
      <PageHeader title={t('admin.audit.title')} />
      <div className="filters">
        <TextInput label={t('admin.audit.user')} value={filter.user} onChange={(e) => setFilter({ ...filter, user: e.target.value })} />
        <TextInput label={t('admin.audit.action')} value={filter.action} onChange={(e) => setFilter({ ...filter, action: e.target.value })} placeholder="product." />
        <TextInput label={t('admin.audit.objectType')} value={filter.objectType} onChange={(e) => setFilter({ ...filter, objectType: e.target.value })} />
        <Button onClick={() => setApplied(filter)}>{t('admin.audit.apply')}</Button>
      </div>
      <DataState loading={q.loading && !q.data} error={q.error} onRetry={q.reload} empty={q.data?.entries.length === 0}>
        <table className="table">
          <thead>
            <tr>
              <th>{t('admin.audit.time')}</th>
              <th>{t('admin.audit.user')}</th>
              <th>{t('admin.audit.action')}</th>
              <th>{t('admin.audit.object')}</th>
              <th>IP</th>
            </tr>
          </thead>
          <tbody>
            {q.data?.entries.map((e) => (
              <tr key={e.id} onClick={() => setOpen(open === e.id ? null : e.id)} className="clickable">
                <td className="nowrap">{new Date(e.at).toLocaleString()}</td>
                <td>{e.username}</td>
                <td className="mono">{e.action}</td>
                <td>
                  {e.objectType} {e.objectName && <span className="muted">· {e.objectName}</span>}
                  {open === e.id && (e.old || e.new) && (
                    <div className="audit-diff">
                      {e.old && <pre className="mono">- {e.old}</pre>}
                      {e.new && <pre className="mono">+ {e.new}</pre>}
                    </div>
                  )}
                </td>
                <td className="muted">{e.ip}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </DataState>
    </div>
  );
}

interface DeviceSummary {
  devices?: { id: string; online: boolean }[];
}

/** Start page: key figures and quick links (only from data the server really has). */
export function DashboardPage() {
  const t = useT();
  const { server, user } = useSession();
  const devices = useQuery<DeviceSummary>('/api/v1/devices');
  const alerts = useAlertsQuery();
  useEvents(['devices'], () => devices.reload());
  const list = devices.data?.devices ?? [];
  const online = list.filter((d) => d.online).length;
  const problems = openAlerts(alerts.data?.alerts); // acknowledged ones are on the alerts page
  return (
    <div className="page">
      <PageHeader title={t('admin.dashboard.title')} subtitle={t('admin.dashboard.welcome', { name: user?.displayName ?? '' })} />
      <div className="kpis">
        <div className="kpi">
          <div className="kpi-value">{devices.data ? `${online} / ${list.length}` : '—'}</div>
          <div className="kpi-label">{t('admin.dashboard.devicesOnline')}</div>
        </div>
        <Link to="/alerts" className={`kpi ${alerts.data?.count ? 'kpi-danger' : ''}`} title={t('devices.alerts.open')}>
          <div className="kpi-value">{alerts.data?.count ?? '—'}</div>
          <div className="kpi-label">{t('admin.dashboard.alerts')}</div>
        </Link>
        <div className="kpi">
          <div className="kpi-value">{server?.version}</div>
          <div className="kpi-label">{t('admin.dashboard.version')}</div>
        </div>
        <div className="kpi">
          <div className="kpi-value">{server?.timezone}</div>
          <div className="kpi-label">{t('admin.settings.timezone')}</div>
        </div>
      </div>
      {problems.length > 0 && (
        <div className="card dash-alerts">
          <h2>{t('admin.dashboard.alerts')}</h2>
          <AlertList alerts={problems} onChanged={alerts.reload} />
        </div>
      )}
      <DataState loading={false} error={null}>
        <div className="quick-links">
          <Link className="btn btn-secondary" to="/devices">
            {t('admin.nav.devices')}
          </Link>
          <Link className="btn btn-secondary" to="/presentations">
            {t('admin.nav.presentations')}
          </Link>
          <Link className="btn btn-secondary" to="/products">
            {t('admin.nav.products')}
          </Link>
          <a className="btn btn-primary" href="/control">
            {t('admin.nav.control')}
          </a>
        </div>
      </DataState>
    </div>
  );
}
