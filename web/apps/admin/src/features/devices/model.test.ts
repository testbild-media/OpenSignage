import { describe, expect, it } from 'vitest';
import type { DisplayConfig } from '@opensignage/contracts';
import {
  AUTO,
  CUSTOM,
  countBySeverity,
  customModeValid,
  diskLevel,
  displayFor,
  formatPercentValue,
  formatTemperature,
  formatUptime,
  groupAlerts,
  modeLabel,
  openAlerts,
  modeOptions,
  pickerValue,
  splitErrorCode,
  storageOf,
  type Alert,
  unsupportedBoard,
} from './model';

const groups = [
  { width: 1920, height: 1080, refresh: [60, 50, 59.94], preferred: 60, current: 50 },
  { width: 1280, height: 720, refresh: [60] },
];
const disp = (p: Partial<DisplayConfig>): DisplayConfig => ({
  width: 0,
  height: 0,
  refresh: 0,
  rotation: 0,
  custom: false,
  ...p,
});

describe('monitoring values', () => {
  it('formats uptime, temperature and percentages for people', () => {
    expect(formatUptime(42)).toBe('0 min');
    expect(formatUptime(3 * 3600 + 12 * 60)).toBe('3 hr 12 min');
    expect(formatUptime(2 * 86400 + 5 * 3600)).toBe('2 days 5 hr');
    expect(formatUptime(undefined)).toBe('—');
    expect(formatTemperature(51.24)).toBe('51.2°C');
    expect(formatTemperature(null)).toBe('—');
    expect(formatPercentValue(12.345)).toBe('12.3 %');
    expect(modeLabel(1920, 1080, 59.94)).toBe('1920 × 1080 @ 59.94 Hz');
    expect(modeLabel(1280, 720)).toBe('1280 × 720');
  });

  it('derives storage figures and free-space levels', () => {
    expect(storageOf({ diskTotal: 1000, diskFree: 150, cacheBytes: 300, logsBytes: 20 })).toEqual({
      total: 1000,
      used: 850,
      free: 150,
      freePercent: 15,
      level: 'warning',
      cache: 300,
      logs: 20,
    });
    // Older agents: no logs size; no disk figures at all → null.
    expect(storageOf({ diskTotal: 1000, diskFree: 50 })?.logs).toBeNull();
    expect(storageOf({ diskTotal: 1000, diskFree: 50 })?.level).toBe('critical');
    expect(storageOf({})).toBeNull();
    expect(storageOf(null)).toBeNull();
    expect([diskLevel(25), diskLevel(19.9), diskLevel(9.9)]).toEqual(['ok', 'warning', 'critical']);
  });
});

describe('display mode picker', () => {
  const opts = modeOptions(groups);

  it('offers every reported mode with preferred and current marked', () => {
    expect(opts.map((o) => o.value)).toEqual([
      '1920x1080@60',
      '1920x1080@50',
      '1920x1080@59.94',
      '1280x720@60',
    ]);
    expect(opts.filter((o) => o.preferred).map((o) => o.value)).toEqual(['1920x1080@60']);
    expect(opts.filter((o) => o.current).map((o) => o.value)).toEqual(['1920x1080@50']);
    expect(modeOptions(undefined)).toEqual([]);
  });

  it('maps stored settings to the picker and back', () => {
    expect(pickerValue(disp({}), opts)).toBe(AUTO);
    expect(pickerValue(disp({ width: 1920, height: 1080, refresh: 59.94 }), opts)).toBe('1920x1080@59.94');
    expect(pickerValue(disp({ width: 1920, height: 1080, refresh: 0 }), opts)).toBe('1920x1080@60');
    expect(pickerValue(disp({ width: 1366, height: 768, refresh: 60 }), opts)).toBe(CUSTOM);
    expect(pickerValue(disp({ width: 1920, height: 1080, refresh: 60, custom: true }), opts)).toBe(CUSTOM);
    // Rotation stays when the mode changes; automatic clears the size.
    const cur = disp({ width: 1280, height: 720, refresh: 60, rotation: 90 });
    expect(displayFor('1920x1080@50', opts, cur)).toEqual(
      disp({ width: 1920, height: 1080, refresh: 50, rotation: 90 }),
    );
    expect(displayFor(AUTO, opts, cur)).toEqual(disp({ rotation: 90 }));
    expect(displayFor(CUSTOM, opts, cur)).toEqual({ ...cur, custom: true });
    expect(displayFor(CUSTOM, opts, disp({}))).toEqual(
      disp({ width: 1920, height: 1080, refresh: 60, custom: true }),
    );
    expect(customModeValid(disp({ width: 1366, height: 768, refresh: 60 }))).toBe(true);
    expect(customModeValid(disp({ width: 100, height: 768, refresh: 60 }))).toBe(false);
  });
});

describe('alerts', () => {
  const a = (
    deviceId: string,
    deviceName: string,
    kind: Alert['kind'],
    severity: Alert['severity'],
    since = '',
  ): Alert => ({
    kind,
    severity,
    deviceId,
    deviceName,
    since,
  });

  it('groups by device, critical devices and problems first', () => {
    const list = [
      a('1', 'Bar', 'version_mismatch', 'warning'),
      a('2', 'terrace', 'disk_low', 'warning', '2026-10-11T10:00:00Z'),
      a('1', 'Bar', 'device_offline', 'critical', '2026-10-11T09:00:00Z'),
      a('3', 'Entrance', 'sync_failed', 'warning'),
    ];
    const g = groupAlerts(list);
    expect(g.map((x) => [x.deviceName, x.severity])).toEqual([
      ['Bar', 'critical'],
      ['Entrance', 'warning'],
      ['terrace', 'warning'],
    ]);
    expect(g[0]!.alerts.map((x) => x.kind)).toEqual(['device_offline', 'version_mismatch']);
    expect(countBySeverity(list)).toEqual({ critical: 1, warning: 3 });
    // A failed update (with its time) comes before the differing version it explains.
    const upd = groupAlerts([a('4', 'Terrace', 'version_mismatch', 'warning'), a('4', 'Terrace', 'update_failed', 'warning', '2026-10-24T15:46:18Z')]);
    expect(upd[0]!.alerts.map((x) => x.kind)).toEqual(['update_failed', 'version_mismatch']);
    expect(groupAlerts([])).toEqual([]);
  });

  it('acknowledged problems do not count as open', () => {
    const acked = {
      ...a('1', 'Pi', 'power_low', 'warning'),
      ack: { at: '2026-10-11T21:00:00Z', by: 'admin' },
    };
    const open = a('2', 'x86', 'disk_low', 'warning');
    expect(openAlerts([acked, open])).toEqual([open]);
    expect(openAlerts(undefined)).toEqual([]);
  });

  it('splits agent error codes from their details', () => {
    expect(splitErrorCode('reboot_not_permitted: sudo: a password is required')).toEqual({
      code: 'reboot_not_permitted',
      rest: 'sudo: a password is required',
    });
    expect(splitErrorCode('player_not_running')).toEqual({ code: 'player_not_running', rest: '' });
    expect(splitErrorCode('upload failed: HTTP 500')).toEqual({ code: '', rest: 'upload failed: HTTP 500' });
  });
});

describe('unsupported boards', () => {
  it('marks the Raspberry Pi 2/3, not the Pi 4/5 or PCs', () => {
    expect(unsupportedBoard('Raspberry Pi 3 Model B Rev 1.2')).toBe(true);
    expect(unsupportedBoard('Raspberry Pi 3 Model B Plus Rev 1.3')).toBe(true);
    expect(unsupportedBoard('Raspberry Pi 2 Model B Rev 1.1')).toBe(true);
    expect(unsupportedBoard('Raspberry Pi Compute Module 3 Rev 1.0')).toBe(true);
    expect(unsupportedBoard('Raspberry Pi 4 Model B Rev 1.4')).toBe(false);
    expect(unsupportedBoard('Raspberry Pi 5 Model B Rev 1.0')).toBe(false);
    expect(unsupportedBoard('NUC6i3SYB')).toBe(false);
    expect(unsupportedBoard(undefined)).toBe(false);
  });
});
