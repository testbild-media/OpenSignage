-- Several screens in one zone: display groups inside a zone, the position of a screen in
-- its group (or among the screens without group), playlists of single groups in schedule
-- rules, rules that hide the ticker, and the moment a zone was closed (anchor of the
-- closed content for synchronous playback).
CREATE TABLE display_groups (
  id                 TEXT PRIMARY KEY,
  zone_id            TEXT NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
  name               TEXT NOT NULL,
  closed_playlist_id TEXT REFERENCES playlists(id),
  sort               INTEGER NOT NULL DEFAULT 0,
  revision           INTEGER NOT NULL DEFAULT 1,
  created_at         TEXT NOT NULL,
  updated_at         TEXT NOT NULL
);
CREATE INDEX display_groups_zone ON display_groups(zone_id);

ALTER TABLE devices ADD COLUMN group_id TEXT REFERENCES display_groups(id) ON DELETE SET NULL;
ALTER TABLE devices ADD COLUMN position INTEGER;
-- Existing screens get positions 1..n in their zone (by name), so nothing changes for them.
UPDATE devices SET position = (
  SELECT COUNT(*) FROM devices d2
  WHERE d2.zone_id = devices.zone_id AND (d2.name < devices.name OR (d2.name = devices.name AND d2.id <= devices.id))
) WHERE zone_id IS NOT NULL;

-- group_id: a display group of the rule's zone, or 'ungrouped' (screens without group).
CREATE TABLE schedule_group_playlists (
  rule_id     TEXT NOT NULL REFERENCES schedule_rules(id) ON DELETE CASCADE,
  group_id    TEXT NOT NULL,
  playlist_id TEXT NOT NULL REFERENCES playlists(id),
  PRIMARY KEY (rule_id, group_id)
);

ALTER TABLE schedule_rules ADD COLUMN ticker_hide INTEGER NOT NULL DEFAULT 0;
ALTER TABLE zones ADD COLUMN state_changed_at TEXT;
-- clock_key authenticates the LAN time exchange of the zone's screens (hex, 32 bytes).
ALTER TABLE zones ADD COLUMN clock_key TEXT;
UPDATE zones SET clock_key = lower(hex(randomblob(32)));
