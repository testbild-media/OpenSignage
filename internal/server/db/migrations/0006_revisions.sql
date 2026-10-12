-- Optimistic concurrency (ARCHITECTURE §7) for zones, categories and ticker templates: every
-- PATCH must carry the revision it was based on; a stale one is answered with 409 `conflict`.
ALTER TABLE zones ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE categories ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE ticker_templates ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;

-- Pairing requests remember the address they came from, so open requests can be capped per
-- source (a single flooding host cannot crowd out the real players).
ALTER TABLE pairing_requests ADD COLUMN source_ip TEXT NOT NULL DEFAULT '';
CREATE INDEX pairing_source ON pairing_requests(source_ip, device_id);
