// Zone preview (GET /api/v1/zones/{id}/preview): what the screens of a zone show now or at a
// chosen moment. Pure helpers of the page.
import type { LiveProduct, LiveState, Manifest, Money, ScaleMode, ScheduleTicker } from '@opensignage/contracts';

export interface PreviewScreen {
  deviceId?: string;
  name?: string;
  online: boolean;
  groupId?: string;
  groupName?: string;
  position: number;
  width: number;
  height: number;
  scaleMode: ScaleMode;
  kind: 'closed' | 'rule' | 'cleared';
  playlistId?: string;
  ruleId?: string;
  since?: string;
}

export interface ZonePreview {
  zoneId: string;
  zoneName: string;
  at: string;
  timezone: string;
  zoneState: 'OPEN' | 'CLOSED';
  manifest: Manifest;
  live: LiveState;
  prices: Record<string, Money>;
  ticker: ScheduleTicker | null;
  activeRules: { id: string; name: string; scope: string }[];
  screens: PreviewScreen[];
}

export type StateChoice = '' | 'OPEN' | 'CLOSED';

/** Query of the preview: a moment ("2026-10-12T18:00" in the server's time zone, or an
 *  instant) and a chosen zone state ("" = as it is). */
export function previewQuery(at: string | null, state: StateChoice): string {
  const q = new URLSearchParams();
  if (at) q.set('at', at);
  if (state) q.set('state', state);
  const s = q.toString();
  return s ? `?${s}` : '';
}

/** Changes only when the content of the deployment changes (not its build time). */
export function manifestSignature(m: Manifest): string {
  const { id: _id, hash: _hash, builtAt: _built, ...content } = m;
  return JSON.stringify(content);
}

/** "2026-10-12T18:00" of the instant in the time zone (the value of a datetime-local input). */
export function inputValue(at: Date, timeZone: string): string {
  const parts = Object.fromEntries(
    new Intl.DateTimeFormat('en-GB', { timeZone, year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
      .formatToParts(at)
      .map((p) => [p.type, p.value]),
  );
  return `${parts.year}-${parts.month}-${parts.day}T${parts.hour}:${parts.minute}`;
}

/** Milliseconds until shortly after the next full minute of the (simulated) clock: windows of
 *  time schedules start and end on whole minutes. */
export function untilNextMinute(nowMs: number): number {
  return 60_000 - (nowMs % 60_000) + 500;
}

/** Products whose price a time schedule changes now: name, base price and price. */
export function priceChanges(prices: Record<string, Money>, products: LiveProduct[]): { id: string; name: string; base: Money; price: Money }[] {
  return products
    .filter((p) => prices[p.id] && p.status !== 'UNAVAILABLE' && (prices[p.id]!.amount !== p.price.amount || prices[p.id]!.currency !== p.price.currency))
    .map((p) => ({ id: p.id, name: p.name, base: p.price, price: prices[p.id]! }))
    .sort((a, b) => a.name.localeCompare(b.name));
}

/** What is sold out in the zone: products, and single variants as "Product – Variant". */
export function soldOutNames(products: LiveProduct[]): string[] {
  const out: string[] = [];
  for (const p of [...products].sort((a, b) => a.name.localeCompare(b.name))) {
    if (p.status === 'SOLD_OUT') out.push(p.name);
    else if (p.status === 'AVAILABLE') for (const v of p.variants ?? []) if (v.status === 'SOLD_OUT') out.push(`${p.name} – ${v.name}`);
  }
  return out;
}

/** Names of the playlists of the deployment by id. */
export function playlistNames(m: Manifest): Map<string, string> {
  return new Map(m.playlists.map((p) => [p.id, p.name]));
}
