import { describe, expect, it } from 'vitest';
import { currencyDigits, formatMoneyInput, formatQuantityValue, parseMoney, parseQuantity } from './money';

describe('parseMoney', () => {
  it('parses decimal comma and point without float errors', () => {
    expect(parseMoney('4,50')).toBe(450);
    expect(parseMoney('4.50')).toBe(450);
    expect(parseMoney('4,5')).toBe(450);
    expect(parseMoney('0,10')).toBe(10);
    expect(parseMoney('0.29')).toBe(29); // 0.29 * 100 = 28.999999999999996 in floating point
    expect(parseMoney('1.15')).toBe(115);
    expect(parseMoney('19,99')).toBe(1999);
    expect(parseMoney('12')).toBe(1200);
    expect(parseMoney(',5')).toBe(50);
    expect(parseMoney(' 7,00 ')).toBe(700);
  });

  it('accepts thousands separators', () => {
    expect(parseMoney('1.234,50')).toBe(123450);
    expect(parseMoney('1,234.50')).toBe(123450);
    expect(parseMoney('1 234,50')).toBe(123450);
    expect(parseMoney('1.234.567')).toBe(123456700);
  });

  it('rejects invalid input', () => {
    expect(parseMoney('')).toBeNull();
    expect(parseMoney('abc')).toBeNull();
    expect(parseMoney('-1')).toBeNull();
    expect(parseMoney('4,567')).toBeNull(); // three fraction digits for EUR
    expect(parseMoney('1,2,3')).toBeNull();
    expect(parseMoney('1.23.4')).toBeNull();
    expect(parseMoney('1.234,5,6')).toBeNull();
    expect(parseMoney('.')).toBeNull();
    expect(parseMoney('9999999999999999')).toBeNull();
  });

  it('ignores trailing zeros beyond the precision', () => {
    expect(parseMoney('4,500')).toBe(450);
  });

  it('respects the currency precision', () => {
    expect(parseMoney('500', 0)).toBe(500);
    expect(parseMoney('5,5', 0)).toBeNull();
    expect(parseMoney('1,234', 3)).toBe(1234);
  });
});

describe('formatMoneyInput', () => {
  it('formats minor units', () => {
    expect(formatMoneyInput(450, 2, ',')).toBe('4,50');
    expect(formatMoneyInput(5, 2, '.')).toBe('0.05');
    expect(formatMoneyInput(123450, 2, ',')).toBe('1234,50');
    expect(formatMoneyInput(500, 0)).toBe('500');
  });

  it('round-trips through parseMoney', () => {
    for (const n of [0, 1, 10, 99, 450, 1999, 123456]) {
      expect(parseMoney(formatMoneyInput(n, 2, ','))).toBe(n);
    }
  });
});

describe('currencyDigits', () => {
  it('knows common currencies', () => {
    expect(currencyDigits('EUR')).toBe(2);
    expect(currencyDigits('JPY')).toBe(0);
    expect(currencyDigits('???')).toBe(2);
  });
});

describe('parseQuantity', () => {
  it('normalizes values', () => {
    expect(parseQuantity('0,33')).toBe('0.33');
    expect(parseQuantity('500')).toBe('500');
    expect(parseQuantity('0.330')).toBe('0.33');
    expect(parseQuantity('2,0')).toBe('2');
    expect(parseQuantity('007')).toBe('7');
  });

  it('rejects invalid values', () => {
    expect(parseQuantity('')).toBeNull();
    expect(parseQuantity('0')).toBeNull();
    expect(parseQuantity('0,0')).toBeNull();
    expect(parseQuantity('1,2345')).toBeNull();
    expect(parseQuantity('1234567')).toBeNull();
    expect(parseQuantity('-1')).toBeNull();
    expect(parseQuantity('1e3')).toBeNull();
  });

  it('formats with the decimal mark', () => {
    expect(formatQuantityValue('0.33', ',')).toBe('0,33');
  });
});
