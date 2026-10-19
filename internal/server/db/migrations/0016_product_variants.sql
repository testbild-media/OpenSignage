-- Variants of a product ("Orange, Apple, Passion fruit"): they share the product's price and
-- are shown like its description, separated by commas. Each variant has its own status
-- (global and per zone, like products), so a single sold-out variant is struck through on
-- the screens.
CREATE TABLE product_variants (
  id         TEXT PRIMARY KEY,
  product_id TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  sort       INTEGER NOT NULL DEFAULT 0,
  status     TEXT NOT NULL DEFAULT 'AVAILABLE'
               CHECK (status IN ('AVAILABLE', 'UNAVAILABLE', 'SOLD_OUT'))
);
CREATE INDEX product_variants_product ON product_variants(product_id, sort);

CREATE TABLE zone_variant_status (
  zone_id    TEXT NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
  variant_id TEXT NOT NULL REFERENCES product_variants(id) ON DELETE CASCADE,
  status     TEXT NOT NULL CHECK (status IN ('AVAILABLE', 'UNAVAILABLE', 'SOLD_OUT')),
  PRIMARY KEY (zone_id, variant_id)
);
