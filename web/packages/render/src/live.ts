import type { LiveState, Money, LiveProduct, ScheduleTicker } from '@opensignage/contracts';

/** Observable live data shared by all elements of a renderer instance. */
export class LiveStore {
  state: LiveState | null = null;
  prices: Record<string, Money> = {};
  /** Ticker of the active time schedule rule (null when none). */
  ticker: ScheduleTicker | null = null;
  /** Server time minus local time in ms (players without RTC). */
  clockOffsetMs = 0;
  timezone = 'UTC';
  language = 'en';
  private listeners = new Set<() => void>();

  set(state: LiveState | null, prices: Record<string, Money> = {}, ticker: ScheduleTicker | null = null): void {
    this.state = state;
    this.prices = prices;
    this.ticker = ticker;
    this.emit();
  }

  setClock(offsetMs: number, timezone: string): void {
    this.clockOffsetMs = offsetMs;
    this.timezone = timezone || 'UTC';
    this.emit();
  }

  setLanguage(lang: string): void {
    this.language = lang;
    this.emit();
  }

  now(): Date {
    return new Date(Date.now() + this.clockOffsetMs);
  }

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  private emit(): void {
    for (const fn of this.listeners) {
      try {
        fn();
      } catch (e) {
        console.error('live listener failed', e);
      }
    }
  }

  product(id: string): LiveProduct | undefined {
    return this.state?.products.find((p) => p.id === id);
  }

  /** Effective price: schedule override, else base price. */
  price(p: LiveProduct): Money {
    return this.prices[p.id] ?? p.price;
  }
}

export function formatMoney(m: Money, lang: string): string {
  try {
    return new Intl.NumberFormat(lang, { style: 'currency', currency: m.currency }).format(m.amount / 100);
  } catch {
    return `${(m.amount / 100).toFixed(2)} ${m.currency}`;
  }
}

/** "0.33" + "l" → "0,33 l" in German etc. */
export function formatQuantity(q: { value: string; unit: string } | undefined, lang: string): string {
  if (!q || !q.value) return '';
  const n = Number(q.value);
  const v = Number.isFinite(n) ? new Intl.NumberFormat(lang, { maximumFractionDigits: 3 }).format(n) : q.value;
  return `${v} ${q.unit}`.trim();
}
