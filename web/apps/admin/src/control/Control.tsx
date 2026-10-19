// Operator UI (/control): touch-optimized for tablets. After login the
// operator picks a zone (remembered per browser) and controls OPEN/CLOSED and product status
// (zone overrides). Screens and tickers are not operated here: blackout is
// on the device page, tickers come from time schedules and presentations. Every action locks
// its buttons until the server answered; the view only changes after confirmation. A
// banner warns whenever the view may be out of date.
import { type FormEvent, type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Ban, Check, ChevronDown, LayoutDashboard, Loader2, RefreshCw, RotateCcw, Search, Shapes, UtensilsCrossed, WifiOff, Wine, X } from 'lucide-react';
import type { Money, ProductStatus, ZoneState } from '@opensignage/contracts';
import { formatMoney, getLanguage } from '@opensignage/i18n';
import { errorText, post } from '../lib/api';
import { useAction, useQuery, useStoredState, useT } from '../lib/hooks';
import { startEvents, stopEvents, useConnected, useEvents } from '../lib/events';
import { roleAtLeast, SessionProvider, useSession } from '../lib/session';
import { updatedTo } from '../lib/serverWatch';
import { ErrorBoundary, FeedbackProvider, useConfirm } from '../components/feedback';
import { Spinner } from '../components/ui';
import iconUrl from '../../../../../branding/Icon.png';
import logoUrl from '../../../../../branding/logo_horizontal.svg';
import { decimalSeparator, formatQuantityValue } from '../features/products/money';
import { isTypeFilter, matchesType, TYPE_FILTER_LABELS, type TypeFilter, typeChips } from '../features/products/typeFilter';
import { AccountMenu } from './Account';

// ---------------------------------------------------------------- API shapes

interface LiveStatusEntry {
  id: string;
  status: ProductStatus; // effective
  override: ProductStatus | null;
}

export interface LiveZone {
  id: string;
  name: string;
  state: ZoneState;
  hasClosedPlaylist: boolean;
  products: LiveStatusEntry[];
  /** Status of every variant in the zone (id = variant id). */
  variants?: LiveStatusEntry[];
}

interface LiveVariant {
  id: string;
  name: string;
  status: ProductStatus; // global
}

interface LiveCategory {
  id: string;
  parentId?: string;
  name: string;
  sort: number;
}

interface LiveProduct {
  id: string;
  name: string;
  description: string;
  categoryId: string;
  type?: string; // drink | food | other
  quantity?: { value: string; unit: string };
  price: Money;
  status: ProductStatus; // global
  sort: number;
  variants?: LiveVariant[];
}

export interface LiveZones {
  zones: LiveZone[];
  categories: LiveCategory[];
  products: LiveProduct[];
  serverTime: string;
}

interface StatusResult {
  productId: string;
  variantId?: string;
  zoneId?: string;
  status: ProductStatus;
  override: ProductStatus | null;
  effective: ProductStatus;
}

// The operator view switches between available and sold out only. "Not available" means
// the product is not carried: an administrator decides it in the administration, and such
// products are not listed here at all.
const STATUSES: ProductStatus[] = ['AVAILABLE', 'SOLD_OUT'];
const ZONE_KEY = 'os.control.zone';
const TYPE_KEY = 'os.control.typeFilter';

/** Applies a confirmed status answer to the cached live data. */
/** Products carried in the zone: "not available" (effective, i.e. after the zone override)
 * means not carried, so the operator view does not list them at all. */
export function carriedProducts<P extends { id: string; status: ProductStatus }>(
  products: P[],
  zoneProducts: { id: string; status: ProductStatus }[],
): P[] {
  const eff = new Map(zoneProducts.map((p) => [p.id, p.status]));
  return products.filter((p) => (eff.get(p.id) ?? p.status) !== 'UNAVAILABLE');
}

export function applyZoneStatus(data: LiveZones, zoneId: string, r: StatusResult): LiveZones {
  if (r.variantId) {
    const entry = { id: r.variantId, status: r.effective, override: r.override };
    return {
      ...data,
      zones: data.zones.map((z) => {
        if (z.id !== zoneId) return z;
        const list = z.variants ?? [];
        return { ...z, variants: list.some((v) => v.id === r.variantId) ? list.map((v) => (v.id === r.variantId ? entry : v)) : [...list, entry] };
      }),
      products: data.products.map((p) =>
        p.id === r.productId ? { ...p, variants: p.variants?.map((v) => (v.id === r.variantId ? { ...v, status: r.status } : v)) } : p,
      ),
    };
  }
  return {
    ...data,
    zones: data.zones.map((z) =>
      z.id !== zoneId ? z : { ...z, products: z.products.map((p) => (p.id === r.productId ? { ...p, status: r.effective, override: r.override } : p)) },
    ),
    products: data.products.map((p) => (p.id === r.productId ? { ...p, status: r.status } : p)),
  };
}

/** A variant as the operator view shows it: effective status in the zone and its global one. */
export interface ZoneVariant {
  id: string;
  name: string;
  status: ProductStatus;
  global: ProductStatus;
}

/** The variants of a product carried in the zone (not "not available"), in their order. */
export function zoneVariants(p: Pick<LiveProduct, 'variants'>, zone: Pick<LiveZone, 'variants'>): ZoneVariant[] {
  const eff = new Map((zone.variants ?? []).map((v) => [v.id, v.status]));
  return (p.variants ?? []).map((v) => ({ id: v.id, name: v.name, global: v.status, status: eff.get(v.id) ?? v.status })).filter((v) => v.status !== 'UNAVAILABLE');
}

export interface ProductSection {
  category: LiveCategory;
  parent?: LiveCategory;
  products: LiveProduct[];
}

/** Category sections in display order (top-level, then its subcategories). */
export function productSections(data: Pick<LiveZones, 'categories' | 'products'>): ProductSection[] {
  const tops = data.categories.filter((c) => !c.parentId);
  const out: ProductSection[] = [];
  for (const top of tops) {
    for (const c of [top, ...data.categories.filter((x) => x.parentId === top.id)]) {
      const products = data.products.filter((p) => p.categoryId === c.id);
      if (products.length > 0) out.push({ category: c, parent: c.parentId ? top : undefined, products });
    }
  }
  return out;
}

export interface ProductFilter {
  type: TypeFilter;
  /** Top-level category id or 'all'. */
  category: string;
  search: string;
}

/**
 * Top-level categories for the category chips: only those that have products of the chosen
 * type, so the two filters never combine into an empty list by accident.
 */
export function categoryChoices(categories: LiveCategory[], sections: ProductSection[], type: TypeFilter): LiveCategory[] {
  const typed = sections.filter((s) => s.products.some((p) => matchesType(p, type)));
  return categories.filter((c) => !c.parentId && typed.some((s) => s.category.id === c.id || s.parent?.id === c.id));
}

/** The sections left after the type filter, the category chip and the search (combined; the
 *  search also finds a product by the name of one of its variants). */
export function filterSections(sections: ProductSection[], f: ProductFilter): ProductSection[] {
  const needle = f.search.trim().toLowerCase();
  const found = (p: LiveProduct): boolean => !needle || p.name.toLowerCase().includes(needle) || (p.variants ?? []).some((v) => v.name.toLowerCase().includes(needle));
  return sections
    .filter((s) => f.category === 'all' || s.category.id === f.category || s.parent?.id === f.category)
    .map((s) => ({ ...s, products: s.products.filter((p) => matchesType(p, f.type) && found(p)) }))
    .filter((s) => s.products.length > 0);
}

const TYPE_ICONS = { drink: Wine, food: UtensilsCrossed, other: Shapes } as const;

// ---------------------------------------------------------------- shell pieces

function useOnline(): boolean {
  const [online, setOnline] = useState(navigator.onLine);
  useEffect(() => {
    const on = (): void => setOnline(navigator.onLine);
    window.addEventListener('online', on);
    window.addEventListener('offline', on);
    return () => {
      window.removeEventListener('online', on);
      window.removeEventListener('offline', on);
    };
  }, []);
  return online;
}

function Center({ children }: { children: ReactNode }) {
  return <div className="ctl-center">{children}</div>;
}

function ControlLogin() {
  const t = useT();
  const { refresh } = useSession();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [updated] = useState(updatedTo);
  const login = useAction(async () => {
    await post('/api/v1/auth/login', { username, password });
    await refresh();
  });
  const submit = (e: FormEvent): void => {
    e.preventDefault();
    if (username && password) void login.run();
  };
  return (
    <Center>
      <form className="ctl-login" onSubmit={submit}>
        <img src={logoUrl} alt="OpenSignage" className="ctl-login-logo" />
        <h1>{t('control.login.title')}</h1>
        {updated && (
          <div className="ctl-notice" role="status">
            {t('common.update.reloaded', { version: updated })}
          </div>
        )}
        <label className="ctl-field">
          <span>{t('control.login.username')}</span>
          <input value={username} autoComplete="username" autoCapitalize="none" autoCorrect="off" spellCheck={false} autoFocus onChange={(e) => setUsername(e.target.value)} />
        </label>
        <label className="ctl-field">
          <span>{t('control.login.password')}</span>
          <input type="password" value={password} autoComplete="current-password" onChange={(e) => setPassword(e.target.value)} />
        </label>
        {login.error != null && (
          <div className="ctl-error" role="alert">
            {errorText(login.error)}
          </div>
        )}
        <button type="submit" className="ctl-btn ctl-btn-primary ctl-btn-block" disabled={!username || !password || login.busy}>
          {login.busy && <Loader2 size={20} className="spin" />}
          {t('control.login.submit')}
        </button>
        <a className="ctl-link" href="/ca.crt">
          {t('control.login.certificate')}
        </a>
      </form>
    </Center>
  );
}

/** Header: logo and zone left, OPEN/CLOSED of the zone in the middle (always in reach),
 * connection and account right. */
function TopBar({ zone, zones, onSwitch, connected, center }: { zone?: LiveZone; zones: LiveZone[]; onSwitch: () => void; connected: boolean; center?: ReactNode }) {
  const t = useT();
  const { user } = useSession();
  return (
    <header className="ctl-top">
      <div className="ctl-top-left">
        <img src={iconUrl} alt="OpenSignage" className="ctl-top-icon" />
        {zone ? (
          <button type="button" className="ctl-zone-switch" onClick={onSwitch} disabled={zones.length < 2} aria-label={t('control.zone.switch')}>
            <span className="ellipsis">{zone.name}</span>
            {zones.length > 1 && <ChevronDown size={20} />}
          </button>
        ) : (
          <span className="ctl-top-title">{t('control.title')}</span>
        )}
      </div>
      {center && <div className="ctl-top-center">{center}</div>}
      <div className="ctl-top-right">
        <span className={`ctl-conn ${connected ? 'ok' : 'bad'}`} title={connected ? t('control.connection.ok') : t('control.connection.lost')}>
          {connected ? <span className="ctl-conn-dot" /> : <WifiOff size={18} />}
          <span className="ctl-hide-tablet">{connected ? t('control.connection.ok') : t('control.connection.lost')}</span>
        </span>
        {roleAtLeast(user?.role, 'operator') && (
          <a className="ctl-icon-btn" href="/" title={t('control.admin')} aria-label={t('control.admin')}>
            <LayoutDashboard size={22} />
          </a>
        )}
        <AccountMenu />
      </div>
    </header>
  );
}

function StaleBanner({ offline, failure, onReload, onDismiss, reloading }: { offline: boolean; failure: string | null; onReload: () => void; onDismiss: () => void; reloading: boolean }) {
  const t = useT();
  if (!offline && !failure) return null;
  return (
    <div className="ctl-banner" role="alert">
      <WifiOff size={20} />
      <div className="ctl-banner-text">
        {offline ? t('control.banner.offline') : t('control.banner.failed', { error: failure ?? '' })}
      </div>
      <button type="button" className="ctl-btn ctl-btn-small" onClick={onReload} disabled={reloading}>
        <RefreshCw size={16} className={reloading ? 'spin' : ''} />
        {t('control.banner.reload')}
      </button>
      {!offline && (
        <button type="button" className="ctl-icon-btn" onClick={onDismiss} aria-label={t('common.action.close')}>
          <X size={18} />
        </button>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- zone picker

function ZonePicker({ zones, current, onPick }: { zones: LiveZone[]; current?: string; onPick: (id: string) => void }) {
  const t = useT();
  return (
    <div className="ctl-picker">
      <h1>{t('control.zone.pick')}</h1>
      {zones.length === 0 ? (
        <p className="ctl-muted">{t('control.zone.none')}</p>
      ) : (
        <div className="ctl-zone-grid">
          {zones.map((z) => (
            <button key={z.id} type="button" className={`ctl-zone-tile ${z.id === current ? 'current' : ''}`} onClick={() => onPick(z.id)}>
              <span className="ctl-zone-name">{z.name}</span>
              <span className={`ctl-pill ${z.state === 'OPEN' ? 'open' : 'closed'}`}>{z.state === 'OPEN' ? t('control.zone.open') : t('control.zone.closed')}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- zone sections

type Report = (e: unknown) => void;

/** OPEN/CLOSED of the zone in the header; the hint what CLOSED does is its tooltip. */
function ZoneStateSwitch({ zone, onDone, report }: { zone: LiveZone; onDone: () => void; report: Report }) {
  const t = useT();
  const confirm = useConfirm();
  const hint = zone.hasClosedPlaylist ? t('control.zone.closedPlaylist') : t('control.zone.closedNoPlaylist');
  const set = useAction(async (state: ZoneState) => {
    try {
      await post(`/api/v1/live/zones/${zone.id}/state`, { state });
      onDone();
    } catch (e) {
      report(e);
    }
  });
  const pending = set.busy;
  // Asked first: a tap by mistake on a tablet must not switch the whole zone.
  const ask = async (state: ZoneState): Promise<void> => {
    const ok = await confirm({
      title: zone.name,
      message: state === 'OPEN' ? t('control.confirm.open', { zone: zone.name }) : `${t('control.confirm.closed', { zone: zone.name })} ${hint}`,
      confirmLabel: state === 'OPEN' ? t('control.zone.open') : t('control.zone.closed'),
    });
    if (ok) await set.run(state);
  };
  return (
    <div className="ctl-seg ctl-seg-2 ctl-zone-state" role="group" aria-label={`${t('control.zone.state')}: ${hint}`} title={hint}>
      {(['OPEN', 'CLOSED'] as ZoneState[]).map((s) => (
        <button
          key={s}
          type="button"
          className={`ctl-seg-btn zone-${s.toLowerCase()} ${zone.state === s ? 'active' : ''}`}
          aria-pressed={zone.state === s}
          disabled={pending}
          onClick={() => zone.state !== s && void ask(s)}
        >
          {pending && zone.state !== s && <Loader2 size={18} className="spin" />}
          {s === 'OPEN' ? t('control.zone.open') : t('control.zone.closed')}
        </button>
      ))}
    </div>
  );
}

function ProductsCard({ data, zone, onStatus, report }: { data: LiveZones; zone: LiveZone; onStatus: (r: StatusResult) => void; report: Report }) {
  const t = useT();
  const lang = getLanguage();
  const sep = decimalSeparator(lang);
  const [filter, setFilter] = useState<string>('all');
  // The type filter is remembered per browser: a tablet at the bar keeps showing drinks.
  const [storedType, setTypeFilter] = useStoredState<string>(TYPE_KEY, 'all');
  const typeFilter: TypeFilter = isTypeFilter(storedType) ? storedType : 'all';
  const [search, setSearch] = useState('');
  const [pending, setPending] = useState<ReadonlySet<string>>(new Set());
  const lock = useRef(new Set<string>());
  const byId = useMemo(() => new Map(zone.products.map((p) => [p.id, p])), [zone.products]);
  const carried = useMemo(() => ({ ...data, products: carriedProducts(data.products, zone.products) }), [data, zone.products]);
  const sections = useMemo(() => productSections(carried), [carried]);
  const types = typeChips(carried.products, typeFilter);
  const tops = categoryChoices(data.categories, sections, typeFilter);
  // A chosen category without products of the chosen type falls back to "all" (and comes
  // back when the type filter allows it again).
  const category = tops.length > 1 && tops.some((c) => c.id === filter) ? filter : 'all';

  const confirm = useConfirm();
  // A variant switches between available and sold out with one tap (asked first). Going back
  // to its global status clears the zone override instead of setting one.
  const askVariant = async (p: LiveProduct, v: ZoneVariant): Promise<void> => {
    if (lock.current.has(v.id)) return;
    const next: ProductStatus = v.status === 'SOLD_OUT' ? 'AVAILABLE' : 'SOLD_OUT';
    const name = `${p.name} – ${v.name}`;
    const ok = await confirm({
      title: name,
      message: next === 'SOLD_OUT' ? t('control.confirm.soldOut', { name }) : t('control.confirm.available', { name }),
      confirmLabel: t(`control.status.${next}`),
      danger: next === 'SOLD_OUT',
    });
    if (ok) await setStatus(v.id, next === v.global ? null : next, 'variants');
  };
  // Asked first: a tap by mistake on a tablet must not take a product off the screens.
  const askStatus = async (p: LiveProduct, status: ProductStatus | null): Promise<void> => {
    if (lock.current.has(p.id)) return;
    const message =
      status === null
        ? t('control.confirm.reset', { name: p.name, status: t(`control.status.${p.status}`) })
        : status === 'SOLD_OUT'
          ? t('control.confirm.soldOut', { name: p.name })
          : t('control.confirm.available', { name: p.name });
    const ok = await confirm({
      title: p.name,
      message,
      confirmLabel: status === null ? t('control.products.resetShort') : t(`control.status.${status}`),
      danger: status === 'SOLD_OUT',
    });
    if (ok) await setStatus(p.id, status);
  };

  const setStatus = async (id: string, status: ProductStatus | null, kind: 'products' | 'variants' = 'products'): Promise<void> => {
    if (lock.current.has(id)) return;
    lock.current.add(id);
    setPending(new Set(lock.current));
    try {
      const r = await post<StatusResult>(`/api/v1/live/${kind}/${encodeURIComponent(id)}/status`, { status, zoneId: zone.id });
      onStatus(r);
    } catch (e) {
      report(e);
    } finally {
      lock.current.delete(id);
      setPending(new Set(lock.current));
    }
  };

  const visible = filterSections(sections, { type: typeFilter, category, search });

  return (
    <section className="ctl-card ctl-products">
      <div className="ctl-products-head">
        <h2>{t('control.products.title')}</h2>
        <label className="ctl-search">
          <Search size={18} />
          <input value={search} onChange={(e) => setSearch(e.target.value)} placeholder={t('control.products.search')} aria-label={t('control.products.search')} />
          {search && (
            <button type="button" onClick={() => setSearch('')} aria-label={t('common.action.close')}>
              <X size={16} />
            </button>
          )}
        </label>
      </div>
      {types.length > 0 && (
        <div className="ctl-chips ctl-type-chips" role="group" aria-label={t('control.products.types')}>
          {types.map((c) => {
            const Icon = c.value === 'all' ? null : TYPE_ICONS[c.value];
            return (
              <button key={c.value} type="button" className={typeFilter === c.value ? 'active' : ''} aria-pressed={typeFilter === c.value} onClick={() => setTypeFilter(c.value)}>
                {Icon && <Icon size={18} />}
                {t(TYPE_FILTER_LABELS[c.value])}
                <span className="ctl-chip-count">{c.count}</span>
              </button>
            );
          })}
        </div>
      )}
      {tops.length > 1 && (
        <div className="ctl-chips" role="group" aria-label={t('control.products.categories')}>
          <button type="button" className={category === 'all' ? 'active' : ''} aria-pressed={category === 'all'} onClick={() => setFilter('all')}>
            {t('control.products.all')}
          </button>
          {tops.map((c) => (
            <button key={c.id} type="button" className={category === c.id ? 'active' : ''} aria-pressed={category === c.id} onClick={() => setFilter(c.id)}>
              {c.name}
            </button>
          ))}
        </div>
      )}
      {sections.length === 0 ? (
        <p className="ctl-muted">{t('control.products.none')}</p>
      ) : visible.length === 0 ? (
        <p className="ctl-muted">{t('control.products.noMatch')}</p>
      ) : (
        visible.map((s) => (
          <div key={s.category.id} className="ctl-cat">
            <h3>{s.parent ? `${s.parent.name} › ${s.category.name}` : s.category.name}</h3>
            <ul className="ctl-product-list">
              {s.products.map((p) => {
                const st = byId.get(p.id);
                const effective = st?.status ?? p.status;
                const override = st?.override ?? null;
                const busy = pending.has(p.id);
                const qty = p.quantity ? `${formatQuantityValue(p.quantity.value, sep)} ${t(`admin.products.unit.${p.quantity.unit}`)}` : '';
                const variants = zoneVariants(p, zone);
                // Every variant sold out: the screens show the whole product as sold out.
                const allOut = effective === 'AVAILABLE' && variants.length > 0 && variants.every((v) => v.status === 'SOLD_OUT');
                return (
                  <li key={p.id} className={`ctl-product st-${(allOut ? 'SOLD_OUT' : effective).toLowerCase()} ${variants.length ? 'has-variants' : ''}`}>
                    {/* One line like the product list: name, then quantity and price in fixed columns
                        so they line up in every row. A zone override shows as the reset button. */}
                    <div className="ctl-product-info">
                      <span className="ctl-product-name">{p.name}</span>
                      <span className="ctl-qty">{qty}</span>
                      <span className="ctl-price">{formatMoney(p.price.amount, p.price.currency, lang)}</span>
                    </div>
                    <div className="ctl-status" role="group" aria-label={t('control.products.statusOf', { name: p.name })}>
                      {/* The reset slot always takes its place (empty when there is nothing to reset), so
                          price, reset, "available" and "sold out" stay in the same place in every row. */}
                      {!(override && p.status !== 'UNAVAILABLE') && <span className="ctl-st ctl-st-reset ctl-st-slot" aria-hidden="true" />}
                      {override && p.status !== 'UNAVAILABLE' && (
                        <button
                          type="button"
                          className="ctl-st ctl-st-reset"
                          disabled={busy}
                          title={t('control.products.reset', { status: t(`control.status.${p.status}`) })}
                          aria-label={t('control.products.reset', { status: t(`control.status.${p.status}`) })}
                          onClick={() => void askStatus(p, null)}
                        >
                          <RotateCcw size={20} />
                          <span className="ctl-st-label">{t('control.products.resetShort')}</span>
                        </button>
                      )}
                      {STATUSES.map((s2) => (
                        <button
                          key={s2}
                          type="button"
                          className={`ctl-st ctl-st-${s2.toLowerCase()} ${effective === s2 ? 'active' : ''}`}
                          aria-pressed={effective === s2}
                          aria-label={t(`control.status.${s2}`)}
                          title={t(`control.status.${s2}`)}
                          disabled={busy}
                          onClick={() => effective !== s2 && void askStatus(p, s2)}
                        >
                          {busy && effective === s2 ? (
                            <Loader2 size={20} className="spin" />
                          ) : s2 === 'AVAILABLE' ? (
                            <Check size={22} strokeWidth={2.5} />
                          ) : (
                            <Ban size={20} strokeWidth={2.5} />
                          )}
                          <span className="ctl-st-label">{t(`control.status.${s2}`)}</span>
                        </button>
                      ))}
                    </div>
                    {variants.length > 0 && (
                      <div className="ctl-variants" role="group" aria-label={t('control.products.variantsOf', { name: p.name })}>
                        {variants.map((v) => (
                          <button
                            key={v.id}
                            type="button"
                            className={`ctl-var st-${v.status.toLowerCase()}`}
                            aria-pressed={v.status === 'SOLD_OUT'}
                            title={t(`control.status.${v.status}`)}
                            disabled={pending.has(v.id) || effective !== 'AVAILABLE'}
                            onClick={() => void askVariant(p, v)}
                          >
                            {pending.has(v.id) ? <Loader2 size={16} className="spin" /> : v.status === 'SOLD_OUT' ? <Ban size={16} /> : <Check size={16} />}
                            <span className="ctl-var-name">{v.name}</span>
                          </button>
                        ))}
                        {allOut && <span className="ctl-var-note">{t('control.products.allVariantsSoldOut')}</span>}
                      </div>
                    )}
                  </li>
                );
              })}
            </ul>
          </div>
        ))
      )}
    </section>
  );
}

// ---------------------------------------------------------------- operator app

function Operator() {
  const t = useT();
  const connected = useConnected();
  const online = useOnline();
  const live = useQuery<LiveZones>('/api/v1/live/zones');
  const [failure, setFailure] = useState<string | null>(null);
  const [stored, setStored] = useStoredState<string | null>(ZONE_KEY, null);
  const [picking, setPicking] = useState(false);

  useEvents(['live', 'zones', 'products'], () => live.reload());
  // Without the event socket nothing tells us about changes: poll until it is back.
  useEffect(() => {
    if (connected) return;
    const id = window.setInterval(live.reload, 15000);
    return () => clearInterval(id);
  }, [connected, live.reload]);

  const report = useCallback((e: unknown) => setFailure(errorText(e)), []);
  const reload = useCallback(() => {
    setFailure(null);
    live.reload();
  }, [live]);
  // A failed background reload also means the view may be stale.
  const loadFailed = live.error && live.data ? errorText(live.error) : null;

  const zones = live.data?.zones ?? [];
  const zone = zones.find((z) => z.id === stored) ?? (zones.length === 1 ? zones[0] : undefined);
  const pick = (id: string): void => {
    setStored(id);
    setPicking(false);
  };
  const onStatus = useCallback(
    (r: StatusResult) => {
      if (live.data && zone) live.setData(applyZoneStatus(live.data, zone.id, r));
    },
    [live, zone],
  );

  if (!live.data) {
    return (
      <div className="ctl-app">
        <TopBar zones={[]} onSwitch={() => {}} connected={connected && online} />
        <Center>
          {live.error ? (
            <div className="ctl-error-state" role="alert">
              <p>{errorText(live.error)}</p>
              <button type="button" className="ctl-btn ctl-btn-primary" onClick={live.reload}>
                {t('common.action.retry')}
              </button>
            </div>
          ) : (
            <Spinner label={t('common.state.loading')} />
          )}
        </Center>
      </div>
    );
  }

  return (
    <div className="ctl-app">
      <TopBar
        zone={picking ? undefined : zone}
        zones={zones}
        onSwitch={() => setPicking(true)}
        connected={connected && online}
        center={zone && !picking ? <ZoneStateSwitch zone={zone} onDone={live.reload} report={report} /> : undefined}
      />
      <StaleBanner offline={!connected || !online} failure={failure ?? loadFailed} onReload={reload} onDismiss={() => setFailure(null)} reloading={live.loading} />
      {!zone || picking ? (
        <ZonePicker zones={zones} current={zone?.id} onPick={pick} />
      ) : (
        <main className="ctl-main" key={zone.id}>
          <ProductsCard data={live.data} zone={zone} onStatus={onStatus} report={report} />
        </main>
      )}
    </div>
  );
}

function ControlRoot() {
  const t = useT();
  const { ready, needsSetup, user } = useSession();
  useEffect(() => {
    if (user) startEvents();
    else stopEvents();
  }, [user]);
  if (!ready) {
    return (
      <Center>
        <Spinner label={t('common.state.loading')} />
      </Center>
    );
  }
  if (needsSetup) {
    return (
      <Center>
        <div className="ctl-login">
          <img src={logoUrl} alt="OpenSignage" className="ctl-login-logo" />
          <p>{t('control.setupFirst')}</p>
          <a className="ctl-btn ctl-btn-primary ctl-btn-block" href="/">
            {t('control.admin')}
          </a>
        </div>
      </Center>
    );
  }
  if (!user) return <ControlLogin />;
  return <Operator />;
}

export function ControlApp() {
  const t = useT();
  return (
    <ErrorBoundary label={t('common.state.error')} retryLabel={t('common.action.retry')}>
      <SessionProvider>
        <FeedbackProvider>
          <ControlRoot />
        </FeedbackProvider>
      </SessionProvider>
    </ErrorBoundary>
  );
}

