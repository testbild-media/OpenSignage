-- OpenSignage initial schema. Forward-only migrations; never edit an applied file.
-- Conventions: ids are UUIDv7 text, timestamps RFC 3339 UTC text, JSON stored as text,
-- money as integer minor units + currency, booleans as 0/1.

CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,              -- JSON
  updated_at TEXT NOT NULL
);

CREATE TABLE users (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
  display_name  TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  role          TEXT NOT NULL CHECK (role IN ('admin', 'operator', 'user')),
  active        INTEGER NOT NULL DEFAULT 1,
  language      TEXT,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  revision      INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE sessions (
  id_hash      TEXT PRIMARY KEY,           -- sha256 of the cookie value
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf         TEXT NOT NULL,
  created_at   TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  expires_at   TEXT NOT NULL,
  ip           TEXT NOT NULL DEFAULT '',
  user_agent   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE audit_log (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  at          TEXT NOT NULL,
  user_id     TEXT,                        -- no FK: entries survive user deletion
  username    TEXT NOT NULL DEFAULT '',    -- name at the time of the action
  action      TEXT NOT NULL,
  object_type TEXT NOT NULL DEFAULT '',
  object_id   TEXT NOT NULL DEFAULT '',
  object_name TEXT NOT NULL DEFAULT '',
  zone_id     TEXT NOT NULL DEFAULT '',
  old_value   TEXT,
  new_value   TEXT,
  ip          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_at ON audit_log(at);
CREATE INDEX audit_object ON audit_log(object_type, object_id);

-- Media -----------------------------------------------------------------------

CREATE TABLE blobs (
  sha256     TEXT PRIMARY KEY,
  size       INTEGER NOT NULL,
  mime       TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE media_folders (
  id                 TEXT PRIMARY KEY,
  parent_id          TEXT REFERENCES media_folders(id),
  name               TEXT NOT NULL,
  created_at         TEXT NOT NULL,
  updated_at         TEXT NOT NULL,
  trashed_at         TEXT,
  trash_batch        TEXT
);
CREATE INDEX media_folders_parent ON media_folders(parent_id);

CREATE TABLE media_assets (
  id                 TEXT PRIMARY KEY,
  folder_id          TEXT REFERENCES media_folders(id),   -- NULL = root
  name               TEXT NOT NULL,
  kind               TEXT NOT NULL CHECK (kind IN ('image', 'video', 'audio')),
  revision           INTEGER NOT NULL DEFAULT 1,
  blob_sha256        TEXT REFERENCES blobs(sha256),        -- current original
  status             TEXT NOT NULL CHECK (status IN ('processing', 'ready', 'failed')),
  error              TEXT NOT NULL DEFAULT '',
  meta               TEXT NOT NULL DEFAULT '{}',           -- width, height, durationMs, codecs …
  created_at         TEXT NOT NULL,
  updated_at         TEXT NOT NULL,
  trashed_at         TEXT,
  trash_batch        TEXT,
  original_folder_id TEXT
);
CREATE INDEX media_assets_folder ON media_assets(folder_id);

CREATE TABLE media_revisions (
  asset_id    TEXT NOT NULL REFERENCES media_assets(id) ON DELETE CASCADE,
  revision    INTEGER NOT NULL,
  blob_sha256 TEXT NOT NULL REFERENCES blobs(sha256),
  meta        TEXT NOT NULL DEFAULT '{}',
  created_at  TEXT NOT NULL,
  PRIMARY KEY (asset_id, revision)
);

CREATE TABLE media_variants (
  asset_id    TEXT NOT NULL REFERENCES media_assets(id) ON DELETE CASCADE,
  revision    INTEGER NOT NULL,
  variant     TEXT NOT NULL,              -- thumb | v1080 | v720 | i1920 | i512
  blob_sha256 TEXT NOT NULL REFERENCES blobs(sha256),
  width       INTEGER NOT NULL DEFAULT 0,
  height      INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL,
  PRIMARY KEY (asset_id, revision, variant)
);

CREATE TABLE media_jobs (
  id             TEXT PRIMARY KEY,
  asset_id       TEXT NOT NULL REFERENCES media_assets(id) ON DELETE CASCADE,
  revision       INTEGER NOT NULL,
  state          TEXT NOT NULL CHECK (state IN ('queued', 'running', 'done', 'failed')),
  attempts       INTEGER NOT NULL DEFAULT 0,
  error          TEXT NOT NULL DEFAULT '',
  reserved_bytes INTEGER NOT NULL DEFAULT 0,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
CREATE INDEX media_jobs_state ON media_jobs(state);

CREATE TABLE uploads (
  id               TEXT PRIMARY KEY,
  user_id          TEXT NOT NULL,
  filename         TEXT NOT NULL,
  size             INTEGER NOT NULL,
  received         INTEGER NOT NULL DEFAULT 0,
  folder_id        TEXT,
  replace_asset_id TEXT,
  created_at       TEXT NOT NULL,
  expires_at       TEXT NOT NULL
);

CREATE TABLE font_faces (
  id          TEXT PRIMARY KEY,
  family      TEXT NOT NULL,
  style       TEXT NOT NULL CHECK (style IN ('normal', 'italic')),
  weight_min  INTEGER NOT NULL,
  weight_max  INTEGER NOT NULL,
  blob_sha256 TEXT NOT NULL REFERENCES blobs(sha256),
  format      TEXT NOT NULL,                 -- woff2 | woff | truetype | opentype
  scripts     TEXT NOT NULL DEFAULT '[]',
  license     TEXT NOT NULL,                 -- license name/notes confirmed on upload
  uploaded_by TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
);
CREATE INDEX font_faces_family ON font_faces(family);

-- Products ----------------------------------------------------------------------

CREATE TABLE categories (
  id         TEXT PRIMARY KEY,
  parent_id  TEXT REFERENCES categories(id),   -- NULL = top level; max two levels
  name       TEXT NOT NULL,
  sort       INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE products (
  id                    TEXT PRIMARY KEY,
  category_id           TEXT NOT NULL REFERENCES categories(id),
  name                  TEXT NOT NULL,
  description           TEXT NOT NULL DEFAULT '',
  type                  TEXT NOT NULL CHECK (type IN ('drink', 'food', 'other')),
  quantity_value        TEXT NOT NULL DEFAULT '',        -- decimal as text, e.g. "0.33"
  quantity_unit         TEXT NOT NULL DEFAULT '',
  price_amount          INTEGER NOT NULL,
  currency              TEXT NOT NULL DEFAULT 'EUR',
  status                TEXT NOT NULL DEFAULT 'AVAILABLE'
                          CHECK (status IN ('AVAILABLE', 'UNAVAILABLE', 'SOLD_OUT')),
  image_asset_id        TEXT REFERENCES media_assets(id),
  image_instead_of_name INTEGER NOT NULL DEFAULT 0,
  sort                  INTEGER NOT NULL DEFAULT 0,
  archived_at           TEXT,
  created_at            TEXT NOT NULL,
  updated_at            TEXT NOT NULL,
  revision              INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX products_category ON products(category_id);

-- Content -----------------------------------------------------------------------

CREATE TABLE presentations (
  id                TEXT PRIMARY KEY,
  name              TEXT NOT NULL,
  draft             TEXT NOT NULL,             -- full Presentation JSON
  draft_revision    INTEGER NOT NULL DEFAULT 1,
  published_version INTEGER,                   -- NULL = never published
  has_changes       INTEGER NOT NULL DEFAULT 1, -- draft differs from published
  is_template       INTEGER NOT NULL DEFAULT 0,
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL,
  archived_at       TEXT
);

CREATE TABLE presentation_versions (
  presentation_id TEXT NOT NULL REFERENCES presentations(id) ON DELETE CASCADE,
  version         INTEGER NOT NULL,
  doc             TEXT NOT NULL,
  published_at    TEXT NOT NULL,
  published_by    TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (presentation_id, version)
);

CREATE TABLE playlists (
  id                TEXT PRIMARY KEY,
  name              TEXT NOT NULL,
  draft             TEXT NOT NULL,             -- Playlist JSON
  draft_revision    INTEGER NOT NULL DEFAULT 1,
  published_version INTEGER,
  has_changes       INTEGER NOT NULL DEFAULT 1,
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL,
  archived_at       TEXT
);

CREATE TABLE playlist_versions (
  playlist_id  TEXT NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
  version      INTEGER NOT NULL,
  doc          TEXT NOT NULL,
  published_at TEXT NOT NULL,
  published_by TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (playlist_id, version)
);

-- Which content uses which asset (kept in sync on save/publish; used for "unused
-- media", usage display and safe deletion).
CREATE TABLE content_refs (
  asset_id   TEXT NOT NULL,
  owner_type TEXT NOT NULL,     -- presentation_draft | presentation_published | playlist_draft | playlist_published | product
  owner_id   TEXT NOT NULL,
  PRIMARY KEY (asset_id, owner_type, owner_id)
);
CREATE INDEX content_refs_owner ON content_refs(owner_type, owner_id);

-- Zones, schedules, devices ----------------------------------------------------

CREATE TABLE ticker_templates (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  style      TEXT NOT NULL,                     -- JSON TickerTemplate
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE zones (
  id                 TEXT PRIMARY KEY,
  name               TEXT NOT NULL,
  state              TEXT NOT NULL DEFAULT 'OPEN' CHECK (state IN ('OPEN', 'CLOSED')),
  closed_playlist_id TEXT REFERENCES playlists(id),
  ticker_text        TEXT NOT NULL DEFAULT '',
  ticker_visible     INTEGER NOT NULL DEFAULT 0,
  ticker_template_id TEXT REFERENCES ticker_templates(id),
  sort               INTEGER NOT NULL DEFAULT 0,
  created_at         TEXT NOT NULL,
  updated_at         TEXT NOT NULL
);

CREATE TABLE user_zones (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  zone_id TEXT NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
  PRIMARY KEY (user_id, zone_id)
);

CREATE TABLE zone_product_status (
  zone_id    TEXT NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
  product_id TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  status     TEXT NOT NULL CHECK (status IN ('AVAILABLE', 'UNAVAILABLE', 'SOLD_OUT')),
  PRIMARY KEY (zone_id, product_id)
);

CREATE TABLE schedule_rules (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  scope       TEXT NOT NULL CHECK (scope IN ('zone', 'global', 'default')),
  zone_id     TEXT REFERENCES zones(id) ON DELETE CASCADE,
  days        TEXT,                     -- JSON array of ISO weekdays, NULL for default
  start_time  TEXT,
  end_time    TEXT,
  playlist_id TEXT REFERENCES playlists(id),
  enabled     INTEGER NOT NULL DEFAULT 1,
  revision    INTEGER NOT NULL DEFAULT 1,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX schedule_rules_zone ON schedule_rules(zone_id);

CREATE TABLE schedule_prices (
  rule_id    TEXT NOT NULL REFERENCES schedule_rules(id) ON DELETE CASCADE,
  product_id TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  amount     INTEGER NOT NULL,
  currency   TEXT NOT NULL,
  PRIMARY KEY (rule_id, product_id)
);

CREATE TABLE devices (
  id                     TEXT PRIMARY KEY,
  hardware_id            TEXT NOT NULL UNIQUE,
  name                   TEXT NOT NULL,
  zone_id                TEXT REFERENCES zones(id) ON DELETE SET NULL,
  token_hash             TEXT,                 -- NULL = revoked / unpaired
  config                 TEXT NOT NULL,        -- JSON DeviceConfig
  paired_at              TEXT,
  last_seen_at           TEXT,
  last_status            TEXT NOT NULL DEFAULT '{}',
  version                TEXT NOT NULL DEFAULT '',
  ips                    TEXT NOT NULL DEFAULT '[]',
  active_deployment_id   TEXT,
  previous_deployment_id TEXT,
  active_source          TEXT NOT NULL DEFAULT '{}',
  created_at             TEXT NOT NULL,
  updated_at             TEXT NOT NULL
);

CREATE TABLE pairing_requests (
  id          TEXT PRIMARY KEY,
  pin         TEXT NOT NULL,
  hardware_id TEXT NOT NULL,
  info        TEXT NOT NULL,                  -- JSON (hostname, version, ips, arch, screen)
  secret_hash TEXT NOT NULL,                  -- agent proves it created the request
  created_at  TEXT NOT NULL,
  expires_at  TEXT NOT NULL,
  device_id   TEXT,                           -- set when claimed
  token       TEXT                            -- plaintext token, delivered once then cleared
);
CREATE INDEX pairing_pin ON pairing_requests(pin);

CREATE TABLE deployments (
  id            TEXT PRIMARY KEY,
  zone_id       TEXT NOT NULL,
  hash          TEXT NOT NULL,
  manifest      TEXT NOT NULL,
  built_at      TEXT NOT NULL,
  superseded_at TEXT
);
CREATE INDEX deployments_zone ON deployments(zone_id, built_at);

CREATE TABLE deployment_blobs (
  deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  sha256        TEXT NOT NULL,
  PRIMARY KEY (deployment_id, sha256)
);
CREATE INDEX deployment_blobs_sha ON deployment_blobs(sha256);

CREATE TABLE device_deployments (
  device_id     TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  deployment_id TEXT NOT NULL,
  state         TEXT NOT NULL,
  done          INTEGER NOT NULL DEFAULT 0,
  total         INTEGER NOT NULL DEFAULT 0,
  error         TEXT NOT NULL DEFAULT '',
  updated_at    TEXT NOT NULL,
  PRIMARY KEY (device_id, deployment_id)
);

-- Updates -----------------------------------------------------------------------

CREATE TABLE releases (
  version       TEXT PRIMARY KEY,
  source        TEXT NOT NULL,                 -- github | upload
  notes         TEXT NOT NULL DEFAULT '',
  size          INTEGER NOT NULL DEFAULT 0,
  published_at  TEXT NOT NULL DEFAULT '',
  state         TEXT NOT NULL,                 -- available | downloading | ready | failed
  error         TEXT NOT NULL DEFAULT '',
  path          TEXT NOT NULL DEFAULT '',
  updated_at    TEXT NOT NULL
);

CREATE TABLE update_runs (
  id          TEXT PRIMARY KEY,
  kind        TEXT NOT NULL,                   -- server | clients
  version     TEXT NOT NULL,
  state       TEXT NOT NULL,                   -- running | done | failed | rolled_back | stopped
  error       TEXT NOT NULL DEFAULT '',
  started_by  TEXT NOT NULL DEFAULT '',
  started_at  TEXT NOT NULL,
  finished_at TEXT
);

CREATE TABLE device_updates (
  run_id     TEXT NOT NULL REFERENCES update_runs(id) ON DELETE CASCADE,
  device_id  TEXT NOT NULL,
  state      TEXT NOT NULL,                    -- waiting | sent | installing | done | failed | rolled_back
  error      TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  PRIMARY KEY (run_id, device_id)
);
