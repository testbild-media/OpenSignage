// OpenSignage player: runs in the kiosk browser, loads ONLY from the local agent
// (http://127.0.0.1:7600). It never talks to the server and never sees credentials.
import './player.css';
import logoUrl from '../../../../branding/logo_horizontal.svg';
import type {
  AgentToPlayer,
  FontManifest,
  Manifest,
  PlayerConfig,
  PlayerMode,
  PlayerToAgent,
  Presentation,
  Slide,
} from '@opensignage/contracts';
import {
  LiveStore,
  Sequencer,
  Stage,
  ZoneTickerOverlay,
  contentFromManifest,
  customFontCss,
  fontFaceCss,
  installStyle,
  zoneTickerSuppressed,
  type AssetResolver,
  type RenderEnv,
} from '@opensignage/render';
import { setLanguage, t, restrictNamespaces } from '@opensignage/i18n';
import { createNoise } from './noise';

restrictNamespaces(['common', 'player']);

const $ = (id: string): HTMLElement => document.getElementById(id)!;

const live = new LiveStore();
const stage = new Stage($('stage'), 'contain');
const noise = createNoise($('noise'));
let manifest: Manifest | null = null;
let bundleHash = '';
let mode: PlayerMode | null = null;
let config: PlayerConfig | null = null;
let currentSlide: Slide | null = null;
let lastItem = '';
let ws: WebSocket | null = null;
// The playlist the schedule wants; played whenever the mode is "playing" (also after blackout),
// synchronously from its anchor and as the track of this screen's place.
let wantedPlaylist: string | null = null;
let wantedAnchor: number | null = null;
let wantedPosition = 1;
const playWanted = (): void => sequencer.play(wantedPlaylist, wantedAnchor, wantedPosition);

const assets: AssetResolver = {
  url(id, purpose) {
    const v = purpose === 'display' || purpose === 'small' ? '' : `?variant=${purpose}`; // screens never draw thumbnails
    return `/assets/${encodeURIComponent(id)}${v}`;
  },
};

const report = (level: 'warn' | 'error', msg: string): void => send({ t: 'log', p: { level: level === 'warn' ? 'warn' : 'error', msg } });

const envFor = (p: Presentation | null): RenderEnv => ({
  typography: p?.typography ?? (manifest?.presentations[0]?.typography as Presentation['typography']),
  assets,
  live,
  mode: 'play',
  report,
});

const sequencer = new Sequencer(stage, envFor, {
  onSlide(slide, description) {
    currentSlide = slide;
    lastItem = description;
    updateZoneTicker();
  },
  report,
  now: () => live.now().getTime(), // corrected clock: every screen of the zone switches together
});

// ---------------------------------------------------------------- zone ticker overlay

const zoneTicker = new ZoneTickerOverlay($('zone-ticker'));

function updateZoneTicker(): void {
  const tk = live.ticker; // ticker of the active time schedule rule
  const show = mode?.kind === 'playing' && !zoneTickerSuppressed(currentSlide);
  zoneTicker.update(show ? tk : null, window.innerHeight / 1080);
}

// ---------------------------------------------------------------- system screens

function pinBoxes(pin: string): string {
  return `<div class="pin">${[...pin].map((d) => `<span>${escapeHtml(d)}</span>`).join('')}</div>`;
}

function addresses(m: PlayerMode): string {
  const ips = m.ips.length ? m.ips.map((ip) => `<b>${escapeHtml(ip)}</b>`).join(' · ') : '—';
  return `<div class="addr">IP ${ips}<br>${escapeHtml(t('player.ip.webgui'))}: <b>${escapeHtml(m.webguiUrl)}</b></div>`;
}

function renderScreen(): void {
  const el = $('screen');
  const m = mode;
  if (!m || m.kind === 'playing' || m.kind === 'cleared' || m.kind === 'blackout' || m.kind === 'regeneration') {
    el.classList.add('hidden');
    return;
  }
  const logo = `<img class="logo" src="${logoUrl}" alt="OpenSignage">`;
  const notice = m.notice ? `<p class="warn">${escapeHtml(m.notice)}</p>` : '';
  let body = '';
  switch (m.kind) {
    case 'pairing':
      body = `<h1>${escapeHtml(t('player.pairing.title'))}</h1>${pinBoxes(m.pin ?? '------')}
        <p>${escapeHtml(t('player.pairing.hint'))}</p>${addresses(m)}
        ${m.fingerprint ? `<div class="fp">${escapeHtml(t('player.pairing.server', { fingerprint: m.fingerprint }))}</div>` : ''}`;
      break;
    case 'searching':
      body = `<div class="spinner"></div><h1>${escapeHtml(t('player.searching.title'))}</h1>
        <p>${escapeHtml(t('player.searching.hint'))}</p>${addresses(m)}`;
      break;
    case 'standby':
      body = `<h1>${escapeHtml(t('player.standby.title'))}</h1><p>${escapeHtml(t('player.standby.hint'))}</p>
        ${m.deviceName ? `<div class="addr"><b>${escapeHtml(m.deviceName)}</b></div>` : ''}${addresses(m)}`;
      break;
    case 'updating':
      body = `<div class="spinner"></div><h1>${escapeHtml(t('player.updating'))}</h1>`;
      break;
  }
  el.innerHTML = logo + body + notice;
  el.classList.remove('hidden');
}

function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!);
}

// ---------------------------------------------------------------- IP overlay (every start)

let ipShownAt = 0;
function showIpOverlay(): void {
  // Wait for the device config (sent right after the mode): the overlay can be disabled.
  if (!mode || !config || ipShownAt || !config.showIpOverlay) return;
  if (mode.kind !== 'playing' && mode.kind !== 'cleared' && mode.kind !== 'blackout') return; // system screens show IPs anyway
  ipShownAt = Date.now();
  const el = $('ip');
  const ips = mode.ips.map((ip) => `<b>${escapeHtml(ip)}</b>`).join('<br>');
  el.innerHTML = `${ips}<br>${escapeHtml(t('player.ip.webgui'))}: ${escapeHtml(mode.webguiUrl)}`;
  el.classList.remove('hidden');
  window.setTimeout(() => el.classList.add('hidden'), 12_000);
}

// ---------------------------------------------------------------- state handling

// The version notice is translated when shown and again after a language change (the
// notice may arrive before the device language is loaded).
let versionNotice = false;
function renderNotice(): void {
  const el = $('notice');
  const show = versionNotice && mode?.kind !== 'regeneration'; // nothing but noise then
  el.textContent = show ? t('player.notice.version') : '';
  el.classList.toggle('hidden', !show);
}

function applyMode(m: PlayerMode): void {
  const langChanged = m.language !== mode?.language;
  mode = m;
  const finish = (): void => {
    renderScreen();
    $('black').classList.toggle('hidden', m.kind !== 'blackout');
    // Plasma regeneration: the content stops (frees the CPU/GPU for the noise) and starts
    // again from the beginning when it is switched off.
    if (m.kind === 'regeneration') noise.start();
    else noise.stop();
    const idle = ['cleared', 'pairing', 'searching', 'standby', 'regeneration'].includes(m.kind);
    if (m.kind === 'playing') playWanted();
    else if (idle) sequencer.play(null);
    updateZoneTicker();
    showIpOverlay();
    renderNotice();
  };
  if (langChanged) {
    live.setLanguage(m.language);
    setLanguage(m.language).then(finish, finish);
  } else {
    finish();
  }
}

function applyBundle(hash: string, m: Manifest | null): void {
  if (hash === bundleHash) return; // never reset timers without a real change
  bundleHash = hash;
  manifest = m;
  if (m) {
    installStyle('os-custom-fonts', customFontCss(m.fonts, (f) => `/fonts/custom/${f.sha256}`));
    live.setClock(live.clockOffsetMs, m.timezone);
    sequencer.setContent(contentFromManifest(m));
  } else {
    sequencer.setContent({ presentations: new Map(), playlists: new Map(), media: new Map() });
  }
}

function handle(msg: AgentToPlayer): void {
  switch (msg.t) {
    case 'mode':
      applyMode(msg.p);
      break;
    case 'config':
      config = msg.p;
      stage.setScaleMode(msg.p.scaleMode);
      stage.setPlasmaShift(msg.p.plasmaShift);
      live.setClock(msg.p.clockOffsetMs, msg.p.timezone || manifest?.timezone || 'UTC');
      showIpOverlay();
      break;
    case 'bundle':
      applyBundle(msg.p.hash, msg.p.manifest);
      break;
    case 'source':
      wantedPlaylist = msg.p.playlistId;
      wantedAnchor = msg.p.anchor ?? null;
      wantedPosition = msg.p.position && msg.p.position > 0 ? msg.p.position : 1;
      if (mode?.kind === 'playing') playWanted();
      break;
    case 'live':
      live.set(msg.p.state, msg.p.prices, msg.p.ticker ?? null);
      updateZoneTicker();
      break;
    case 'cmd':
      if (msg.p.name === 'reload') location.reload();
      break;
    case 'versionNotice':
      versionNotice = !!msg.p.text;
      renderNotice();
      break;
    case 'networkNotice': {
      // Stays until the change is confirmed by a new login (or the device restarts).
      const el = $('netnotice');
      el.textContent = msg.p.text ?? '';
      el.classList.toggle('hidden', !msg.p.text);
      break;
    }
  }
}

function send(msg: PlayerToAgent): void {
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg));
}

function connect(): void {
  ws = new WebSocket(`ws://${location.host}/ws`);
  ws.onmessage = (ev) => {
    try {
      handle(JSON.parse(String(ev.data)) as AgentToPlayer);
    } catch (e) {
      console.error('bad message from agent', e);
    }
  };
  ws.onclose = () => {
    ws = null;
    window.setTimeout(connect, 2000); // content keeps playing meanwhile
  };
}

// Heartbeat for the agent watchdog (no ping for 30 s → the agent restarts the browser).
window.setInterval(() => {
  const mem = (performance as unknown as { memory?: { usedJSHeapSize: number } }).memory;
  send({ t: 'ping', p: { heap: mem?.usedJSHeapSize ?? 0, item: lastItem, fps: 0 } });
}, 5000);

window.addEventListener('error', (e) => report('error', `${e.message} @ ${e.filename}:${e.lineno}`));
window.addEventListener('unhandledrejection', (e) => report('error', `unhandled: ${String(e.reason)}`));

async function boot(): Promise<void> {
  try {
    const res = await fetch('/fonts/bundled/manifest.json', { cache: 'no-cache' }); // updates add families
    if (res.ok) installStyle('os-fonts', fontFaceCss((await res.json()) as FontManifest, '/fonts/bundled'));
    else report('warn', `font manifest: HTTP ${res.status}`);
  } catch (e) {
    report('warn', `font manifest: ${String(e)}`);
  }
  connect();
}

void boot();
