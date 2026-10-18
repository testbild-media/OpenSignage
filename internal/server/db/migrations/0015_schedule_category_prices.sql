-- Schedule prices for whole categories ("all long drinks 5 €"): while the rule is active,
-- every product of the category (and of its subcategories) gets the price, unless the rule
-- sets a price for the product itself or for its subcategory. The server turns them into
-- product prices for the screens (schedule.ExpandPrices).
CREATE TABLE schedule_category_prices (
  rule_id     TEXT NOT NULL REFERENCES schedule_rules(id) ON DELETE CASCADE,
  category_id TEXT NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
  amount      INTEGER NOT NULL,
  currency    TEXT NOT NULL,
  PRIMARY KEY (rule_id, category_id)
);
CREATE INDEX schedule_category_prices_category ON schedule_category_prices(category_id);
