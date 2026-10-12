// Fonts: bundled fonts with preview and license, custom fonts
// with upload (license must be confirmed) and deletion (refused while in use).
import { type FormEvent, useEffect, useMemo, useRef, useState } from 'react';
import { Trash2, Upload } from 'lucide-react';
import { Link } from 'react-router';
import type { FontFamilyEntry, FontManifest } from '@opensignage/contracts';
import { customFontCss, installStyle } from '@opensignage/render';
import { ApiError, api, del } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { byFamily, loadFontManifest } from '../../lib/fonts';
import { useConfirm, useToast } from '../../components/feedback';
import {
  Badge,
  Button,
  Checkbox,
  DataState,
  IconButton,
  PageHeader,
  Select,
  TextInput,
} from '../../components/ui';
import { formatBytes, formatDate, mediaErrorText } from '../media/model';

interface FontFace {
  id: string;
  family: string;
  style: 'normal' | 'italic';
  weightMin: number;
  weightMax: number;
  format: string;
  license: string;
  size: number;
  sha256: string;
  uploadedBy: string;
  createdAt: string;
  url: string;
}

/** Content that uses a font: presentation or template (draft/published) or ticker template. */
interface FontUser {
  type: 'presentation' | 'template' | 'ticker';
  id: string;
  name: string;
  states?: string[];
}

interface FontFamily {
  family: string;
  source: 'bundled' | 'custom';
  category?: string;
  license?: string;
  faces?: FontFace[];
  usage: number;
  usedBy?: FontUser[];
}

/** Where a font is used, with links, so it can be changed there (e.g. before deleting it). */
function UsedBy({ users }: { users: FontUser[] }) {
  const t = useT();
  if (users.length === 0) return null;
  const kind = (u: FontUser): string =>
    u.type === 'ticker'
      ? t('media.fonts.ownerTicker')
      : u.type === 'template'
        ? t('media.fonts.ownerTemplate')
        : t('media.usage.presentation');
  return (
    <div className="st-font-usedby">
      <div className="faint">{t('media.fonts.usedIn')}</div>
      <ul className="st-font-users">
        {users.map((u) => (
          <li key={`${u.type}:${u.id}`}>
            <span className="faint">{kind(u)}</span>
            <Link to={u.type === 'ticker' ? '/zones/tickers' : `/presentations/${encodeURIComponent(u.id)}`}>
              {u.name || u.id}
            </Link>
            {(u.states ?? []).map((s) => (
              <Badge key={s}>
                {s === 'published' ? t('media.usage.state.published') : t('media.usage.state.draft')}
              </Badge>
            ))}
          </li>
        ))}
      </ul>
    </div>
  );
}

const MAX_FONT = 20 * 1024 * 1024;
const FONT_EXT = /\.(ttf|otf|woff2?)$/i;
const FAMILY_RE = /^[\p{L}\p{N} \-_.'()&+,]{1,100}$/u;
const CATEGORIES = ['sans', 'serif', 'display', 'condensed', 'script', 'mono'] as const;
const WEIGHTS: [RegExp, number][] = [
  [/hairline|thin/i, 100],
  [/extra-?light|ultra-?light/i, 200],
  [/light/i, 300],
  [/semi-?bold|demi-?bold/i, 600],
  [/extra-?bold|ultra-?bold/i, 800],
  [/black|heavy/i, 900],
  [/bold/i, 700],
  [/medium/i, 500],
];

/** Suggests family, style and weight from a file name like "OpenSans-SemiBoldItalic.ttf". */
export function guessFromFilename(name: string): {
  family: string;
  style: 'normal' | 'italic';
  weightMin: number;
  weightMax: number;
} {
  const base = name.replace(/\.[^.]+$/, '');
  const [fam = base, rest = ''] = base.split(/[-_]/, 2);
  const family = fam
    .replace(/([a-z])([A-Z])/g, '$1 $2')
    .replace(/[^\p{L}\p{N} \-_.'()&+,]/gu, '')
    .trim();
  const style = /italic|oblique/i.test(rest) ? 'italic' : 'normal';
  if (/variable|\bvf\b|\[wght/i.test(base)) return { family, style, weightMin: 100, weightMax: 900 };
  const w = WEIGHTS.find(([re]) => re.test(rest))?.[1] ?? 400;
  return { family, style, weightMin: w, weightMax: w };
}

function useManifest(): { data: FontManifest | null; loading: boolean } {
  const [state, setState] = useState<{ data: FontManifest | null; loading: boolean }>({
    data: null,
    loading: true,
  });
  useEffect(() => {
    let alive = true;
    void loadFontManifest().then((m) => alive && setState({ data: m, loading: false }));
    return () => {
      alive = false;
    };
  }, []);
  return state;
}

function weightText(min: number, max: number): string {
  return min === max ? String(min) : `${min}–${max}`;
}

function UploadForm({ reserved, onDone }: { reserved: string[]; onDone: () => void }) {
  const t = useT();
  const toast = useToast();
  const input = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | null>(null);
  const [family, setFamily] = useState('');
  const [style, setStyle] = useState<'normal' | 'italic'>('normal');
  const [wMin, setWMin] = useState('400');
  const [wMax, setWMax] = useState('400');
  const [license, setLicense] = useState('');
  const [confirmed, setConfirmed] = useState(false);
  const min = Number(wMin);
  const max = Number(wMax);
  const fileError = file
    ? !FONT_EXT.test(file.name)
      ? t('media.fonts.badType')
      : file.size > MAX_FONT
        ? t('errors.file_too_large', { max: formatBytes(MAX_FONT) })
        : null
    : null;
  const familyError = !family
    ? null
    : !FAMILY_RE.test(family) || family.trim() !== family
      ? t('media.fonts.familyRule')
      : reserved.some((r) => r.toLowerCase() === family.toLowerCase())
        ? t('errors.font_family_reserved', { family })
        : null;
  const weightError =
    !Number.isInteger(min) || !Number.isInteger(max) || min < 1 || max > 1000 || max < min
      ? t('media.fonts.weightRule')
      : null;
  const valid =
    !!file &&
    !fileError &&
    !!family &&
    !familyError &&
    !weightError &&
    !!license.trim() &&
    [...license.trim()].length <= 200 &&
    confirmed;
  const send = useAction(async () => {
    if (!file) return;
    const fd = new FormData();
    fd.append('family', family);
    fd.append('style', style);
    fd.append('weightMin', String(min));
    fd.append('weightMax', String(max));
    fd.append('license', license.trim());
    fd.append('licenseConfirmed', 'true');
    fd.append('file', file, file.name);
    await api('/api/v1/fonts', { method: 'POST', body: fd });
    toast(t('media.fonts.added', { family }));
    setFile(null);
    setFamily('');
    setLicense('');
    setConfirmed(false);
    setStyle('normal');
    setWMin('400');
    setWMax('400');
    onDone();
  });
  const pick = (f: File | undefined): void => {
    send.clearError();
    setFile(f ?? null);
    if (!f) return;
    const g = guessFromFilename(f.name);
    setFamily(g.family);
    setStyle(g.style);
    setWMin(String(g.weightMin));
    setWMax(String(g.weightMax));
  };
  const submit = (e: FormEvent): void => {
    e.preventDefault();
    if (valid) void send.run();
  };
  return (
    <form className="card form" onSubmit={submit}>
      <h2>{t('media.fonts.uploadTitle')}</h2>
      <p className="muted" style={{ margin: 0 }}>
        {t('media.fonts.uploadText')}
      </p>
      <div className="field">
        <label>{t('media.fonts.file')}</label>
        <div className="mm-inline-status">
          <input
            ref={input}
            type="file"
            hidden
            accept=".ttf,.otf,.woff,.woff2,font/ttf,font/otf,font/woff,font/woff2"
            onChange={(e) => {
              pick(e.target.files?.[0]);
              e.target.value = '';
            }}
          />
          <Button icon={<Upload size={16} />} onClick={() => input.current?.click()}>
            {file ? t('media.fonts.otherFile') : t('media.fonts.chooseFile')}
          </Button>
          {file && (
            <span>
              {file.name} <span className="faint">· {formatBytes(file.size)}</span>
            </span>
          )}
        </div>
        {fileError ? (
          <div className="field-error">{fileError}</div>
        ) : (
          <div className="field-hint">{t('media.fonts.fileHint', { max: formatBytes(MAX_FONT) })}</div>
        )}
      </div>
      <TextInput
        label={t('media.fonts.family')}
        value={family}
        maxLength={100}
        onChange={(e) => setFamily(e.target.value)}
        hint={t('media.fonts.familyHint')}
        error={familyError}
      />
      <div className="st-font-row">
        <Select
          label={t('media.fonts.style')}
          value={style}
          onChange={(e) => setStyle(e.target.value as 'normal' | 'italic')}
          options={[
            { value: 'normal', label: t('media.fonts.styleNormal') },
            { value: 'italic', label: t('media.fonts.styleItalic') },
          ]}
        />
        <TextInput
          label={t('media.fonts.weightMin')}
          type="number"
          min={1}
          max={1000}
          step={1}
          value={wMin}
          onChange={(e) => setWMin(e.target.value)}
        />
        <TextInput
          label={t('media.fonts.weightMax')}
          type="number"
          min={1}
          max={1000}
          step={1}
          value={wMax}
          onChange={(e) => setWMax(e.target.value)}
          error={weightError}
        />
      </div>
      <div className="field-hint" style={{ marginTop: -8 }}>
        {t('media.fonts.weightHint')}
      </div>
      <TextInput
        label={t('media.fonts.license')}
        value={license}
        maxLength={200}
        onChange={(e) => setLicense(e.target.value)}
        hint={t('media.fonts.licenseHint')}
      />
      <Checkbox label={t('media.fonts.licenseConfirm')} checked={confirmed} onChange={setConfirmed} />
      {!!send.error && (
        <div className="form-error" role="alert">
          {mediaErrorText(send.error)}
        </div>
      )}
      <div className="form-actions">
        <Button
          type="submit"
          variant="primary"
          icon={<Upload size={16} />}
          busy={send.busy}
          disabled={!valid}
        >
          {t('media.fonts.upload')}
        </Button>
      </div>
    </form>
  );
}

export function FontsPage() {
  const t = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const manifest = useManifest();
  const fonts = useQuery<{ fonts: FontFamily[] }>('/api/v1/fonts');
  useEvents(['content'], (e) => {
    if (e.topic === '*' || e.kind?.startsWith('font.')) fonts.reload();
  });
  const [sample, setSample] = useState('');
  const [category, setCategory] = useState<'all' | (typeof CATEGORIES)[number]>('all');
  const [search, setSearch] = useState('');
  const text = sample.trim() || t('media.fonts.sample');

  const custom = useMemo(
    () =>
      (fonts.data?.fonts ?? [])
        .filter((f) => f.source === 'custom')
        .sort((a, b) => byFamily(a.family, b.family)),
    [fonts.data],
  );
  const usage = useMemo(
    () => new Map((fonts.data?.fonts ?? []).map((f) => [f.family.toLowerCase(), f.usedBy ?? []])),
    [fonts.data],
  );
  const bundled: FontFamilyEntry[] = manifest.data?.families ?? [];
  const shown = bundled
    .filter(
      (f) =>
        (category === 'all' || f.category === category) &&
        (!search.trim() || f.family.toLowerCase().includes(search.trim().toLowerCase())),
    )
    .sort((a, b) => byFamily(a.family, b.family));

  // Custom faces are previewed in their own font.
  useEffect(() => {
    installStyle(
      'os-admin-custom-fonts',
      customFontCss(
        custom.map((f) => ({
          family: f.family,
          files: (f.faces ?? []).map((x) => ({
            url: x.url,
            sha256: x.sha256,
            size: x.size,
            weight: [x.weightMin, x.weightMax] as [number, number],
            style: x.style,
            format: x.format,
          })),
        })),
        (x) => x.url,
      ),
    );
  }, [custom]);

  const remove = useAction(async (face: FontFace) => {
    const ok = await confirm({
      title: t('media.fonts.deleteTitle'),
      message: t('media.fonts.deleteConfirm', {
        family: face.family,
        style: t(face.style === 'italic' ? 'media.fonts.styleItalic' : 'media.fonts.styleNormal'),
        weight: weightText(face.weightMin, face.weightMax),
      }),
      confirmLabel: t('common.action.delete'),
      danger: true,
    });
    if (!ok) return;
    await del(`/api/v1/fonts/${encodeURIComponent(face.id)}`);
    toast(t('media.fonts.deleted', { family: face.family }));
    fonts.reload();
  });

  return (
    <div className="page">
      <PageHeader title={t('admin.settings.fonts')} subtitle={t('media.fonts.subtitle')} />
      <UploadForm reserved={bundled.map((f) => f.family)} onDone={fonts.reload} />

      <div className="card">
        <h2>{t('media.fonts.custom')}</h2>
        {!!remove.error && (
          <div className="form-error" role="alert" style={{ marginBottom: 12 }}>
            {mediaErrorText(remove.error)}
            {remove.error instanceof ApiError && Array.isArray(remove.error.params.usedBy) && (
              <UsedBy users={remove.error.params.usedBy as FontUser[]} />
            )}
          </div>
        )}
        <DataState
          loading={fonts.loading && !fonts.data}
          error={fonts.error}
          onRetry={fonts.reload}
          empty={!!fonts.data && custom.length === 0}
          emptyText={t('media.fonts.noCustom')}
        >
          <div className="st-font-list">
            {custom.map((f) => (
              <div key={f.family} className="st-font-family">
                <div className="st-font-head">
                  <strong>{f.family}</strong>
                  {f.usage > 0 ? (
                    <Badge tone="primary">{t('media.fonts.used', { count: f.usage })}</Badge>
                  ) : (
                    <Badge>{t('media.fonts.unused')}</Badge>
                  )}
                </div>
                <div className="st-font-sample" style={{ fontFamily: `"${f.family}", var(--os-font)` }}>
                  {text}
                </div>
                <UsedBy users={f.usedBy ?? []} />
                <table className="mm-mini-table">
                  <thead>
                    <tr>
                      <th>{t('media.fonts.style')}</th>
                      <th>{t('media.fonts.weight')}</th>
                      <th>{t('media.fonts.format')}</th>
                      <th>{t('media.details.size')}</th>
                      <th>{t('media.fonts.license')}</th>
                      <th>{t('media.fonts.uploadedBy')}</th>
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {(f.faces ?? []).map((x) => (
                      <tr key={x.id}>
                        <td
                          style={{ fontFamily: `"${f.family}"`, fontStyle: x.style, fontWeight: x.weightMin }}
                        >
                          {t(x.style === 'italic' ? 'media.fonts.styleItalic' : 'media.fonts.styleNormal')}
                        </td>
                        <td>{weightText(x.weightMin, x.weightMax)}</td>
                        <td className="mono">{x.format}</td>
                        <td className="nowrap">{formatBytes(x.size)}</td>
                        <td>{x.license}</td>
                        <td>
                          {x.uploadedBy} <span className="faint">· {formatDate(x.createdAt)}</span>
                        </td>
                        <td className="row-actions">
                          <IconButton
                            label={t('common.action.delete')}
                            disabled={remove.busy}
                            onClick={() => void remove.run(x)}
                          >
                            <Trash2 size={15} />
                          </IconButton>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ))}
          </div>
        </DataState>
      </div>

      <div className="card">
        <h2>{t('media.fonts.bundled')}</h2>
        <p className="muted" style={{ marginTop: 0 }}>
          {t('media.fonts.bundledText')}
        </p>
        <div className="filters">
          <TextInput
            label={t('media.fonts.sampleLabel')}
            value={sample}
            placeholder={t('media.fonts.sample')}
            onChange={(e) => setSample(e.target.value)}
          />
          <TextInput
            label={t('media.fonts.search')}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
          <div className="segmented" role="group" aria-label={t('media.fonts.category')}>
            {(['all', ...CATEGORIES] as const).map((c) => (
              <button
                key={c}
                type="button"
                className={category === c ? 'active' : ''}
                aria-pressed={category === c}
                onClick={() => setCategory(c)}
              >
                {t(`media.fonts.cat.${c}`)}
              </button>
            ))}
          </div>
        </div>
        <DataState
          loading={manifest.loading}
          error={!manifest.loading && !manifest.data ? new Error(t('media.fonts.manifestError')) : null}
          empty={shown.length === 0}
          emptyText={t('media.fonts.noMatch')}
        >
          <table className="table">
            <thead>
              <tr>
                <th>{t('media.fonts.family')}</th>
                <th>{t('media.fonts.preview')}</th>
                <th>{t('media.fonts.weight')}</th>
                <th>{t('media.fonts.scripts')}</th>
                <th>{t('media.fonts.license')}</th>
                <th>{t('media.fonts.usage')}</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((f) => {
                const ws = f.files.flatMap((x) => x.weight);
                const italic = f.files.some((x) => x.style === 'italic');
                const users = usage.get(f.family.toLowerCase()) ?? [];
                const licenseFile = (f as FontFamilyEntry & { licenseFile?: string }).licenseFile;
                return (
                  <tr key={f.id}>
                    <td>
                      <div>{f.family}</div>
                      <div className="faint">{t(`media.fonts.cat.${f.category}`)}</div>
                    </td>
                    <td className="st-font-sample" style={{ fontFamily: `"${f.family}", var(--os-font)` }}>
                      {text}
                    </td>
                    <td className="nowrap">
                      {weightText(Math.min(...ws), Math.max(...ws))}
                      {italic && <div className="faint">{t('media.fonts.withItalic')}</div>}
                    </td>
                    <td className="faint">{(f.scripts ?? []).join(', ')}</td>
                    <td className="nowrap">
                      {licenseFile ? (
                        <a
                          href={`/fonts/${licenseFile.split('/').map(encodeURIComponent).join('/')}`}
                          target="_blank"
                          rel="noreferrer"
                        >
                          {f.license}
                        </a>
                      ) : (
                        f.license
                      )}
                    </td>
                    <td>
                      {users.length > 0 ? (
                        <span title={users.map((u) => u.name || u.id).join(', ')}>
                          <Badge tone="primary">{users.length}</Badge>
                        </span>
                      ) : (
                        <span className="faint">—</span>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </DataState>
      </div>
    </div>
  );
}
