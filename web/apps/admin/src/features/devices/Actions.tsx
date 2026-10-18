// Remote actions: every action shows "pending" until the device
// answers or the timeout applies, then the result or the reason of a failure. Blackout and
// display changes are pending until the device reports them applied. "Clean cache" shows a
// preview first. Plasma regeneration (white noise) runs until it is switched off or
// the client restarts; the device reports it as its mode.
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import {
  Camera,
  CheckCircle2,
  Clock,
  Loader2,
  Monitor,
  MonitorOff,
  Power,
  RefreshCw,
  RotateCw,
  Sparkles,
  XCircle,
  Info,
  AlertTriangle,
} from 'lucide-react';
import { has } from '@opensignage/i18n';
import type { CacheCleanup, DisplayConfig } from '@opensignage/contracts';
import { errorText, get, post } from '../../lib/api';
import { useAction, useT } from '../../lib/hooks';
import { useConfirm } from '../../components/feedback';
import { Button, Dialog, ErrorText } from '../../components/ui';
import { formatBytes } from '../media/model';
import { modeLabel, splitErrorCode, type ApplyState, type Device } from './model';

export type RunState = 'pending' | 'ok' | 'failed' | 'timeout';

export interface ActionRun {
  name: string;
  state: RunState;
  error?: string; // translated reason
  data?: unknown;
}

const POLL_MS = 1000;
const POLL_MAX = 75; // the server times out after 60 s

/** Translated reason of a failed action ("reboot_not_permitted: …" → text (…)). */
export function useActionError(): (raw: string | undefined) => string {
  const t = useT();
  return (raw) => {
    if (!raw) return t('devices.actionState.failed');
    const { code, rest } = splitErrorCode(raw);
    if (code && has(`devices.actionError.${code}`))
      return rest ? `${t(`devices.actionError.${code}`)} (${rest})` : t(`devices.actionError.${code}`);
    return raw;
  };
}

/** Runs remote actions of one device and tracks their state per key. */
export function useRemoteActions(deviceId: string): {
  runs: Record<string, ActionRun>;
  run: (key: string, name: string, params?: Record<string, unknown>) => Promise<ActionRun>;
} {
  const [runs, setRuns] = useState<Record<string, ActionRun>>({});
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  const actionError = useActionError();
  const errRef = useRef(actionError);
  errRef.current = actionError;
  const set = (key: string, r: ActionRun): void => {
    if (alive.current) setRuns((all) => ({ ...all, [key]: r }));
  };
  const run = useCallback(
    async (key: string, name: string, params?: Record<string, unknown>): Promise<ActionRun> => {
      set(key, { name, state: 'pending' });
      let result: ActionRun;
      try {
        const id = encodeURIComponent(deviceId);
        const r = await post<{ requestId: string }>(
          `/api/v1/devices/${id}/actions`,
          params ? { name, params } : { name },
        );
        result = { name, state: 'timeout' };
        for (let i = 0; i < POLL_MAX && alive.current; i++) {
          await new Promise((res) => setTimeout(res, POLL_MS));
          const s = await get<{ state: RunState; error?: string; data?: unknown }>(
            `/api/v1/devices/${id}/actions/${encodeURIComponent(r.requestId)}`,
          );
          if (s.state !== 'pending') {
            result = {
              name,
              state: s.state,
              data: s.data,
              error: s.state === 'failed' ? errRef.current(s.error) : undefined,
            };
            break;
          }
        }
      } catch (e) {
        result = { name, state: 'failed', error: errorText(e) };
      }
      set(key, result);
      return result;
    },
    [deviceId],
  );
  return { runs, run };
}

function StateLine({
  icon,
  tone,
  children,
}: {
  icon: ReactNode;
  tone?: 'ok' | 'warn' | 'bad' | 'muted';
  children: ReactNode;
}) {
  return (
    <div className={`state-line ${tone ?? 'muted'}`} role="status">
      {icon}
      <span>{children}</span>
    </div>
  );
}

/** One line per action that ran: pending, done, failed (reason) or no answer (timeout). */
export function ActionRunLine({ label, r }: { label: string; r: ActionRun }) {
  const t = useT();
  switch (r.state) {
    case 'pending':
      return (
        <StateLine icon={<Loader2 size={14} className="spin" aria-hidden />}>
          {label}: {t('devices.actionPending')}
        </StateLine>
      );
    case 'ok':
      return (
        <StateLine tone="ok" icon={<CheckCircle2 size={14} aria-hidden />}>
          {t('devices.actionDone', { action: label })}
        </StateLine>
      );
    case 'timeout':
      return (
        <StateLine tone="warn" icon={<Clock size={14} aria-hidden />}>
          {label}: {t('devices.actionState.timeout')}
        </StateLine>
      );
    default:
      return (
        <StateLine tone="bad" icon={<XCircle size={14} aria-hidden />}>
          {t('devices.actionFailed', { action: label, reason: r.error ?? t('devices.actionState.failed') })}
        </StateLine>
      );
  }
}

/** Whether the device applied a blackout or display change. */
export function AppliedLine({
  state,
  online,
  error,
  localMode,
}: {
  state: ApplyState | undefined;
  online: boolean;
  error?: string;
  localMode?: DisplayConfig;
}) {
  const t = useT();
  switch (state) {
    case 'confirmed':
      return (
        <StateLine tone="ok" icon={<CheckCircle2 size={14} aria-hidden />}>
          {t('devices.applied.confirmed')}
        </StateLine>
      );
    case 'pending':
      return online ? (
        <StateLine icon={<Loader2 size={14} className="spin" aria-hidden />}>
          {t('devices.applied.pending')}
        </StateLine>
      ) : (
        <StateLine tone="warn" icon={<Clock size={14} aria-hidden />}>
          {t('devices.applied.offline')}
        </StateLine>
      );
    case 'timeout':
      return (
        <StateLine tone="warn" icon={<AlertTriangle size={14} aria-hidden />}>
          {t('devices.applied.timeout')}
        </StateLine>
      );
    case 'failed':
      return (
        <StateLine tone="bad" icon={<XCircle size={14} aria-hidden />}>
          {t('devices.applied.failed', { reason: error || t('devices.actionState.failed') })}
        </StateLine>
      );
    case 'local':
      return (
        <StateLine icon={<Info size={14} aria-hidden />}>
          {t('devices.applied.local', {
            mode: localMode?.width
              ? modeLabel(localMode.width, localMode.height, localMode.refresh)
              : t('devices.displayAuto'),
          })}
        </StateLine>
      );
    default:
      return null; // older agent: it does not report it
  }
}

const ACTIONS: { name: string; icon: typeof RefreshCw; danger?: boolean }[] = [
  { name: 'sync_now', icon: RefreshCw },
  { name: 'reload_player', icon: RotateCw },
  { name: 'restart_client', icon: Monitor },
  { name: 'screenshot', icon: Camera },
  { name: 'reboot_device', icon: Power, danger: true },
];

/** The remote actions card: actions with their state, blackout with its confirmation. */
export function ActionsCard({
  d,
  onChanged,
  onScreenshot,
  shot,
  control = true,
}: {
  d: Device;
  onChanged: () => void;
  onScreenshot: () => void;
  shot: number;
  /** Restart, reload, blackout …: false for operators limited to zones without the displays right (a screenshot only looks). */
  control?: boolean;
}) {
  const t = useT();
  const confirm = useConfirm();
  const { runs, run } = useRemoteActions(d.id);
  const start = async (name: string, danger?: boolean): Promise<void> => {
    if (runs[name]?.state === 'pending') return;
    if (
      danger &&
      !(await confirm({
        title: t(`devices.action.${name}`),
        message: t('devices.rebootConfirm'),
        danger: true,
        confirmLabel: t(`devices.action.${name}`),
      }))
    )
      return;
    const r = await run(name, name);
    if (r.state === 'ok' && name === 'screenshot') onScreenshot();
  };
  const blackout = useAction(async (on: boolean) => {
    await post(`/api/v1/devices/${encodeURIComponent(d.id)}/blackout`, { on });
    onChanged();
  });
  const blackoutState = d.applied?.blackout;
  const canRegenerate = d.metrics?.caps?.includes('plasma_regeneration') ?? false;
  const regenerating = d.online && d.metrics?.mode === 'regeneration';
  const regeneration = async (): Promise<void> => {
    if (runs.regeneration?.state === 'pending') return;
    const on = !regenerating;
    if (
      on &&
      !(await confirm({
        title: t('devices.regen.start'),
        message: t('devices.regen.confirm'),
        confirmLabel: t('devices.regen.start'),
      }))
    )
      return;
    const r = await run('regeneration', 'plasma_regeneration', { on });
    if (r.state === 'ok') onChanged();
  };
  return (
    <div className="card">
      <h2>{t('devices.actions')}</h2>
      <div className="action-grid">
        {ACTIONS.filter((a) => control || a.name === 'screenshot').map((a) => (
          <Button
            key={a.name}
            icon={<a.icon size={16} />}
            variant={a.danger ? 'danger' : 'secondary'}
            busy={runs[a.name]?.state === 'pending'}
            disabled={!d.online}
            onClick={() => void start(a.name, a.danger)}
          >
            {t(`devices.action.${a.name}`)}
          </Button>
        ))}
        {control && (
          <Button
            icon={<MonitorOff size={16} />}
            variant={d.config.blackout ? 'primary' : 'secondary'}
            busy={blackout.busy}
            onClick={() => void blackout.run(!d.config.blackout)}
          >
            {d.config.blackout ? t('devices.blackoutOff') : t('devices.blackoutOn')}
          </Button>
        )}
        {control && canRegenerate && (
          <Button
            icon={<Sparkles size={16} />}
            variant={regenerating ? 'primary' : 'secondary'}
            busy={runs.regeneration?.state === 'pending'}
            disabled={!d.online}
            onClick={() => void regeneration()}
          >
            {regenerating ? t('devices.regen.stop') : t('devices.regen.start')}
          </Button>
        )}
      </div>
      {!d.online && <p className="faint">{t('devices.offlineActions')}</p>}
      <div className="state-lines">
        {ACTIONS.filter((a) => runs[a.name]).map((a) => (
          <ActionRunLine key={a.name} label={t(`devices.action.${a.name}`)} r={runs[a.name]!} />
        ))}
        {runs.regeneration && runs.regeneration.state !== 'ok' && (
          <ActionRunLine label={t('devices.action.plasma_regeneration')} r={runs.regeneration} />
        )}
        {regenerating && (
          <StateLine tone="warn" icon={<Sparkles size={14} aria-hidden />}>
            {t('devices.regen.running')}
          </StateLine>
        )}
        {(d.config.blackout || (blackoutState && blackoutState !== 'confirmed')) && (
          <div className="state-group">
            <span className="state-label">
              {d.config.blackout ? t('devices.status.blackout') : t('devices.blackoutOff')}:
            </span>
            <AppliedLine state={blackoutState} online={d.online} />
          </div>
        )}
      </div>
      <ErrorText error={blackout.error} />
      {(d.screenshotAt || shot > 0) && (
        <img
          className="screenshot"
          src={`/api/v1/devices/${encodeURIComponent(d.id)}/screenshot?t=${shot || d.screenshotAt}`}
          alt={t('devices.action.screenshot')}
        />
      )}
    </div>
  );
}

/** "Clean cache": the device first reports what it would delete, then the user confirms. */
export function CleanCacheDialog({
  d,
  onClose,
  onDone,
}: {
  d: Device;
  onClose: () => void;
  onDone: (freed: number) => void;
}) {
  const t = useT();
  const { runs, run } = useRemoteActions(d.id);
  const started = useRef(false);
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    void run('preview', 'clean_cache', { dryRun: true });
  }, [run]);
  const preview = runs.preview;
  const clean = runs.clean;
  const p = preview?.state === 'ok' ? (preview.data as CacheCleanup | undefined) : undefined;
  const c = clean?.state === 'ok' ? (clean.data as CacheCleanup | undefined) : undefined;
  const reported = useRef(false);
  useEffect(() => {
    if (c && !reported.current) {
      reported.current = true;
      onDone(c.freedBytes);
    }
  }, [c, onDone]);
  const nothing = !!p && p.deleted === 0;
  return (
    <Dialog
      title={t('devices.action.clean_cache')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>
            {p && !nothing && !c ? t('common.action.cancel') : t('common.action.close')}
          </Button>
          {p && !nothing && !c && (
            <Button
              variant="danger"
              busy={clean?.state === 'pending'}
              disabled={!d.online}
              onClick={() => void run('clean', 'clean_cache')}
            >
              {t('devices.clean.confirm', { size: formatBytes(p.freedBytes) })}
            </Button>
          )}
        </>
      }
    >
      <div className="form">
        {(!preview || preview.state === 'pending') && (
          <ActionRunLine
            label={t('devices.clean.preview')}
            r={preview ?? { name: 'clean_cache', state: 'pending' }}
          />
        )}
        {preview && preview.state !== 'pending' && preview.state !== 'ok' && (
          <ActionRunLine label={t('devices.clean.preview')} r={preview} />
        )}
        {p && !c && (
          <>
            {nothing ? (
              <p>{t('devices.clean.nothing')}</p>
            ) : (
              <dl className="kv">
                <dt>{t('devices.clean.files')}</dt>
                <dd>
                  {p.deleted} · {formatBytes(p.freedBytes)}
                </dd>
                {p.partial > 0 && (
                  <>
                    <dt>{t('devices.clean.partial')}</dt>
                    <dd>
                      {p.partial} · {formatBytes(p.partialBytes)}
                    </dd>
                  </>
                )}
                <dt>{t('devices.clean.after')}</dt>
                <dd>{formatBytes(p.cacheBytes)}</dd>
              </dl>
            )}
            <p className="faint">{t('devices.clean.hint')}</p>
          </>
        )}
        {clean && clean.state !== 'ok' && <ActionRunLine label={t('devices.action.clean_cache')} r={clean} />}
        {c && (
          <StateLine tone="ok" icon={<CheckCircle2 size={14} aria-hidden />}>
            {t('devices.clean.done', { size: formatBytes(c.freedBytes), cache: formatBytes(c.cacheBytes) })}
          </StateLine>
        )}
      </div>
    </Dialog>
  );
}
