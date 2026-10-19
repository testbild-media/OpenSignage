// TypeScript mirror of internal/contracts (Go is the source of truth, see ARCHITECTURE §5).
// Keep both in sync in the same commit; testdata/contracts fixtures are checked by both.

export type Color = string; // #rrggbb or #rrggbbaa

export interface Money {
  amount: number; // minor units
  currency: string;
}

export type ProductStatus = 'AVAILABLE' | 'UNAVAILABLE' | 'SOLD_OUT';
export type ZoneState = 'OPEN' | 'CLOSED';
export type Role = 'admin' | 'operator' | 'user';
export type Fit = 'contain' | 'cover' | 'fill';
export type ScaleMode = 'contain' | 'cover' | 'none';
export type HAlign = 'left' | 'center' | 'right' | 'justify';
export type VAlign = 'top' | 'middle' | 'bottom';

// ---------------------------------------------------------------- slide document

export interface Outline {
  color: Color;
  width: number;
}
export interface Shadow {
  color: Color;
  dx: number;
  dy: number;
}

export interface TextStyle {
  font: string;
  size: number;
  color: Color;
  bold: boolean;
  italic: boolean;
  underline: boolean;
  strike: boolean;
  highlight?: Color;
  letterSpacing: number;
  lineHeight: number;
  align: HAlign;
  uppercase?: boolean;
  outline?: Outline;
  shadow?: Shadow;
}

export interface Marks {
  font?: string;
  size?: number;
  color?: Color;
  bold?: boolean;
  italic?: boolean;
  underline?: boolean;
  strike?: boolean;
  highlight?: Color;
  letterSpacing?: number;
}

export interface Run {
  text: string;
  marks?: Marks;
}
export interface Paragraph {
  align?: HAlign;
  bullet?: 'none' | 'disc' | 'number';
  runs: Run[];
}
export interface RichDoc {
  paragraphs: Paragraph[];
}

export interface Typography {
  heading: TextStyle;
  subheading: TextStyle;
  item: TextStyle;
  description: TextStyle;
  price: TextStyle;
}

export type Duration = { mode: 'fixed'; ms: number } | { mode: 'video'; maxMs?: number };

export interface GradientStop {
  color: Color;
  at: number;
}

export type Background =
  | { type: 'color'; color: Color }
  | { type: 'gradient'; color: Color; angle: number; stops: GradientStop[] }
  | { type: 'image'; color: Color; assetId: string; fit: Fit }
  | { type: 'video'; color: Color; assetId: string; fit: Fit };

export type TransitionType = 'cut' | 'fade' | 'slide' | 'zoom';
export type Direction = 'left' | 'right' | 'up' | 'down';
export interface Transition {
  type: TransitionType;
  ms: number;
  direction?: Direction;
}

export interface ElementBase {
  id: string;
  name: string;
  x: number;
  y: number;
  w: number;
  h: number;
  rotation: number;
  opacity: number;
  locked: boolean;
  hidden: boolean;
  pinned: boolean;
  groupId?: string;
}

export interface TextElement extends ElementBase {
  type: 'text';
  doc: RichDoc;
  style: TextStyle;
  valign: VAlign;
  padding?: [number, number, number, number];
  fill?: Color;
  autoShrink?: boolean;
}
export interface ImageElement extends ElementBase {
  type: 'image';
  assetId: string;
  fit: Fit;
}
export interface VideoElement extends ElementBase {
  type: 'video';
  assetId: string;
  fit: Fit;
  loop?: boolean;
  muted?: boolean;
}
export interface AudioElement extends ElementBase {
  type: 'audio';
  assetId: string;
  loop?: boolean;
  volume: number;
}
export interface Stroke {
  color: Color;
  width: number;
  dash: 'solid' | 'dashed' | 'dotted';
}
export interface ShapeElement extends ElementBase {
  type: 'shape';
  shape: 'rect' | 'ellipse' | 'line' | 'arrow';
  fill?: Color;
  stroke: Stroke;
  radius?: number;
}
export interface ProductFields {
  name: boolean;
  quantity: boolean;
  description: boolean;
  price: boolean;
  /** The product's variants below the name, like the description (absent = shown). */
  variants?: boolean;
}
export interface ProductOverrides {
  heading?: Marks;
  subheading?: Marks;
  item?: Marks;
  description?: Marks;
  price?: Marks;
}
export interface ProductListElement extends ElementBase {
  type: 'productList';
  source: { kind: 'all' | 'categories' | 'products'; ids: string[] };
  columns: number;
  fields: ProductFields;
  categoryHeadings?: boolean;
  subcategoryHeadings?: boolean;
  /** Hidden category headings keep their space (only with categoryHeadings off). */
  categorySpace?: boolean;
  /** Hidden subcategory headings keep their space (only with subcategoryHeadings off). */
  subcategorySpace?: boolean;
  scale: number;
  rowGap?: number;
  columnGap?: number;
  priceAlign: 'left' | 'right';
  overrides?: ProductOverrides;
}
export interface ProductElement extends ElementBase {
  type: 'product';
  productId: string;
  fields: ProductFields;
  scale: number;
  priceAlign: 'left' | 'right';
  overrides?: ProductOverrides;
}
export interface ClockElement extends ElementBase {
  type: 'clock';
  format: 'HH:mm' | 'HH:mm:ss' | 'h:mm a' | 'h:mm:ss a';
  date: 'none' | 'short' | 'long';
  style: TextStyle;
  valign: VAlign;
}
export interface TickerElement extends ElementBase {
  type: 'ticker';
  /** 'zone': the ticker of the active time schedule rule; 'text': own text. */
  tickerSource: 'text' | 'zone';
  text?: string;
  style: TextStyle;
  fill?: Color;
  speed: number;
  direction: 'left' | 'right';
}

export type SlideElement =
  | TextElement
  | ImageElement
  | VideoElement
  | AudioElement
  | ShapeElement
  | ProductListElement
  | ProductElement
  | ClockElement
  | TickerElement;

export type ElementType = SlideElement['type'];

export interface Slide {
  id: string;
  name: string;
  hidden: boolean;
  duration: Duration;
  background: Background;
  transition: Transition;
  elements: SlideElement[];
}

export interface Presentation {
  id: string;
  name: string;
  width: number;
  height: number;
  typography: Typography;
  slides: Slide[];
}

export const LIMITS = {
  maxSlides: 200,
  maxElementsPerSlide: 200,
  minDesignSize: 16,
  maxDesignSize: 7680,
  minSlideMs: 1000,
  maxSlideMs: 24 * 60 * 60 * 1000,
  defaultVideoMaxMs: 30 * 60 * 1000,
  maxTextRunes: 20000,
  /** Paragraphs per text element. */
  maxParagraphs: 1000,
  maxRunsPerParagraph: 500,
  /** Ids of a product list source (categories or products). */
  maxSourceIds: 500,
} as const;

// ---------------------------------------------------------------- playlists & rules

export type PlayMode = { mode: 'duration'; ms: number } | { mode: 'repeat'; count: number };

export interface PlaylistItem {
  id: string;
  kind: 'presentation' | 'slide' | 'media';
  refId: string;
  presentationId?: string;
  play: PlayMode;
  fit?: Fit;
  /** Tracks 2..screens of a playlist for several screens; null = like track 1. */
  cells?: (PlaylistCell | null)[];
}

/** Content of one track in one step: a slide or a medium. */
export interface PlaylistCell {
  kind: 'slide' | 'media';
  refId: string;
  presentationId?: string;
  fit?: Fit;
}

export interface Playlist {
  id: string;
  name: string;
  items: PlaylistItem[];
  /**
   * Number of tracks (absent/1 = every screen shows the same). The screen at place n plays
   * track n, a place beyond the tracks track 1; every step is a slide or a medium with a
   * fixed duration.
   */
  screens?: number;
}

/** Most tracks of a playlist for several screens. */
export const MAX_PLAYLIST_SCREENS = 16;

export interface Window {
  days: number[]; // ISO weekday of the window start (1 = Monday)
  start: string; // HH:MM
  end: string; // HH:MM; <= start crosses midnight
  /** Period of dates (YYYY-MM-DD, server time zone, inclusive; absent = open): an occurrence
   *  counts when it starts on a day of the period. */
  from?: string;
  until?: string;
}

/**
 * Price of one product, or of every product of a category and its subcategories (exactly one
 * of productId and categoryId). Within a rule a product price comes before a subcategory
 * price, which comes before a category price.
 */
export interface PriceOverride {
  productId?: string;
  categoryId?: string;
  price: Money;
}

export interface Rule {
  id: string;
  revision: number;
  name: string;
  scope: 'zone' | 'global' | 'default';
  zoneId?: string;
  window?: Window;
  playlistId?: string;
  /** Playlists of single display groups of the zone ('ungrouped' = screens without group). */
  groupPlaylists?: GroupPlaylist[];
  prices: PriceOverride[];
  /** Ticker over the content of every screen of the zone while the rule is active. */
  ticker?: RuleTicker;
  enabled: boolean;
}

/** Targets the screens of a zone that are in no display group. */
export const NO_GROUP_ID = 'ungrouped';

export interface GroupPlaylist {
  groupId: string;
  playlistId: string;
}

/** Ticker of a time schedule rule; hide = hides the ticker of lower rules. */
export interface RuleTicker {
  hide?: boolean;
  text: string;
  /** Ticker template; empty = the zone's template (or the default). */
  templateId?: string;
}

/** The ticker the player shows: text of the active rule with its resolved style. */
export interface ScheduleTicker {
  text: string;
  template: TickerTemplate;
  ruleId: string;
}

// ---------------------------------------------------------------- deployment & live

export interface ManifestAsset {
  id: string;
  revision: number;
  kind: 'image' | 'video' | 'audio' | 'font';
  sha256: string;
  size: number;
  mime: string;
  variant: string;
  url: string;
  width?: number;
  height?: number;
  durationMs?: number;
}

export interface ManifestFontFile {
  url: string;
  sha256: string;
  size: number;
  weight: [number, number];
  style: 'normal' | 'italic';
  format: string;
}

export interface Manifest {
  id: string;
  zoneId: string;
  hash: string;
  builtAt: string;
  minAgentVersion: string;
  timezone: string;
  closedPlaylistId?: string;
  rules: Rule[];
  playlists: Playlist[];
  presentations: Presentation[];
  products: { imageAssetIds: string[] };
  assets: ManifestAsset[];
  fonts: { family: string; files: ManifestFontFile[] }[];
}

export interface TickerTemplate {
  font: string;
  size: number;
  color: Color;
  background: Color;
  speed: number;
  direction: 'left' | 'right';
  height: number; // design px of a 1080 px tall screen
  position: 'top' | 'bottom';
}

export interface LiveCategory {
  id: string;
  parentId?: string;
  name: string;
  sort: number;
}

export interface LiveProduct {
  id: string;
  name: string;
  description: string;
  categoryId: string;
  type: 'drink' | 'food' | 'other';
  quantity?: { value: string; unit: string };
  price: Money;
  status: ProductStatus;
  imageAssetId?: string;
  showImageInsteadOfName: boolean;
  /** Size of the image instead of the name, percent of the automatic size (absent = 100). */
  imageScale?: number;
  sort: number;
  /** Variants share the price and are shown like the description (absent = none). */
  variants?: LiveVariant[];
}

/** One variant of a product with its effective status (UNAVAILABLE ones are not shown). */
export interface LiveVariant {
  id: string;
  name: string;
  status: ProductStatus;
}

export interface LiveState {
  zone: { id: string; name: string; state: ZoneState };
  /** Always empty (tickers come from time schedule rules); kept for older players. */
  ticker: { text: string; visible: boolean; template: TickerTemplate };
  categories: LiveCategory[];
  products: LiveProduct[];
  serverTime: string;
}

// ---------------------------------------------------------------- device link (agent → server)

export interface DisplayConfig {
  width: number; // 0 = automatic (the screen's preferred mode)
  height: number;
  refresh: number;
  rotation: 0 | 90 | 180 | 270;
  custom: boolean; // force a mode the screen does not report
}

export interface ActiveSource {
  kind?: 'playlist' | 'closed' | 'cleared';
  playlistId?: string;
  ruleId?: string;
}

/** One resolution the screen reports (unrotated) with its refresh rates. */
export interface DisplayModeGroup {
  width: number;
  height: number;
  refresh: number[];
  preferred?: number; // refresh rate of the preferred mode at this size
  current?: number; // refresh rate of the current mode at this size
}

/** What the agent made of the last device.config (absent in older agents). */
export interface AppliedConfig {
  blackout: boolean;
  display: DisplayConfig; // last setting received from the server
  displayMode: DisplayConfig; // setting in effect on the device
  displayBusy?: boolean;
  displayError?: string;
}

/**
 * The `status` message of an agent (every 30 s). Every key is optional: older agents send
 * fewer, and the server stores whatever arrives.
 */
export interface DeviceNetIface {
  name: string;
  kind: 'ethernet' | 'wifi';
  connected: boolean;
  ipv4?: string[];
  method?: 'dhcp' | 'static';
  speedMbps?: number;
  ssid?: string;
  signalDbm?: number;
  freqMHz?: number;
}

export interface DeviceStatus {
  version?: string;
  os?: string;
  arch?: string;
  ips?: string[];
  mode?: PlayerModeKind;
  player?: 'ok' | 'starting' | 'crashed' | 'restarting' | string;
  playerItem?: string;
  playerRestarts?: number;
  activeSource?: ActiveSource;
  activeDeploymentId?: string;
  cpu?: number; // percent, all cores
  ramUsed?: number; // bytes
  ramTotal?: number;
  temperature?: number | null; // °C
  power?: { underVoltage: boolean; events: number; last?: string }; // Raspberry Pi supply
  /** Video decoding: hardware (path v4l2 | vaapi-vulkan) or software; fallback
   * "decode_errors" = "auto" switched to software after decode errors (until restart). */
  video?: {
    hw: boolean;
    path?: string;
    fallback?: string;
    fallbackAt?: string;
    /** What Chromium really renders with: hardware | software (never had the GPU) | lost (had it, restarts did not help). */
    gpu?: 'hardware' | 'software' | 'lost';
    gpuCrashes?: number;
    /** Player restarts that brought the GPU back (self-healing). */
    gpuRecoveries?: number;
    gpuLostAt?: string;
  };
  uptime?: number; // seconds since the client service started
  diskTotal?: number; // bytes of the file system holding the data
  diskFree?: number;
  cacheBytes?: number; // content cache
  logsBytes?: number;
  display?: { output?: string; width: number; height: number; refresh: number; rotation: number; modes?: DisplayModeGroup[] };
  sync?: { state: string; deploymentId?: string; done: number; total: number; error?: string };
  /** Wired and wireless interfaces (absent in older agents); pending: a change made in the
   * client WebGUI waits for its confirmation. */
  network?: { interfaces: DeviceNetIface[]; pending?: boolean };
  update?: { phase: string; version?: string; error?: string; done?: number; total?: number };
  applied?: AppliedConfig;
  caps?: string[];
  /** Hardware model, e.g. "Raspberry Pi 3 Model B Rev 1.2". */
  model?: string;
  /** Time used for synchronous playback: from the server, a LAN time leader or the own clock. */
  clock?: { source: 'server' | 'peer' | 'local'; offsetMs: number; rttMs: number; leader?: string; ageS: number };
}

/** Data of a clean_cache result; with dryRun only what would be deleted. */
export interface CacheCleanup {
  dryRun?: boolean;
  deleted: number;
  freedBytes: number;
  partial: number;
  partialBytes: number;
  cacheBytes: number;
}

// ---------------------------------------------------------------- player link (agent ↔ player)

export type PlayerModeKind =
  | 'searching'
  | 'pairing'
  | 'standby'
  | 'playing'
  | 'cleared'
  | 'blackout'
  | 'updating'
  | 'regeneration'; // plasma regeneration: full-screen white noise

export interface PlayerMode {
  kind: PlayerModeKind;
  pin?: string;
  ips: string[];
  webguiUrl: string;
  fingerprint?: string;
  deviceName?: string;
  serverAddress?: string;
  language: string;
  notice?: string;
}

export interface PlayerConfig {
  scaleMode: ScaleMode;
  perf: 'low' | 'standard' | 'high';
  plasmaShift: boolean;
  showIpOverlay: boolean;
  timezone: string;
  clockOffsetMs: number; // server time - local time
}

export type AgentToPlayer =
  | { t: 'mode'; p: PlayerMode }
  | { t: 'bundle'; p: { hash: string; manifest: Manifest | null } }
  | { t: 'source'; p: { playlistId: string | null; reason: string; anchor?: number | null; position?: number } }
  | { t: 'live'; p: { state: LiveState | null; prices: Record<string, Money>; ticker?: ScheduleTicker | null } }
  | { t: 'config'; p: PlayerConfig }
  | { t: 'cmd'; p: { name: 'reload' } }
  | { t: 'versionNotice'; p: { text: string | null } }
  | { t: 'networkNotice'; p: { text: string | null } };

export type PlayerToAgent =
  | { t: 'ping'; p: { heap: number; item: string; fps: number } }
  | { t: 'log'; p: { level: 'info' | 'warn' | 'error'; msg: string } };

// ---------------------------------------------------------------- fonts manifest (bundled)

export interface FontFileEntry {
  file: string;
  style: 'normal' | 'italic';
  weight: [number, number];
  width?: [number, number];
  format?: string;
  sha256: string;
  size: number;
}

export interface FontFamilyEntry {
  id: string;
  family: string;
  category: 'sans' | 'serif' | 'display' | 'condensed' | 'script' | 'mono' | 'custom';
  license: string;
  scripts: string[];
  files: FontFileEntry[];
}

export interface FontManifest {
  version: number;
  fallback: string;
  families: FontFamilyEntry[];
}
