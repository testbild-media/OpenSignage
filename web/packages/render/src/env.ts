import type { Typography } from '@opensignage/contracts';
import type { LiveStore } from './live';

export type RenderMode = 'play' | 'edit' | 'thumb';

/** Resolves asset ids to URLs. purpose "display" = best variant for the screen. */
export interface AssetResolver {
  url(assetId: string, purpose: 'display' | 'poster' | 'thumb'): string | null;
}

export interface RenderEnv {
  typography: Typography;
  assets: AssetResolver;
  live: LiveStore;
  mode: RenderMode;
  /** Called when something cannot be shown (missing asset etc.). Never throws. */
  report?: (level: 'warn' | 'error', msg: string) => void;
}

/** A rendered element with lifecycle. */
export interface ElementHandle {
  el: HTMLElement;
  /** Video element whose end matters (background or element video). */
  video?: HTMLVideoElement;
  start(): void;
  stop(): void;
  destroy(): void;
}

export function noopHandle(el: HTMLElement): ElementHandle {
  return { el, start() {}, stop() {}, destroy() {} };
}
