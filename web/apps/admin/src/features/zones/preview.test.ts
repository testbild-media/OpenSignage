import { describe, expect, it } from 'vitest';
import type { LiveProduct, Manifest } from '@opensignage/contracts';
import { inputValue, manifestSignature, previewQuery, priceChanges, soldOutNames, untilNextMinute } from './preview';

const product = (id: string, name: string, amount: number, patch: Partial<LiveProduct> = {}): LiveProduct => ({
  id,
  name,
  description: '',
  categoryId: 'c',
  type: 'drink',
  price: { amount, currency: 'EUR' },
  status: 'AVAILABLE',
  showImageInsteadOfName: false,
  sort: 0,
  ...patch,
});

describe('zone preview', () => {
  it('asks for a moment and a chosen state only when given', () => {
    expect(previewQuery(null, '')).toBe('');
    expect(previewQuery('2026-10-12T18:00', 'CLOSED')).toBe('?at=2026-10-12T18%3A00&state=CLOSED');
  });

  it('shows an instant as the date and time of the server time zone', () => {
    expect(inputValue(new Date('2026-10-12T16:05:30Z'), 'Europe/Berlin')).toBe('2026-10-12T18:05');
    expect(inputValue(new Date('2026-12-31T23:30:00Z'), 'Europe/Berlin')).toBe('2027-01-01T00:30');
  });

  it('evaluates again shortly after the next full minute', () => {
    expect(untilNextMinute(Date.parse('2026-10-12T16:05:30Z'))).toBe(30_500);
    expect(untilNextMinute(Date.parse('2026-10-12T16:05:00Z'))).toBe(60_500);
  });

  it('keeps the content signature when only the build changes', () => {
    const m = { id: 'a', hash: 'h1', builtAt: '1', zoneId: 'z', rules: [], playlists: [] } as unknown as Manifest;
    expect(manifestSignature({ ...m, id: 'b', hash: 'h2', builtAt: '2' })).toBe(manifestSignature(m));
    expect(manifestSignature({ ...m, zoneId: 'y' })).not.toBe(manifestSignature(m));
  });

  it('lists the prices a time schedule changes and what is sold out in the zone', () => {
    const products = [
      product('m', 'Mojito', 950),
      product('c', 'Cola', 300),
      product('g', 'Gone', 500, { status: 'UNAVAILABLE' }),
      product('w', 'Wheat beer', 490, { status: 'SOLD_OUT' }),
      product('j', 'Juices', 400, { variants: [{ id: 'o', name: 'Orange', status: 'SOLD_OUT' }, { id: 'a', name: 'Apple', status: 'AVAILABLE' }] }),
    ];
    const prices = { m: { amount: 650, currency: 'EUR' }, c: { amount: 300, currency: 'EUR' }, g: { amount: 100, currency: 'EUR' } };
    expect(priceChanges(prices, products).map((p) => `${p.name} ${p.base.amount}→${p.price.amount}`)).toEqual(['Mojito 950→650']);
    expect(soldOutNames(products)).toEqual(['Juices – Orange', 'Wheat beer']);
  });
});
