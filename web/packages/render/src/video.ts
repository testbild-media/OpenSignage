/**
 * Video self-healing: detects stalls (current time not advancing while
 * it should play) and the "hangs just before the end" decoder bug, and restarts or
 * reports the end. Also makes a single looping video restart cleanly.
 */
export interface VideoWatch {
  start(): void;
  stop(): void;
  /** Switches between looping and playing to the end (a continuing background video
   * follows the duration mode of each new slide). */
  setLoop(loop: boolean): void;
}

export interface WatchOptions {
  loop: boolean;
  /** Called when the video ended (or is treated as ended after a stall near the end). */
  onEnded?: () => void;
  /** Called when the video could not be recovered (error or repeated stalls). */
  onFailed?: (reason: string) => void;
  stallMs?: number;
}

/** Stalls that follow each other within this window count together (the fourth is a
 * failure); isolated stalls, e.g. hours apart on a looping background, are always recovered. */
const RECOVERY_WINDOW_MS = 60_000;

export function watchVideo(v: HTMLVideoElement, opts: WatchOptions): VideoWatch {
  const stallMs = opts.stallMs ?? 5000;
  let timer = 0;
  let lastTime = -1;
  let lastProgressAt = 0;
  let recoveries = 0;
  let lastRecoveryAt = -Infinity;
  let active = false;
  let loop = opts.loop;

  const play = (): void => {
    const p = v.play();
    if (p) p.catch((e: unknown) => {
      // Interrupted by a new load or a pause (AbortError, e.g. right after the start or a
      // recovery reload): not a media failure; the stall check notices a video that then
      // does not run.
      if ((e as { name?: string } | null)?.name === 'AbortError') return;
      // Autoplay is allowed by kiosk flags; any other rejection means the media is broken.
      opts.onFailed?.('play_rejected');
    });
  };

  const onEnded = (): void => {
    if (!active) return;
    if (loop) {
      v.currentTime = 0;
      play();
    } else {
      opts.onEnded?.();
    }
  };
  // Reloads the media and continues where it was. Up to three recoveries per minute; a
  // video that keeps failing is retried at most once a minute (never given up for good).
  const recover = (now: number): boolean => {
    if (now - lastRecoveryAt > RECOVERY_WINDOW_MS) recoveries = 0;
    if (recoveries >= 3) return false;
    recoveries++;
    lastRecoveryAt = now;
    lastProgressAt = now;
    const t = Number.isFinite(v.currentTime) ? v.currentTime : 0;
    v.load();
    if (t > 0) v.currentTime = t;
    play();
    return true;
  };

  // A decode error (e.g. the hardware decoder gave up) leaves the element paused with
  // v.error set: reload it instead of keeping a frozen picture.
  const onError = (): void => {
    if (!active) return;
    const code = v.error?.code ?? 0;
    if (!recover(performance.now())) opts.onFailed?.(`media_error_${code}`);
  };

  const check = (): void => {
    if (!active) return;
    const now = performance.now();
    if (v.error && now - lastRecoveryAt > RECOVERY_WINDOW_MS) {
      recover(now);
    } else if (v.currentTime !== lastTime) {
      lastTime = v.currentTime;
      lastProgressAt = now;
    } else if (!v.paused && v.readyState >= 1 && now - lastProgressAt > stallMs) {
      const nearEnd = Number.isFinite(v.duration) && v.duration > 0 && v.duration - v.currentTime < 1.5;
      lastProgressAt = now;
      if (nearEnd) {
        onEnded();
      } else {
        if (!recover(now)) opts.onFailed?.('stalled');
      }
    }
    timer = window.setTimeout(check, 1000);
  };

  v.addEventListener('ended', onEnded);
  v.addEventListener('error', onError);
  return {
    start() {
      active = true;
      lastProgressAt = performance.now();
      lastTime = -1;
      play();
      clearTimeout(timer);
      timer = window.setTimeout(check, 1000);
    },
    stop() {
      active = false;
      clearTimeout(timer);
    },
    setLoop(next: boolean) {
      loop = next;
      v.loop = next;
    },
  };
}
