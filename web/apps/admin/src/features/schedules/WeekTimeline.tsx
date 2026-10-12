// Weekly timeline (CSS grid, no chart library): one row per weekday, zone windows in the
// upper lane, global windows in the lower lane, a marker at "now" in the server time zone.
import { useEffect, useMemo, useState } from 'react';
import type { Rule } from '@opensignage/contracts';
import { getLanguage } from '@opensignage/i18n';
import { useT } from '../../lib/hooks';
import { DAY_MINUTES, nowInTimezone, weekdayLabels, windowSegments } from './windows';

/** Current weekday/minute in the zone, refreshed every 30 s. */
export function useNowIn(timeZone: string): { day: number; minute: number } {
  const [now, setNow] = useState(() => nowInTimezone(new Date(), timeZone));
  useEffect(() => {
    setNow(nowInTimezone(new Date(), timeZone));
    const id = window.setInterval(() => setNow(nowInTimezone(new Date(), timeZone)), 30000);
    return () => clearInterval(id);
  }, [timeZone]);
  return now;
}

interface Block {
  key: string;
  rule: Rule;
  day: number;
  start: number;
  end: number;
  continued: boolean;
  continues: boolean;
  lane: 'zone' | 'global';
}

const HOURS = [0, 3, 6, 9, 12, 15, 18, 21, 24];

export function Timeline({ rules, timeZone, onOpen, showZoneLane }: { rules: Rule[]; timeZone: string; onOpen: (r: Rule) => void; showZoneLane: boolean }) {
  const t = useT();
  const lang = getLanguage();
  const labels = useMemo(() => weekdayLabels(lang), [lang]);
  const now = useNowIn(timeZone);
  const blocks = useMemo(() => {
    const out: Block[] = [];
    for (const r of rules) {
      if (!r.window || r.scope === 'default') continue;
      for (const s of windowSegments(r.window)) {
        out.push({ key: `${r.id}-${s.day}-${s.start}`, rule: r, ...s, lane: r.scope === 'global' ? 'global' : 'zone' });
      }
    }
    return out;
  }, [rules]);
  const pct = (m: number): string => `${(m / DAY_MINUTES) * 100}%`;
  const lanes = showZoneLane ? 'two-lanes' : 'one-lane';
  return (
    <div className={`tl ${lanes}`} role="table" aria-label={t('admin.schedules.timeline')}>
      <div className="tl-row tl-head" role="row">
        <span className="tl-day" />
        <div className="tl-hours">
          {HOURS.map((h) => (
            <span key={h} style={{ left: pct(h * 60) }}>
              {String(h).padStart(2, '0')}
            </span>
          ))}
        </div>
      </div>
      {[1, 2, 3, 4, 5, 6, 7].map((day) => (
        <div key={day} className={`tl-row ${now.day === day ? 'today' : ''}`} role="row">
          <span className="tl-day" role="rowheader">
            {labels[day]}
          </span>
          <div className="tl-track" role="cell">
            {HOURS.slice(1, -1).map((h) => (
              <span key={h} className="tl-grid" style={{ left: pct(h * 60) }} />
            ))}
            {blocks
              .filter((b) => b.day === day)
              .map((b) => {
                const r = b.rule;
                const time = `${r.window!.start}–${r.window!.end}`;
                const kind = r.playlistId ? '' : ` · ${t('admin.schedules.pricesOnly')}`;
                const state = r.enabled ? '' : ` · ${t('admin.schedules.disabled')}`;
                return (
                  <button
                    key={b.key}
                    type="button"
                    className={`tl-block lane-${b.lane} ${r.playlistId ? '' : 'prices-only'} ${r.enabled ? '' : 'disabled'} ${b.continued ? 'continued' : ''} ${b.continues ? 'continues' : ''}`}
                    style={{ left: pct(b.start), width: pct(b.end - b.start) }}
                    title={`${r.name} · ${time}${kind}${state}`}
                    onClick={() => onOpen(r)}
                  >
                    <span>{r.name}</span>
                  </button>
                );
              })}
            {now.day === day && <span className="tl-now" style={{ left: pct(now.minute) }} title={t('admin.schedules.nowMarker')} />}
          </div>
        </div>
      ))}
      <div className="tl-legend">
        {showZoneLane && (
          <span>
            <i className="lane-zone" /> {t('admin.schedules.scope.zone')}
          </span>
        )}
        <span>
          <i className="lane-global" /> {t('admin.schedules.scope.global')}
        </span>
        <span>
          <i className="prices-only" /> {t('admin.schedules.pricesOnly')}
        </span>
        <span>
          <i className="disabled" /> {t('admin.schedules.disabled')}
        </span>
        <span className="faint">{t('admin.schedules.timezoneNote', { tz: timeZone })}</span>
      </div>
    </div>
  );
}
