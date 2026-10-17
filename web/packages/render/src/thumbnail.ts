import type { Slide } from '@opensignage/contracts';
import type { ElementHandle, RenderEnv } from './env';
import { renderElement } from './elements';
import { injectRenderCss } from './css';
import { backgroundSignature } from './stage';
import { color, round } from './units';

/**
 * Renders a static slide preview into host (editor slide rail, presentation cards).
 * Uses the same element renderer as the player; videos show their poster.
 * Returns a cleanup function.
 */
export function renderThumbnail(host: HTMLElement, slide: Slide, width: number, height: number, env: RenderEnv): () => void {
  injectRenderCss(host.ownerDocument);
  const box = host.getBoundingClientRect();
  const s = Math.min((box.width || 160) / width, (box.height || 90) / height);
  const canvas = document.createElement('div');
  canvas.className = 'os-canvas';
  canvas.style.position = 'relative';
  canvas.style.left = '0';
  canvas.style.top = '0';
  canvas.style.margin = '0 auto';
  canvas.style.width = `${round(width * s)}px`;
  canvas.style.height = `${round(height * s)}px`;
  canvas.style.setProperty('--s', String(round(s * 10000) / 10000));
  const bg = document.createElement('div');
  bg.className = 'os-bg';
  const b = slide.background;
  bg.style.backgroundColor = color(b.color, '#000');
  bg.dataset.sig = backgroundSignature(b);
  if (b.type === 'gradient') {
    bg.style.backgroundImage = `linear-gradient(${b.angle}deg, ${b.stops.map((x) => `${color(x.color)} ${x.at * 100}%`).join(', ')})`;
  } else if (b.type === 'image' || b.type === 'video') {
    const url = env.assets.url(b.assetId, b.type === 'video' ? 'poster' : 'small');
    if (url) bg.style.backgroundImage = `url("${url}")`;
    bg.style.backgroundSize = b.fit === 'fill' ? '100% 100%' : b.fit;
  }
  canvas.appendChild(bg);
  const handles: ElementHandle[] = [];
  for (const e of slide.elements) {
    const h = renderElement(e, { ...env, mode: 'thumb' });
    handles.push(h);
    canvas.appendChild(h.el);
  }
  host.replaceChildren(canvas);
  return () => {
    for (const h of handles) h.destroy();
    canvas.remove();
  };
}
