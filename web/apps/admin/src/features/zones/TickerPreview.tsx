// Live preview of a zone ticker template, rendered by the same engine as the player
// (@opensignage/render createTicker) on a 16:9 screen scaled from 1920×1080.
import { type CSSProperties, useEffect, useRef, useState } from 'react';
import type { TickerTemplate } from '@opensignage/contracts';
import { color, createTicker, injectRenderCss } from '@opensignage/render';
import { loadCustomFonts } from '../../lib/fonts';

export function TickerPreview({ template, text, animate, label }: { template: TickerTemplate; text: string; animate: boolean; label: string }) {
  const screen = useRef<HTMLDivElement>(null);
  const host = useRef<HTMLDivElement>(null);
  const [scale, setScale] = useState(0);

  useEffect(() => {
    injectRenderCss();
    const el = screen.current;
    if (!el) return;
    const measure = (): void => setScale(el.getBoundingClientRect().height / 1080);
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const sig = JSON.stringify(template);
  useEffect(() => {
    const el = host.current;
    if (!el || scale <= 0) return;
    const tpl = JSON.parse(sig) as TickerTemplate;
    let cancelled = false;
    let ticker: ReturnType<typeof createTicker> | null = null;
    const make = (): void => {
      if (cancelled) return;
      el.replaceChildren();
      ticker = createTicker(
        el,
        { font: tpl.font, size: tpl.size, color: tpl.color, bold: false, italic: false, underline: false, strike: false, letterSpacing: 0, lineHeight: 1, align: 'left' },
        tpl.speed,
        tpl.direction,
        animate,
      );
      ticker.setText(text);
      ticker.start();
    };
    // Measure only after the font is loaded (uploaded fonts need their @font-face rules
    // first), otherwise the loop width is wrong.
    const fonts = document.fonts;
    void loadCustomFonts()
      .then(() => fonts?.load?.(`${Math.round(tpl.size)}px "${tpl.font.replace(/"/g, '')}"`))
      .then(make, make);
    return () => {
      cancelled = true;
      ticker?.stop();
      el.replaceChildren();
    };
  }, [sig, text, animate, scale]);

  const style: CSSProperties & Record<'--s', string> = {
    '--s': String(scale || 0.0001),
    height: `${template.height * scale}px`,
    background: color(template.background),
    top: template.position === 'top' ? 0 : undefined,
    bottom: template.position === 'bottom' ? 0 : undefined,
  };
  return (
    <div className="ticker-screen" ref={screen} role="img" aria-label={label}>
      <div className="ticker-screen-content" />
      <div className="ticker-host" ref={host} style={style} />
    </div>
  );
}
