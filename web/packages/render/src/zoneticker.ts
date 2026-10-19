// The ticker of the active time schedule rule over the content of a screen. Used by the player
// and by the zone preview of the administration, so both show it the same way.
import type { ScheduleTicker, Slide } from '@opensignage/contracts';
import { createTicker, type Ticker } from './ticker';

/** A slide with a visible ticker element fed from the zone shows the text itself: no overlay. */
export function zoneTickerSuppressed(slide: Slide | null): boolean {
  return slide?.elements.some((e) => e.type === 'ticker' && e.tickerSource === 'zone' && !e.hidden) ?? false;
}

export class ZoneTickerOverlay {
  private ticker: Ticker | null = null;
  private sig = '';

  /** el is positioned over the content by the page (left and right 0, above the stage). */
  constructor(private el: HTMLElement) {}

  /**
   * Shows the ticker (null or empty text: hidden). scale = height of the screen / 1080: the
   * template sizes refer to a Full HD screen. The animation keeps running when only the text
   * changes.
   */
  update(tk: ScheduleTicker | null, scale: number): void {
    const el = this.el;
    const show = !!tk && tk.text.trim() !== '';
    el.classList.toggle('hidden', !show);
    if (!tk || !show) {
      this.ticker?.stop();
      return;
    }
    const tpl = tk.template;
    const sig = JSON.stringify(tpl);
    el.style.setProperty('--s', String(scale));
    el.style.height = `${tpl.height * scale}px`;
    el.style.top = tpl.position === 'top' ? '0' : '';
    el.style.bottom = tpl.position === 'bottom' ? '0' : '';
    el.style.background = tpl.background;
    if (sig !== this.sig || !this.ticker) {
      this.ticker?.stop();
      el.replaceChildren();
      this.ticker = createTicker(
        el,
        { font: tpl.font, size: tpl.size, color: tpl.color, bold: false, italic: false, underline: false, strike: false, letterSpacing: 0, lineHeight: 1, align: 'left' },
        tpl.speed,
        tpl.direction,
        true,
      );
      this.sig = sig;
    }
    this.ticker.setText(tk.text);
    this.ticker.start();
  }

  stop(): void {
    this.ticker?.stop();
  }
}
