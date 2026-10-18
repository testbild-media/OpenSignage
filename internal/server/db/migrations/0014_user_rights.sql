-- User rights. Operators with scope 'all' keep every right as before; scope 'zones' limits an
-- operator to the zones in user_zones (view or edit) and to the rights below.
ALTER TABLE users ADD COLUMN scope TEXT NOT NULL DEFAULT 'all' CHECK (scope IN ('all', 'zones'));
ALTER TABLE users ADD COLUMN can_create INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN media_right TEXT NOT NULL DEFAULT 'use' CHECK (media_right IN ('use', 'upload', 'manage'));
ALTER TABLE users ADD COLUMN products_right TEXT NOT NULL DEFAULT 'view' CHECK (products_right IN ('view', 'edit'));
ALTER TABLE users ADD COLUMN devices_right INTEGER NOT NULL DEFAULT 0;

-- Per zone: view (read only) or edit. Users (role user) have 'edit' for the live operation.
ALTER TABLE user_zones ADD COLUMN access TEXT NOT NULL DEFAULT 'edit' CHECK (access IN ('view', 'edit'));

-- Who created content or uploaded media: operators limited to zones may edit their own
-- presentations and playlists that run nowhere else, and rename or move their own uploads.
ALTER TABLE presentations ADD COLUMN created_by TEXT;
ALTER TABLE playlists ADD COLUMN created_by TEXT;
ALTER TABLE media_assets ADD COLUMN uploaded_by TEXT;
ALTER TABLE media_folders ADD COLUMN created_by TEXT;
