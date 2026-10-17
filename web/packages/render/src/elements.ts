import type {
  ClockElement,
  Fit,
  ImageElement,
  RichDoc,
  ShapeElement,
  SlideElement,
  TextElement,
  TickerElement,
  VideoElement,
  AudioElement,
} from '@opensignage/contracts';
import type { ElementHandle, RenderEnv } from './env';
import { noopHandle } from './env';
import { renderProduct, renderProductList } from './productlist';
import { applyMarks, applyTextStyle, color, fpx, px, round } from './units';
import { createTicker } from './ticker';
import { watchVideo } from './video';

/** Places an element box (position, size, rotation, opacity) in design pixels. */
export function placeBox(el: HTMLElement, e: SlideElement): void {
  const st = el.style;
  st.left = px(e.x);
  st.top = px(e.y);
  st.width = px(e.w);
  st.height = px(e.h);
  if (e.rotation) st.transform = `rotate(${round(e.rotation)}deg)`;
  st.opacity = String(e.opacity ?? 1);
  if (e.hidden) st.display = 'none';
}

/** Renders one element including its box. */
export function renderElement(e: SlideElement, env: RenderEnv): ElementHandle {
  let h: ElementHandle;
  try {
    h = renderInner(e, env);
  } catch (err) {
    env.report?.('error', `element ${e.id} (${e.type}) failed: ${String(err)}`);
    h = noopHandle(document.createElement('div'));
  }
  h.el.classList.add('os-el');
  h.el.dataset.elementId = e.id;
  placeBox(h.el, e);
  return h;
}

function renderInner(e: SlideElement, env: RenderEnv): ElementHandle {
  switch (e.type) {
    case 'text':
      return renderText(e, env);
    case 'image':
      return renderImage(e, env);
    case 'video':
      return renderVideo(e, env);
    case 'audio':
      return renderAudio(e, env);
    case 'shape':
      return noopHandle(renderShape(e));
    case 'productList':
      return renderProductList(e, env);
    case 'product':
      return renderProduct(e, env);
    case 'clock':
      return renderClock(e, env);
    case 'ticker':
      return renderTickerElement(e, env);
  }
}

/** Renders a rich text document into container (shared by editor read-only view). */
export function renderRichDoc(container: HTMLElement, doc: RichDoc): void {
  container.replaceChildren();
  let number = 0;
  for (const para of doc.paragraphs) {
    const p = document.createElement('div');
    p.className = 'os-p';
    if (para.align) p.style.textAlign = para.align;
    if (para.bullet === 'disc' || para.bullet === 'number') {
      number = para.bullet === 'number' ? number + 1 : 0;
      const b = document.createElement('span');
      b.className = 'os-bullet';
      b.textContent = para.bullet === 'disc' ? '•' : `${number}.`;
      p.appendChild(b);
    } else {
      number = 0;
    }
    if (para.runs.length === 0 || para.runs.every((r) => r.text === '')) {
      p.appendChild(document.createElement('br'));
    }
    for (const run of para.runs) {
      if (!run.text) continue;
      const span = document.createElement('span');
      span.textContent = run.text;
      applyMarks(span, run.marks);
      p.appendChild(span);
    }
    container.appendChild(p);
  }
}

function renderText(e: TextElement, _env: RenderEnv): ElementHandle {
  const el = document.createElement('div');
  el.className = 'os-text';
  el.dataset.valign = e.valign;
  applyTextStyle(el, e.style);
  if (e.fill) el.style.backgroundColor = color(e.fill);
  const pad = e.padding ?? [0, 0, 0, 0];
  el.style.padding = pad.map(px).join(' ');
  renderRichDoc(el, e.doc);
  let frame = 0;
  const fit = (): void => {
    if (!e.autoShrink) return;
    // Binary search a font factor so the text fits the box (after fonts are loaded).
    el.style.setProperty('--fit', '1');
    if (el.scrollHeight <= el.clientHeight + 1) return;
    let lo = 0.2;
    let hi = 1;
    for (let i = 0; i < 8; i++) {
      const mid = (lo + hi) / 2;
      el.style.setProperty('--fit', String(round(mid)));
      if (el.scrollHeight <= el.clientHeight + 1) lo = mid;
      else hi = mid;
    }
    el.style.setProperty('--fit', String(round(lo)));
  };
  const schedule = (): void => {
    cancelAnimationFrame(frame);
    frame = requestAnimationFrame(fit);
  };
  if (e.autoShrink) {
    document.fonts?.ready.then(schedule).catch(() => schedule());
  }
  return {
    el,
    start: schedule,
    stop() {},
    destroy() {
      cancelAnimationFrame(frame);
    },
  };
}

export function objectFit(fit: Fit): string {
  return fit === 'fill' ? 'fill' : fit;
}

function renderImage(e: ImageElement, env: RenderEnv): ElementHandle {
  const img = document.createElement('img');
  img.decoding = 'async';
  img.draggable = false;
  img.style.objectFit = objectFit(e.fit);
  const url = env.assets.url(e.assetId, env.mode === 'thumb' ? 'small' : 'display');
  if (url) img.src = url;
  else env.report?.('warn', `image asset ${e.assetId} not available`);
  const wrap = document.createElement('div');
  wrap.appendChild(img);
  return noopHandle(wrap);
}

function renderVideo(e: VideoElement, env: RenderEnv): ElementHandle {
  const wrap = document.createElement('div');
  if (env.mode !== 'play') {
    const img = document.createElement('img');
    img.style.objectFit = objectFit(e.fit);
    const url = env.assets.url(e.assetId, 'poster');
    if (url) img.src = url;
    wrap.appendChild(img);
    return noopHandle(wrap);
  }
  const v = createVideo(env.assets.url(e.assetId, 'display'), e.fit, e.loop ?? true, e.muted ?? true);
  wrap.appendChild(v);
  const watch = watchVideo(v, { loop: e.loop ?? true });
  return {
    el: wrap,
    video: v,
    start() {
      watch.start();
    },
    stop() {
      watch.stop();
      v.pause();
    },
    destroy() {
      watch.stop();
      releaseVideo(v);
    },
  };
}

function renderAudio(e: AudioElement, env: RenderEnv): ElementHandle {
  const wrap = document.createElement('div');
  if (env.mode !== 'play') return noopHandle(wrap);
  const a = document.createElement('audio');
  const url = env.assets.url(e.assetId, 'display');
  if (url) a.src = url;
  a.loop = e.loop ?? false;
  a.volume = Math.max(0, Math.min(1, e.volume));
  a.preload = 'auto';
  wrap.appendChild(a);
  return {
    el: wrap,
    start() {
      a.play().catch((err) => env.report?.('warn', `audio play failed: ${String(err)}`));
    },
    stop() {
      a.pause();
    },
    destroy() {
      a.pause();
      a.removeAttribute('src');
      a.load();
    },
  };
}

export function createVideo(url: string | null, fit: Fit, loop: boolean, muted: boolean): HTMLVideoElement {
  const v = document.createElement('video');
  v.muted = muted;
  v.defaultMuted = muted;
  v.loop = loop;
  v.playsInline = true;
  v.preload = 'auto';
  v.disablePictureInPicture = true;
  v.style.objectFit = objectFit(fit);
  if (url) v.src = url;
  return v;
}

/** Frees decoder memory (important on the Pi: a paused video still holds buffers). */
export function releaseVideo(v: HTMLVideoElement): void {
  try {
    v.pause();
    v.removeAttribute('src');
    v.load();
  } catch {
    // element already detached; nothing left to free
  }
}

const SVG = 'http://www.w3.org/2000/svg';

function renderShape(e: ShapeElement): HTMLElement {
  const wrap = document.createElement('div');
  const stroke = e.stroke;
  if (e.shape === 'rect' || e.shape === 'ellipse') {
    wrap.className = 'os-shape';
    const st = wrap.style;
    st.width = '100%';
    st.height = '100%';
    if (e.fill) st.backgroundColor = color(e.fill);
    if (stroke.width > 0) st.border = `${px(stroke.width)} ${stroke.dash} ${color(stroke.color)}`;
    st.borderRadius = e.shape === 'ellipse' ? '50%' : px(e.radius ?? 0);
    return wrap;
  }
  // line / arrow: SVG in design units; the box is scaled uniformly so strokes stay true.
  const svg = document.createElementNS(SVG, 'svg');
  svg.setAttribute('class', 'os-svg');
  svg.setAttribute('viewBox', `0 0 ${round(e.w)} ${round(e.h)}`);
  svg.setAttribute('preserveAspectRatio', 'none');
  const y = round(e.h / 2);
  const sw = Math.max(0.5, stroke.width);
  const head = e.shape === 'arrow' ? Math.min(e.w / 2, sw * 4) : 0;
  const line = document.createElementNS(SVG, 'line');
  line.setAttribute('x1', '0');
  line.setAttribute('y1', String(y));
  line.setAttribute('x2', String(round(e.w - head * 0.9)));
  line.setAttribute('y2', String(y));
  line.setAttribute('stroke', color(stroke.color));
  line.setAttribute('stroke-width', String(round(sw)));
  if (stroke.dash === 'dashed') line.setAttribute('stroke-dasharray', `${round(sw * 3)} ${round(sw * 2)}`);
  if (stroke.dash === 'dotted') {
    line.setAttribute('stroke-dasharray', `0 ${round(sw * 2)}`);
    line.setAttribute('stroke-linecap', 'round');
  }
  svg.appendChild(line);
  if (head > 0) {
    const poly = document.createElementNS(SVG, 'polygon');
    const hw = sw * 2.5;
    poly.setAttribute('points', `${round(e.w)},${y} ${round(e.w - head)},${round(y - hw)} ${round(e.w - head)},${round(y + hw)}`);
    poly.setAttribute('fill', color(stroke.color));
    svg.appendChild(poly);
  }
  wrap.appendChild(svg);
  return wrap;
}

function clockText(e: ClockElement, env: RenderEnv): string[] {
  const now = env.live.now();
  const lang = env.live.language;
  const tz = env.live.timezone;
  const hour12 = e.format.startsWith('h');
  const opts: Intl.DateTimeFormatOptions = { hour: hour12 ? 'numeric' : '2-digit', minute: '2-digit', hour12, timeZone: tz };
  if (e.format.includes(':ss')) opts.second = '2-digit';
  const lines: string[] = [];
  try {
    lines.push(new Intl.DateTimeFormat(lang, opts).format(now));
    if (e.date !== 'none') {
      lines.push(
        new Intl.DateTimeFormat(lang, e.date === 'short' ? { dateStyle: 'medium', timeZone: tz } : { dateStyle: 'full', timeZone: tz }).format(now),
      );
    }
  } catch {
    lines.push(now.toISOString().slice(11, 16));
  }
  return lines;
}

function renderClock(e: ClockElement, env: RenderEnv): ElementHandle {
  const el = document.createElement('div');
  el.className = 'os-clock';
  el.dataset.valign = e.valign;
  el.style.justifyContent = e.valign === 'top' ? 'flex-start' : e.valign === 'bottom' ? 'flex-end' : 'center';
  applyTextStyle(el, e.style);
  const time = document.createElement('div');
  const date = document.createElement('div');
  date.style.fontSize = fpx(e.style.size * 0.45);
  el.append(time, date);
  const update = (): void => {
    const [t, d] = clockText(e, env);
    if (time.textContent !== t) time.textContent = t ?? '';
    if (date.textContent !== (d ?? '')) date.textContent = d ?? '';
    date.style.display = d ? '' : 'none';
  };
  update();
  let timer = 0;
  const tick = (): void => {
    update();
    const ms = 1000 - (env.live.now().getTime() % 1000) + 5;
    timer = window.setTimeout(tick, ms);
  };
  const unsub = env.live.subscribe(update);
  return {
    el,
    start() {
      clearTimeout(timer);
      tick();
    },
    stop() {
      clearTimeout(timer);
    },
    destroy() {
      clearTimeout(timer);
      unsub();
    },
  };
}

function renderTickerElement(e: TickerElement, env: RenderEnv): ElementHandle {
  const el = document.createElement('div');
  if (e.fill) el.style.backgroundColor = color(e.fill);
  // Source 'zone': the ticker of the active time schedule rule.
  const textFor = (): string => (e.tickerSource === 'zone' ? (env.live.ticker?.text ?? '') : (e.text ?? ''));
  const ticker = createTicker(el, e.style, e.speed, e.direction, env.mode === 'play');
  // On screens the element disappears with its bar while no rule shows a ticker, like the
  // ticker overlay. The editor keeps showing it for layout work.
  const sync = (): void => {
    ticker.setText(textFor());
    if (e.tickerSource === 'zone' && env.mode === 'play') {
      const tk = env.live.ticker;
      el.style.visibility = tk && tk.text.trim() !== '' ? '' : 'hidden';
    }
  };
  sync();
  const unsub = e.tickerSource === 'zone' ? env.live.subscribe(sync) : () => {};
  return {
    el,
    start() {
      ticker.start();
    },
    stop() {
      ticker.stop();
    },
    destroy() {
      ticker.stop();
      unsub();
    },
  };
}
