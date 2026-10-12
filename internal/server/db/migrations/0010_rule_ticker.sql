-- Tickers come from time schedule rules now: a rule may show a ticker on every screen of
-- its zone while it is active. The live zone ticker of the operator view
-- (zones.ticker_text / ticker_visible) is gone; the columns stay unused (SQLite cannot drop
-- them cheaply), zones.ticker_template_id remains the zone's default ticker template.
ALTER TABLE schedule_rules ADD COLUMN ticker_text TEXT;
ALTER TABLE schedule_rules ADD COLUMN ticker_template_id TEXT REFERENCES ticker_templates(id);
UPDATE zones SET ticker_text = '', ticker_visible = 0;
