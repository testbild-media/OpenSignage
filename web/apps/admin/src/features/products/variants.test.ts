import { describe, expect, it } from 'vitest';
import { duplicateNames, splitVariantNames, variantsBody } from './variants';

describe('variants', () => {
  it('splits a typed or pasted list', () => {
    expect(splitVariantNames('Orange, Apfel, Maracuja')).toEqual(['Orange', 'Apfel', 'Maracuja']);
    expect(splitVariantNames('Bitter Lemon;Tonic\n Ginger Ale ,, ')).toEqual(['Bitter Lemon', 'Tonic', 'Ginger Ale']);
    expect(splitVariantNames('  ')).toEqual([]);
  });

  it('finds names given twice, ignoring case and spaces', () => {
    expect([...duplicateNames([{ name: 'Orange' }, { name: ' orange ' }, { name: 'Apple' }, { name: '' }, { name: '' }])]).toEqual(['orange']);
    expect(duplicateNames([{ name: 'Orange' }, { name: 'Apple' }]).size).toBe(0);
  });

  it('saves stored variants with their id and leaves out empty rows', () => {
    expect(
      variantsBody([
        { key: 1, id: 'v1', name: ' Orange ' },
        { key: 2, name: '' },
        { key: 3, name: 'Guava' },
      ]),
    ).toEqual([{ id: 'v1', name: 'Orange' }, { name: 'Guava' }]);
  });
});
