// Money and quantity input helpers. Prices are typed as decimals ("4,50") and sent as
// integer minor units (450): parsing works on the digit strings, never with floating point
// arithmetic, so no rounding error can creep in.

/** Upper bound of a single price in minor units (mirrors contracts.MaxMoneyAmount). */
export const MAX_MONEY_AMOUNT = 100_000_000_000;

/** Number of minor-unit digits of an ISO 4217 currency (EUR 2, JPY 0, BHD 3). */
export function currencyDigits(currency: string): number {
  try {
    const d = new Intl.NumberFormat('en', { style: 'currency', currency }).resolvedOptions().maximumFractionDigits;
    return typeof d === 'number' ? d : 2;
  } catch {
    return 2;
  }
}

export function validCurrency(c: string): boolean {
  return /^[A-Z]{3}$/.test(c);
}

/** Decimal separator of a language ("," for de, "." for en). */
export function decimalSeparator(lang: string): string {
  try {
    return new Intl.NumberFormat(lang).formatToParts(1.5).find((p) => p.type === 'decimal')?.value ?? '.';
  } catch {
    return '.';
  }
}

/**
 * Parses a price typed by a person into minor units. Accepts "4,50", "4.5", "12",
 * "1.234,50", "1,234.50" and "1 234,50". With a single separator kind occurring once, that
 * separator is the decimal mark. Returns null for anything else (negative, too many
 * fraction digits, letters, out of range).
 */
export function parseMoney(input: string, digits = 2): number | null {
  const s = input.replace(/[\s\u00a0\u202f']/g, '');
  if (!/^[0-9.,]+$/.test(s) || !/[0-9]/.test(s)) return null;
  const lastComma = s.lastIndexOf(',');
  const lastDot = s.lastIndexOf('.');
  let intPart: string;
  let frac = '';
  if (lastComma >= 0 && lastDot >= 0) {
    const dec = lastComma > lastDot ? ',' : '.';
    const thousands = dec === ',' ? '.' : ',';
    const at = s.lastIndexOf(dec);
    if (s.indexOf(dec) !== at) return null;
    intPart = s.slice(0, at);
    frac = s.slice(at + 1);
    if (!validGroups(intPart, thousands)) return null;
    intPart = intPart.split(thousands).join('');
  } else if (lastComma >= 0 || lastDot >= 0) {
    const sep = lastComma >= 0 ? ',' : '.';
    const count = s.split(sep).length - 1;
    if (count === 1) {
      const at = s.indexOf(sep);
      intPart = s.slice(0, at);
      frac = s.slice(at + 1);
    } else {
      // Repeated separator: thousands grouping only ("1.234.567").
      if (!validGroups(s, sep)) return null;
      intPart = s.split(sep).join('');
    }
  } else {
    intPart = s;
  }
  if (intPart === '') intPart = '0';
  if (!/^[0-9]+$/.test(intPart) || !/^[0-9]*$/.test(frac)) return null;
  if (frac.length > digits) {
    // Trailing zeros beyond the currency precision are harmless ("4,500" → 4.50 €).
    if (/[^0]/.test(frac.slice(digits))) return null;
    frac = frac.slice(0, digits);
  }
  if (intPart.replace(/^0+/, '').length > 15) return null;
  const amount = Number(intPart) * 10 ** digits + Number(frac.padEnd(digits, '0') || '0');
  if (!Number.isSafeInteger(amount) || amount > MAX_MONEY_AMOUNT) return null;
  return amount;
}

function validGroups(s: string, sep: string): boolean {
  const parts = s.split(sep);
  if (parts.length === 1) return true;
  const [head, ...rest] = parts;
  return !!head && /^[0-9]{1,3}$/.test(head) && rest.every((p) => /^[0-9]{3}$/.test(p));
}

/** Formats minor units for an input field ("4,50" in German, "4.50" in English). */
export function formatMoneyInput(amount: number, digits = 2, sep = '.'): string {
  const neg = amount < 0;
  const abs = Math.abs(Math.trunc(amount));
  const base = 10 ** digits;
  const int = Math.floor(abs / base);
  const frac = String(abs % base).padStart(digits, '0');
  return `${neg ? '-' : ''}${int}${digits > 0 ? sep + frac : ''}`;
}

/**
 * Normalizes a quantity value ("0,33" → "0.33") to the server format: a positive decimal
 * with at most six integer and three fraction digits. Returns null when invalid.
 */
export function parseQuantity(input: string): string | null {
  let s = input.trim().replace(',', '.');
  if (!/^[0-9]+(\.[0-9]+)?$/.test(s)) return null;
  s = s.replace(/^0+(?=[0-9])/, '');
  if (s.includes('.')) s = s.replace(/0+$/, '').replace(/\.$/, '');
  if (!/^(0|[1-9][0-9]{0,5})(\.[0-9]{1,3})?$/.test(s)) return null;
  if (/^[0.]+$/.test(s)) return null;
  return s;
}

/** Shows a stored quantity value with the language's decimal mark. */
export function formatQuantityValue(value: string, sep = '.'): string {
  return value.replace('.', sep);
}
