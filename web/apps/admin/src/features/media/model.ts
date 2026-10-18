// Media API types, URLs and formatting shared by the media manager, the pickers and the
// storage page. Shapes mirror internal/server/media (assets.go, folders.go, trash.go,
// storage.go); the server is the source of truth.
import { getLanguage, has, t } from '@opensignage/i18n';
import { ApiError, errorText } from '../../lib/api';

export type MediaKind = 'image' | 'video' | 'audio';
export type MediaStatus = 'processing' | 'ready' | 'failed';

/** "root" lists only the top level; omitting the folder means "search everywhere". */
export const ROOT = 'root';

export interface MediaProgress {
  step: string;
  index: number;
  total: number;
  percent: number;
}

export interface MediaMeta {
  mime?: string;
  width?: number;
  height?: number;
  durationMs?: number;
  fps?: number;
  videoCodec?: string;
  audioCodec?: string;
  pixFmt?: string;
  container?: string;
  bitrate?: number;
  hasAudio?: boolean;
  rotation?: number;
  animated?: boolean;
  hasAlpha?: boolean;
}

export interface MediaAsset {
  id: string;
  name: string;
  kind: MediaKind;
  folderId: string | null;
  revision: number;
  /** Newest fully processed revision (0 = none yet); screens show this one. */
  readyRevision?: number;
  status: MediaStatus;
  error?: string;
  meta: MediaMeta;
  size: number;
  mime: string;
  sha256: string;
  variants: string[];
  progress?: MediaProgress;
  createdAt: string;
  updatedAt: string;
  trashedAt?: string;
  /** User id of the uploader (operators limited to zones change only their own files). */
  uploadedBy?: string;
}

export interface MediaFolder {
  id: string;
  parentId: string | null;
  name: string;
  itemCount: number;
  folderCount: number;
  totalSize: number;
  createdAt: string;
  updatedAt: string;
  /** User id of the creator (operators limited to zones change only their own folders). */
  createdBy?: string;
}

export interface Crumb {
  id: string;
  name: string;
}

export interface MediaListing {
  folders: MediaFolder[];
  assets: MediaAsset[];
  breadcrumb: Crumb[];
  total: number;
}

export interface UsageRef {
  ownerType: string;
  ownerId: string;
  ownerName: string;
}

export interface Revision {
  revision: number;
  size: number;
  mime: string;
  sha256: string;
  meta: MediaMeta;
  createdAt: string;
  current: boolean;
}

export interface VariantInfo {
  variant: string;
  revision: number;
  size: number;
  mime: string;
  width: number;
  height: number;
  sha256: string;
}

export interface AssetDetails extends MediaAsset {
  revisions: Revision[];
  variantInfo: VariantInfo[];
  usage: UsageRef[];
  job: {
    id: string;
    state: string;
    revision: number;
    attempts: number;
    error?: string;
    updatedAt: string;
  } | null;
  path: Crumb[];
}

export interface BlockedItem {
  id: string;
  name: string;
  reason: string;
}

export interface TrashItem {
  id: string;
  type: 'asset' | 'folder';
  name: string;
  kind?: MediaKind;
  trashedAt: string;
  ageDays: number;
  size: number;
  itemCount: number;
  folderCount: number;
  originalFolderId: string | null;
  originalPath: Crumb[];
  usage: UsageRef[];
  blocked: boolean;
  batch: string;
}

export interface PurgePlan {
  items: { id: string; type: 'asset' | 'folder'; name: string; count: number }[];
  assets: number;
  folders: number;
  bytes: number;
  blocked: BlockedItem[];
}

export interface CleanupPlan {
  assets: number;
  items: number;
  bytes: number;
  blocked: BlockedItem[];
}

export function listingPath(o: {
  folder?: string;
  q?: string;
  kind?: MediaKind | '';
  limit?: number;
}): string {
  const p = new URLSearchParams();
  const q = o.q?.trim();
  if (q) p.set('q', q);
  else p.set('folder', o.folder || ROOT);
  if (o.kind) p.set('kind', o.kind);
  if (o.limit) p.set('limit', String(o.limit));
  return `/api/v1/media?${p.toString()}`;
}

export function fileUrl(
  id: string,
  variant?: string,
  opts: { rev?: number; download?: boolean } = {},
): string {
  const p = new URLSearchParams();
  if (variant) p.set('variant', variant);
  if (opts.rev) p.set('rev', String(opts.rev));
  if (opts.download) p.set('download', '1');
  const qs = p.toString();
  return `/api/v1/media/${encodeURIComponent(id)}/file${qs ? `?${qs}` : ''}`;
}

/** Fully processed, or a processed earlier revision keeps showing while a replacement runs. */
export function isPickable(a: Pick<MediaAsset, 'status' | 'readyRevision'>): boolean {
  return a.status === 'ready' || (a.readyRevision ?? 0) > 0;
}

/** What content may reference (server rule): anything except a failure without a ready revision. */
export function isUsable(a: Pick<MediaAsset, 'status' | 'readyRevision'>): boolean {
  return a.status !== 'failed' || (a.readyRevision ?? 0) > 0;
}

/** Route of the manager page showing a folder (null/undefined = top level). */
export function folderRoute(folderId: string | null | undefined): string {
  return folderId ? `/media/folder/${encodeURIComponent(folderId)}` : '/media';
}

/** Whether the asset has a variant the browser can play/show right now. */
export function hasPreview(a: Pick<MediaAsset, 'kind' | 'variants' | 'readyRevision'>): boolean {
  if ((a.readyRevision ?? 0) > 0) return true; // the server falls back to the ready revision
  if (a.kind === 'image') return true; // display falls back to the original
  if (a.kind === 'video') return a.variants.includes('v1080') || a.variants.includes('v720');
  return a.variants.includes('a');
}

export const ACCEPT: Record<MediaKind | 'all', string> = {
  image: 'image/*,.svg',
  video: 'video/*',
  audio: 'audio/*',
  all: 'image/*,video/*,audio/*,.svg',
};

// ---- formatting ---------------------------------------------------------------------

const UNITS = ['B', 'KB', 'MB', 'GB', 'TB'];

export function formatBytes(n: number | null | undefined): string {
  let v = Math.max(0, Number(n) || 0);
  let u = 0;
  while (v >= 1024 && u < UNITS.length - 1) {
    v /= 1024;
    u++;
  }
  const digits = u === 0 || v >= 100 ? 0 : 1;
  return `${new Intl.NumberFormat(getLanguage(), { maximumFractionDigits: digits, minimumFractionDigits: 0 }).format(v)} ${UNITS[u]}`;
}

export function formatDuration(ms: number | null | undefined): string {
  const s = Math.round((Number(ms) || 0) / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  return h
    ? `${h}:${String(m).padStart(2, '0')}:${String(sec).padStart(2, '0')}`
    : `${m}:${String(sec).padStart(2, '0')}`;
}

export function formatDate(iso: string | null | undefined): string {
  if (!iso) return '—';
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? '—'
    : d.toLocaleString(getLanguage(), { dateStyle: 'medium', timeStyle: 'short' });
}

export function formatPercent(v: number): string {
  return new Intl.NumberFormat(getLanguage(), { maximumFractionDigits: 1 }).format(v) + ' %';
}

export function pathText(path: Crumb[]): string {
  return [t('media.root'), ...path.map((c) => c.name)].join(' › ');
}

// ---- errors -------------------------------------------------------------------------

const num = (v: unknown): number => (typeof v === 'number' ? v : Number(v) || 0);

/** Translated error text with media-specific parameters formatted (bytes, kinds, names). */
export function mediaErrorText(e: unknown): string {
  if (e instanceof ApiError) {
    const p = e.params;
    switch (e.code) {
      case 'disk_full':
        return t('errors.disk_full', { needed: formatBytes(num(p.needed)), free: formatBytes(num(p.free)) });
      case 'file_too_large':
      case 'chunk_too_large':
      case 'too_many_uploads':
        return t(`errors.${e.code}`, {
          max: e.code === 'too_many_uploads' ? num(p.max) : formatBytes(num(p.max)),
        });
      case 'kind_mismatch':
        return t('errors.kind_mismatch', {
          expected: has(`media.kind.${String(p.expected)}`)
            ? t(`media.kind.${String(p.expected)}`)
            : String(p.expected ?? ''),
        });
      case 'unsupported_type':
        return t('errors.unsupported_type', {
          detected: typeof p.detected === 'string' && p.detected ? p.detected : t('media.unknownType'),
        });
      case 'asset_in_use': {
        const names = Array.isArray(p.blocked)
          ? (p.blocked as BlockedItem[]).map((b) => b.name).filter(Boolean)
          : [];
        return t('errors.asset_in_use', {
          names: names.slice(0, 5).join(', ') + (names.length > 5 ? ' …' : ''),
        });
      }
      case 'font_in_use':
        return t('errors.font_in_use', { family: String(p.family ?? ''), usage: num(p.usage) });
      case 'font_family_reserved':
        return t('errors.font_family_reserved', { family: String(p.family ?? '') });
      case 'upload_offset':
        return t('errors.upload_offset', { received: formatBytes(num(p.received)) });
    }
  }
  return errorText(e);
}

/** Processing failures are stored as "<code>: <text>" on the asset. */
export function failureText(raw: string | undefined): { text: string; detail: string } {
  if (!raw) return { text: t('media.failure.unknown'), detail: '' };
  const i = raw.indexOf(':');
  const code = i > 0 ? raw.slice(0, i).trim() : '';
  const detail = i > 0 ? raw.slice(i + 1).trim() : raw;
  const key = `media.failure.${code}`;
  return has(key) ? { text: t(key), detail } : { text: t('media.failure.unknown'), detail: raw };
}

/** Groups usage references by owner (a draft and a published version count once). */
export function groupUsage(
  usage: UsageRef[],
): { type: 'presentation' | 'playlist' | 'product' | 'other'; id: string; name: string; states: string[] }[] {
  const out = new Map<
    string,
    { type: 'presentation' | 'playlist' | 'product' | 'other'; id: string; name: string; states: string[] }
  >();
  for (const u of usage) {
    const [base = '', state = ''] = u.ownerType.split('_');
    const type = base === 'presentation' || base === 'playlist' || base === 'product' ? base : 'other';
    const key = `${type}:${u.ownerId}`;
    const g = out.get(key) ?? { type, id: u.ownerId, name: u.ownerName, states: [] };
    if (state && !g.states.includes(state)) g.states.push(state);
    if (!g.name && u.ownerName) g.name = u.ownerName;
    out.set(key, g);
  }
  return [...out.values()];
}
