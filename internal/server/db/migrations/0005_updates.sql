-- Updates module (owner: internal/server/updates).

-- Platform of each paired device, needed to offer the right release package. The agent
-- reports os/arch when it requests pairing; pairing requests are deleted later, so the
-- updates module keeps a copy. align_on_connect marks devices that were just claimed:
-- claiming is the consent to bring the device to the server version (decision A.5).
CREATE TABLE device_platforms (
  device_id        TEXT PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
  os               TEXT NOT NULL DEFAULT '',
  arch             TEXT NOT NULL DEFAULT '',
  align_on_connect INTEGER NOT NULL DEFAULT 0,
  updated_at       TEXT NOT NULL
);

-- When the offer was sent (rollout timeout), and the device's version before the update.
ALTER TABLE device_updates ADD COLUMN sent_at TEXT;
ALTER TABLE device_updates ADD COLUMN from_version TEXT NOT NULL DEFAULT '';
CREATE INDEX device_updates_device ON device_updates(device_id, updated_at);
