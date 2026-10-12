import type { TextStyle } from '@opensignage/contracts';
import { applyTextStyle } from './units';

export interface Ticker {
  setText(text: string): void;
  start(): void;
  stop(): void;
}

/**
 * Creates a seamlessly looping marquee inside host. Speed is in design pixels per second;
 * the track is duplicated and moved by exactly one copy width (transform only).
 */
export function createTicker(
  host: HTMLElement,
  style: TextStyle,
  speed: number,
  direction: 'left' | 'right',
  animate: boolean,
): Ticker {
  host.classList.add('os-ticker');
  const track = document.createElement('div');
  track.className = 'os-ticker-track';
  applyTextStyle(track, { ...style, align: 'left' });
  host.appendChild(track);
  let text = '';
  let anim: Animation | null = null;
  let running = false;

  const layout = (): void => {
    anim?.cancel();
    anim = null;
    track.replaceChildren();
    if (!text) return;
    const a = document.createElement('span');
    a.textContent = text;
    track.appendChild(a);
    if (!animate || !running) return;
    // Measure one copy, then fill at least twice the host width for a gap-free loop.
    const one = a.getBoundingClientRect().width;
    const hostW = host.getBoundingClientRect().width;
    if (one <= 0) return;
    const copies = Math.max(2, Math.ceil((hostW * 2) / one));
    for (let i = 1; i < copies; i++) track.appendChild(a.cloneNode(true));
    const scale = parseFloat(getComputedStyle(host).getPropertyValue('--s')) || 1;
    const pxPerSec = Math.max(5, speed * scale);
    const from = direction === 'left' ? 0 : -one;
    const to = direction === 'left' ? -one : 0;
    anim = track.animate([{ transform: `translateX(${from}px)` }, { transform: `translateX(${to}px)` }], {
      duration: (one / pxPerSec) * 1000,
      iterations: Infinity,
      easing: 'linear',
    });
  };

  return {
    setText(next: string) {
      if (next === text) return;
      text = next;
      layout();
    },
    start() {
      // Already running: keep the scroll position (the player calls start() on every
      // slide change and live update; restarting would make the text jump back).
      if (running) return;
      running = true;
      if (document.fonts) document.fonts.ready.then(layout).catch(layout);
      else layout();
    },
    stop() {
      running = false;
      anim?.cancel();
      anim = null;
    },
  };
}
