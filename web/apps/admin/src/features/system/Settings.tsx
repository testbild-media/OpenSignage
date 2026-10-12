import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router';
import { LANGUAGES } from '@opensignage/i18n';
import { del, patch, post } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useToast } from '../../components/feedback';
import { storedLanguage, useSession } from '../../lib/session';
import { Badge, Button, Checkbox, DataState, ErrorText, PageHeader, PasswordPair, Select, TextInput, passwordPairValid } from '../../components/ui';

export interface Settings {
  serverName: string;
  language: string;
  timezone: string;
  currency: string;
  defaults: { mode: 'duration' | 'repeat'; durationMs: number; repeat: number; fit: 'contain' | 'cover' | 'fill'; slideMs: number };
  trashRetentionDays: number;
  deviceDefaults: { scaleMode: 'contain' | 'cover' | 'none' };
  autoAlignVersions: boolean;
  updateCheck: boolean;
  updateRepo: string;
  clientPasswordSet: boolean;
  defaultUpdateRepo: string;
}

function timezones(): string[] {
  const intl = Intl as unknown as { supportedValuesOf?: (k: string) => string[] };
  return Array.from(new Set(['UTC', ...(intl.supportedValuesOf?.('timeZone') ?? [])]));
}

function useSettingsForm() {
  const q = useQuery<Settings>('/api/v1/settings');
  const [form, setForm] = useState<Settings | null>(null);
  useEffect(() => {
    if (q.data) setForm(q.data);
  }, [q.data]);
  return { q, form, setForm };
}

/** Allgemein: name, language (applies on save only), time zone, currency. */
export function GeneralSettingsPage() {
  const t = useT();
  const toast = useToast();
  const zones = useMemo(timezones, []);
  const { q, form, setForm } = useSettingsForm();
  const { refresh, user } = useSession();
  // An own language (account or this browser) takes precedence over the server default:
  // say so, otherwise changing the default seems to do nothing for this user.
  const own = storedLanguage() || user?.language || '';
  const ownName = LANGUAGES.find((l) => l.code === own)?.name;
  const save = useAction(async () => {
    if (!form) return;
    const next = await patch<Settings>('/api/v1/settings', { serverName: form.serverName, language: form.language, timezone: form.timezone, currency: form.currency });
    q.setData(next);
    await refresh();
    toast(t('admin.saved'));
  });
  return (
    <div className="page page-narrow">
      <PageHeader title={t('admin.settings.general')} />
      <DataState loading={!form} error={q.error} onRetry={q.reload}>
        {form && (
          <div className="form card">
            <TextInput label={t('admin.setup.serverName')} value={form.serverName} onChange={(e) => setForm({ ...form, serverName: e.target.value })} />
            <Select
              label={t('admin.settings.language')}
              hint={t('admin.settings.languageHint')}
              value={form.language}
              onChange={(e) => setForm({ ...form, language: e.target.value })}
              options={LANGUAGES.map((l) => ({ value: l.code, label: l.name }))}
            />
            {ownName && (
              <div className="field-hint">
                {t('admin.settings.ownLanguage', { language: ownName })}{' '}
                <Link to="/profile">{t('admin.profile.title')}</Link>
              </div>
            )}
            <Select label={t('admin.settings.timezone')} hint={t('admin.settings.timezoneHint')} value={form.timezone} onChange={(e) => setForm({ ...form, timezone: e.target.value })} options={zones.map((z) => ({ value: z, label: z }))} />
            <TextInput label={t('admin.settings.currency')} value={form.currency} maxLength={3} onChange={(e) => setForm({ ...form, currency: e.target.value.toUpperCase() })} />
            <ErrorText error={save.error} />
            <div className="form-actions">
              <Button variant="primary" busy={save.busy} onClick={() => void save.run()}>
                {t('common.action.save')}
              </Button>
            </div>
          </div>
        )}
      </DataState>
      <NetworkInfo />
    </div>
  );
}

interface CertInfo {
  source: string;
  subject: string;
  issuer: string;
  notAfter: string;
  dnsNames: string[];
  ips: string[];
  fingerprint: string;
}

interface Network {
  version: string;
  addresses: string[];
  ports: Record<string, number>;
  webCertificate?: CertInfo;
  deviceChannel: CertInfo;
  localCA: CertInfo;
  dataDir: string;
  configFile: string;
}

function NetworkInfo() {
  const t = useT();
  const q = useQuery<Network>('/api/v1/system/network');
  const n = q.data;
  return (
    <div className="card">
      <h2>{t('admin.settings.network')}</h2>
      <DataState loading={q.loading && !n} error={q.error} onRetry={q.reload}>
        {n && (
          <dl className="kv">
            <dt>{t('admin.settings.addresses')}</dt>
            <dd>{n.addresses.map((a) => `https://${a}`).join(' · ') || '—'}</dd>
            <dt>{t('admin.settings.ports')}</dt>
            <dd>
              {t('admin.settings.portsValue', { http: n.ports.http ?? 80, https: n.ports.https ?? 443, device: n.ports.device ?? 7443, discovery: n.ports.discovery ?? 7444 })}
              <div className="faint">{t('admin.settings.restartOnly')}</div>
            </dd>
            <dt>{t('admin.settings.deviceKey')}</dt>
            <dd className="mono">{n.deviceChannel.fingerprint.slice(0, 4).toUpperCase()}-{n.deviceChannel.fingerprint.slice(4, 8).toUpperCase()}</dd>
            <dt>{t('admin.settings.dataDir')}</dt>
            <dd className="mono">{n.dataDir}</dd>
          </dl>
        )}
      </DataState>
    </div>
  );
}

/** Zertifikat: status, upload own certificate, download local CA. */
export function CertificatePage() {
  const t = useT();
  const toast = useToast();
  const q = useQuery<Network>('/api/v1/system/network');
  const [cert, setCert] = useState('');
  const [key, setKey] = useState('');
  const upload = useAction(async () => {
    await post('/api/v1/system/certificate', { certificate: cert, key });
    setCert('');
    setKey('');
    toast(t('admin.cert.uploaded'));
    q.reload();
  });
  const remove = useAction(async () => {
    await del('/api/v1/system/certificate');
    toast(t('admin.cert.removed'));
    q.reload();
  });
  const wc = q.data?.webCertificate;
  return (
    <div className="page page-narrow">
      <PageHeader title={t('admin.cert.title')} />
      <DataState loading={q.loading && !q.data} error={q.error} onRetry={q.reload}>
        {wc && (
          <div className="card">
            <h2>{t('admin.cert.current')}</h2>
            <dl className="kv">
              <dt>{t('admin.cert.source')}</dt>
              <dd>{wc.source === 'uploaded' ? <Badge tone="primary">{t('admin.cert.uploadedSource')}</Badge> : <Badge>{t('admin.cert.localCa')}</Badge>}</dd>
              <dt>{t('admin.cert.validUntil')}</dt>
              <dd>{new Date(wc.notAfter).toLocaleDateString()}</dd>
              <dt>{t('admin.cert.names')}</dt>
              <dd className="mono">{[...wc.dnsNames, ...wc.ips].join(', ')}</dd>
            </dl>
            {wc.source === 'uploaded' && (
              <Button variant="danger" busy={remove.busy} onClick={() => void remove.run()}>
                {t('admin.cert.useLocal')}
              </Button>
            )}
            <ErrorText error={remove.error} />
          </div>
        )}
      </DataState>
      <div className="card">
        <h2>{t('admin.cert.trustTitle')}</h2>
        <p className="muted">{t('admin.cert.trustText')}</p>
        <ol className="steps">
          <li>{t('admin.cert.trustIos')}</li>
          <li>{t('admin.cert.trustAndroid')}</li>
          <li>{t('admin.cert.trustDesktop')}</li>
        </ol>
        <a className="btn btn-secondary" href="/ca.crt" download>
          {t('admin.cert.downloadCa')}
        </a>
      </div>
      <div className="card form">
        <h2>{t('admin.cert.uploadTitle')}</h2>
        <p className="muted">{t('admin.cert.uploadText')}</p>
        <label className="field">
          <span>{t('admin.cert.pemCert')}</span>
          <textarea className="input mono" rows={6} value={cert} onChange={(e) => setCert(e.target.value)} placeholder="-----BEGIN CERTIFICATE-----" />
        </label>
        <label className="field">
          <span>{t('admin.cert.pemKey')}</span>
          <textarea className="input mono" rows={6} value={key} onChange={(e) => setKey(e.target.value)} placeholder="-----BEGIN PRIVATE KEY-----" />
        </label>
        <ErrorText error={upload.error} />
        <div className="form-actions">
          <Button variant="primary" busy={upload.busy} disabled={!cert.includes('BEGIN') || !key.includes('BEGIN')} onClick={() => void upload.run()}>
            {t('admin.cert.upload')}
          </Button>
        </div>
      </div>
    </div>
  );
}

/** Standardwerte for new playlist entries and slides, plus device defaults. */
export function DefaultsPage() {
  const t = useT();
  const toast = useToast();
  const { q, form, setForm } = useSettingsForm();
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const save = useAction(async () => {
    if (!form) return;
    q.setData(await patch<Settings>('/api/v1/settings', { defaults: form.defaults, deviceDefaults: form.deviceDefaults, trashRetentionDays: form.trashRetentionDays, autoAlignVersions: form.autoAlignVersions }));
    toast(t('admin.saved'));
  });
  const setClientPw = useAction(async (clear: boolean) => {
    q.setData(await post<Settings>('/api/v1/settings/client-password', clear ? { clear: true } : { password: pw, passwordConfirm: pw2 }));
    setPw('');
    setPw2('');
    toast(t('admin.saved'));
  });
  const sec = (ms: number): number => Math.round(ms / 1000);
  return (
    <div className="page page-narrow">
      <PageHeader title={t('admin.settings.defaults')} />
      <DataState loading={!form} error={q.error} onRetry={q.reload}>
        {form && (
          <>
            <div className="card form">
              <h2>{t('admin.defaults.playlist')}</h2>
              <Select
                label={t('admin.defaults.mode')}
                value={form.defaults.mode}
                onChange={(e) => setForm({ ...form, defaults: { ...form.defaults, mode: e.target.value as 'duration' | 'repeat' } })}
                options={[
                  { value: 'duration', label: t('admin.defaults.modeDuration') },
                  { value: 'repeat', label: t('admin.defaults.modeRepeat') },
                ]}
              />
              <TextInput label={t('admin.defaults.durationSec')} type="number" min={1} value={sec(form.defaults.durationMs)} onChange={(e) => setForm({ ...form, defaults: { ...form.defaults, durationMs: Number(e.target.value) * 1000 } })} />
              <TextInput label={t('admin.defaults.repeat')} type="number" min={1} value={form.defaults.repeat} onChange={(e) => setForm({ ...form, defaults: { ...form.defaults, repeat: Number(e.target.value) } })} />
              <Select
                label={t('admin.defaults.fit')}
                value={form.defaults.fit}
                onChange={(e) => setForm({ ...form, defaults: { ...form.defaults, fit: e.target.value as Settings['defaults']['fit'] } })}
                options={['contain', 'cover', 'fill'].map((f) => ({ value: f, label: t(`admin.fit.${f}`) }))}
              />
              <TextInput label={t('admin.defaults.slideSec')} type="number" min={1} value={sec(form.defaults.slideMs)} onChange={(e) => setForm({ ...form, defaults: { ...form.defaults, slideMs: Number(e.target.value) * 1000 } })} />
            </div>
            <div className="card form">
              <h2>{t('admin.defaults.devices')}</h2>
              <Select
                label={t('admin.defaults.scaleMode')}
                value={form.deviceDefaults.scaleMode}
                onChange={(e) => setForm({ ...form, deviceDefaults: { scaleMode: e.target.value as Settings['deviceDefaults']['scaleMode'] } })}
                options={['contain', 'cover', 'none'].map((f) => ({ value: f, label: t(`admin.scale.${f}`) }))}
              />
              <Checkbox label={t('admin.defaults.autoAlign')} checked={form.autoAlignVersions} onChange={(v) => setForm({ ...form, autoAlignVersions: v })} />
              <Select
                label={t('admin.defaults.trash')}
                value={String(form.trashRetentionDays)}
                onChange={(e) => setForm({ ...form, trashRetentionDays: Number(e.target.value) })}
                options={[7, 30, 60, 90, 0].map((d) => ({ value: String(d), label: d === 0 ? t('admin.defaults.trashNever') : t('admin.defaults.trashDays', { days: d }) }))}
              />
              <ErrorText error={save.error} />
              <div className="form-actions">
                <Button variant="primary" busy={save.busy} onClick={() => void save.run()}>
                  {t('common.action.save')}
                </Button>
              </div>
            </div>
            <div className="card form">
              <h2>{t('admin.defaults.clientPassword')}</h2>
              <p className="muted">{form.clientPasswordSet ? t('admin.defaults.clientPasswordSet') : t('admin.defaults.clientPasswordLocal')}</p>
              <PasswordPair value={pw} confirm={pw2} onChange={setPw} onConfirmChange={setPw2} />
              <ErrorText error={setClientPw.error} />
              <div className="form-actions">
                {form.clientPasswordSet && (
                  <Button busy={setClientPw.busy} onClick={() => void setClientPw.run(true)}>
                    {t('admin.defaults.clientPasswordClear')}
                  </Button>
                )}
                <Button variant="primary" busy={setClientPw.busy} disabled={!passwordPairValid(pw, pw2)} onClick={() => void setClientPw.run(false)}>
                  {t('admin.defaults.clientPasswordSave')}
                </Button>
              </div>
            </div>
          </>
        )}
      </DataState>
    </div>
  );
}
