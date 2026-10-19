// Device model of the admin WebGUI: API types and pure helpers (formatting of the
// monitoring values, the display mode picker, storage levels and alert grouping).
import { getLanguage } from '@opensignage/i18n';
import type { DeviceStatus, DisplayConfig, DisplayModeGroup, ScaleMode } from '@opensignage/contracts';

export interface DeviceConfig {
  scaleMode: ScaleMode;
  blackout: boolean;
  display: DisplayConfig;
  perf: string;
  hwVideo: string;
  plasmaShift: boolean;
  showIpOverlay: boolean;
}

/** Server devices/monitor.go Alert* kinds. */
export type AlertKind =
  | 'device_offline'
  | 'player_crashed'
  | 'sync_failed'
  | 'version_mismatch'
  | 'disk_low'
  | 'disk_critical'
  | 'power_low'
  | 'video_fallback'
  | 'gpu_lost';
export type Severity = 'critical' | 'warning';

export interface Alert {
  kind: AlertKind;
  severity: Severity;
  deviceId: string;
  deviceName: string;
  zoneId?: string;
  zoneName?: string;
  since?: string;
  detail?: string;
  /** Acknowledged by a user: not open until the problem is gone and comes back. */
  ack?: { at: string; by: string };
}

/** The problems that count as open (not acknowledged). */
export function openAlerts(list: Alert[] | undefined): Alert[] {
  return (list ?? []).filter((a) => !a.ack);
}

/** confirmed | pending | timeout | failed | local | unknown (server devices/monitor.go). */
export type ApplyState = 'confirmed' | 'pending' | 'timeout' | 'failed' | 'local' | 'unknown';

export interface Applied {
  blackout: ApplyState;
  display: ApplyState;
  displayError?: string;
  displayMode?: DisplayConfig;
}

export interface Device {
  id: string;
  name: string;
  zoneId: string | null;
  zoneName: string;
  /** Display group in the zone and the screen's place there (older servers: absent). */
  groupId?: string | null;
  groupName?: string;
  position: number;
  paired: boolean;
  online: boolean;
  status: 'online' | 'offline' | 'unpaired';
  version: string;
  versionMatch: boolean;
  lastSeenAt: string | null;
  ips: string[];
  config: DeviceConfig;
  metrics: DeviceStatus | null;
  activeSource: { kind?: string; playlistId?: string } | null;
  // Newer servers only; older answers lack them (shown as "—").
  activeContent?: { kind?: string; playlistId?: string; playlistName?: string };
  sync: { state: string; done: number; total: number; error?: string };
  displayModes?: DisplayModeGroup[];
  applied?: Applied;
  alerts?: Alert[];
  screenshotAt: string | null;
}

/**
 * Boards that are not supported: the Raspberry Pi 2/3 hardware video decoder
 * fails regularly when videos change often. They may work with simple content.
 */
export function unsupportedBoard(model: string | undefined): boolean {
  return !!model && /raspberry pi (2|3)\b|compute module 3/i.test(model);
}

// ---- formatting ---------------------------------------------------------------------

function num(v: number, digits: number): string {
  return new Intl.NumberFormat(getLanguage(), { maximumFractionDigits: digits }).format(v);
}

function unit(v: number, u: 'day' | 'hour' | 'minute' | 'celsius', digits = 0): string {
  return new Intl.NumberFormat(getLanguage(), {
    style: 'unit',
    unit: u,
    unitDisplay: 'short',
    maximumFractionDigits: digits,
  }).format(v);
}

const isNum = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v);

/** "3 days 4 hr", "5 hr 12 min", "12 min" in the UI language; "—" when unknown. */
export function formatUptime(seconds: unknown): string {
  if (!isNum(seconds) || seconds < 0) return '—';
  const s = Math.floor(seconds);
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${unit(d, 'day')} ${unit(h, 'hour')}`;
  if (h > 0) return `${unit(h, 'hour')} ${unit(m, 'minute')}`;
  return unit(m, 'minute');
}

export function formatTemperature(c: unknown): string {
  return isNum(c) ? unit(c, 'celsius', 1) : '—';
}

/** Rating of a wireless signal (as the client WebGUI shows it). */
export function signalQuality(dbm: number): 'excellent' | 'good' | 'fair' | 'weak' {
  if (dbm >= -55) return 'excellent';
  if (dbm >= -67) return 'good';
  if (dbm >= -75) return 'fair';
  return 'weak';
}

/** 0–100 % of a signal in dBm (-100 dBm = 0 %, -50 dBm and better = 100 %). */
export function signalPercent(dbm: number): number {
  if (dbm === 0) return 0;
  return Math.max(0, Math.min(100, 2 * (dbm + 100)));
}

export function formatPercentValue(v: unknown): string {
  return isNum(v) ? `${num(v, 1)} %` : '—';
}

export function formatRefresh(hz: number): string {
  return `${num(hz, 2)} Hz`;
}

/** "1920 × 1080 @ 60 Hz" (the refresh part only when known). */
export function modeLabel(width: number, height: number, refresh?: number): string {
  const size = `${width} × ${height}`;
  return refresh && refresh > 0 ? `${size} @ ${formatRefresh(refresh)}` : size;
}

// ---- storage --------------------------------------------------------------------------

export type DiskLevel = 'ok' | 'warning' | 'critical';

/** Free-space level: < 20 % warning, < 10 % critical (the same rules as on the server). */
export function diskLevel(freePercent: number): DiskLevel {
  if (freePercent < 10) return 'critical';
  if (freePercent < 20) return 'warning';
  return 'ok';
}

export interface Storage {
  total: number;
  used: number;
  free: number;
  freePercent: number;
  level: DiskLevel;
  cache: number | null; // content cache
  logs: number | null; // null: not reported (older agent)
}

/** Storage figures of a status report; null when the device does not report its disk. */
export function storageOf(st: DeviceStatus | null | undefined): Storage | null {
  if (!st || !isNum(st.diskTotal) || st.diskTotal <= 0) return null;
  const total = st.diskTotal;
  const free = Math.min(total, isNum(st.diskFree) ? st.diskFree : 0);
  const freePercent = (free / total) * 100;
  return {
    total,
    used: total - free,
    free,
    freePercent,
    level: diskLevel(freePercent),
    cache: isNum(st.cacheBytes) ? st.cacheBytes : null,
    logs: isNum(st.logsBytes) ? st.logsBytes : null,
  };
}

// ---- display mode picker ---------------------------------------------------------------

export const AUTO = 'auto';
export const CUSTOM = 'custom';

export interface ModeOption {
  value: string;
  width: number;
  height: number;
  refresh: number;
  preferred: boolean;
  current: boolean;
}

const modeValue = (w: number, h: number, r: number): string => `${w}x${h}@${r}`;
// The agent reports rates rounded to 0.01 Hz.
const sameRate = (a: number, b: number): boolean => Math.abs(a - b) < 0.005;

/** One option per reported mode (resolution × refresh rate) in the screen's order. */
export function modeOptions(groups: DisplayModeGroup[] | undefined): ModeOption[] {
  const out: ModeOption[] = [];
  for (const g of groups ?? []) {
    for (const r of g.refresh) {
      out.push({
        value: modeValue(g.width, g.height, r),
        width: g.width,
        height: g.height,
        refresh: r,
        preferred: !!g.preferred && sameRate(g.preferred, r),
        current: !!g.current && sameRate(g.current, r),
      });
    }
  }
  return out;
}

/** The picker value for a stored display setting: auto, a reported mode or custom. Like
 * the agent, a rate within 0.5 Hz selects the closest reported mode; without a rate the
 * preferred (or current, or first) rate of that size. */
export function pickerValue(d: DisplayConfig, options: ModeOption[]): string {
  if (!d.width || !d.height) return AUTO;
  if (d.custom) return CUSTOM;
  const size = options.filter((o) => o.width === d.width && o.height === d.height);
  if (!d.refresh)
    return (size.find((o) => o.preferred) ?? size.find((o) => o.current) ?? size[0])?.value ?? CUSTOM;
  let best: ModeOption | undefined;
  for (const o of size) {
    const diff = Math.abs(o.refresh - d.refresh);
    if (diff < 0.5 && (!best || diff < Math.abs(best.refresh - d.refresh))) best = o;
  }
  return best ? best.value : CUSTOM;
}

/** The display setting for a picker choice (rotation is kept). */
export function displayFor(value: string, options: ModeOption[], current: DisplayConfig): DisplayConfig {
  if (value === AUTO) return { width: 0, height: 0, refresh: 0, rotation: current.rotation, custom: false };
  if (value === CUSTOM) {
    const base =
      current.width && current.height ? current : { ...current, width: 1920, height: 1080, refresh: 60 };
    return { ...base, custom: true };
  }
  const o = options.find((x) => x.value === value);
  if (!o) return current;
  return { width: o.width, height: o.height, refresh: o.refresh, rotation: current.rotation, custom: false };
}

/** Field-level check of a custom mode (the server validates again). */
export function customModeValid(d: DisplayConfig): boolean {
  return (
    d.width >= 320 &&
    d.width <= 7680 &&
    d.height >= 200 &&
    d.height <= 4320 &&
    d.refresh >= 0 &&
    d.refresh <= 240
  );
}

export function sameDisplay(a: DisplayConfig, b: DisplayConfig): boolean {
  return (
    a.width === b.width &&
    a.height === b.height &&
    a.refresh === b.refresh &&
    a.rotation === b.rotation &&
    a.custom === b.custom
  );
}

// ---- alerts ----------------------------------------------------------------------------

export interface DeviceAlerts {
  deviceId: string;
  deviceName: string;
  zoneName: string;
  severity: Severity;
  alerts: Alert[];
}

const severityRank = (s: Severity): number => (s === 'critical' ? 0 : 1);

/** Groups alerts by device: devices with a critical problem first, then by name; within a
 * device critical problems first, then the oldest. */
export function groupAlerts(list: Alert[]): DeviceAlerts[] {
  const byDevice = new Map<string, DeviceAlerts>();
  for (const a of list) {
    let g = byDevice.get(a.deviceId);
    if (!g) {
      g = {
        deviceId: a.deviceId,
        deviceName: a.deviceName,
        zoneName: a.zoneName ?? '',
        severity: a.severity,
        alerts: [],
      };
      byDevice.set(a.deviceId, g);
    }
    g.alerts.push(a);
    if (severityRank(a.severity) < severityRank(g.severity)) g.severity = a.severity;
  }
  const groups = [...byDevice.values()];
  for (const g of groups) {
    g.alerts.sort(
      (x, y) =>
        severityRank(x.severity) - severityRank(y.severity) || (x.since ?? '').localeCompare(y.since ?? ''),
    );
  }
  return groups.sort(
    (x, y) =>
      severityRank(x.severity) - severityRank(y.severity) ||
      x.deviceName.localeCompare(y.deviceName, getLanguage(), { sensitivity: 'base' }),
  );
}

/** Counts per severity (sidebar and dashboard). */
export function countBySeverity(list: Alert[]): Record<Severity, number> {
  const out: Record<Severity, number> = { critical: 0, warning: 0 };
  for (const a of list) out[a.severity === 'critical' ? 'critical' : 'warning']++;
  return out;
}

/** Splits an agent error like "reboot_not_permitted: sudo: …" into a code and the rest. */
export function splitErrorCode(raw: string): { code: string; rest: string } {
  const m = /^([a-z][a-z0-9_]*)(?::\s*(.*))?$/s.exec(raw.trim());
  return m ? { code: m[1]!, rest: (m[2] ?? '').trim() } : { code: '', rest: raw.trim() };
}
