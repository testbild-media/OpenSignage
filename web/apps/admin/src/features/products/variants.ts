// Variants of a product ("Orange, Apple, Passion fruit"): pure helpers of the variant editor.

/** A row of the variant editor; id is set for variants that are already stored. */
export interface VariantRow {
  key: number;
  id?: string;
  name: string;
}

/** Splits a typed or pasted list ("Orange, Apple; Guava" or one per line) into names. */
export function splitVariantNames(text: string): string[] {
  return text
    .split(/[,;\n]/)
    .map((s) => s.trim())
    .filter((s) => s !== '');
}

/** Names that occur more than once (ignoring case and surrounding spaces), lower-cased. */
export function duplicateNames(rows: Pick<VariantRow, 'name'>[]): Set<string> {
  const seen = new Set<string>();
  const dup = new Set<string>();
  for (const r of rows) {
    const n = r.name.trim().toLowerCase();
    if (!n) continue;
    if (seen.has(n)) dup.add(n);
    seen.add(n);
  }
  return dup;
}

/** The variants to save: empty rows are dropped, stored variants keep their id. */
export function variantsBody(rows: VariantRow[]): { id?: string; name: string }[] {
  return rows.filter((r) => r.name.trim() !== '').map((r) => (r.id ? { id: r.id, name: r.name.trim() } : { name: r.name.trim() }));
}
