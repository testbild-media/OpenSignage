-- Media: remember which revision of an asset has finished processing.
--
-- media_assets.revision is the newest revision (the newest upload); its status says whether
-- *that* revision is processed. ready_revision is the newest revision whose variants are
-- complete and usable by screens (0 = none yet). A replacement upload or a reprocess makes
-- the status 'processing' but never takes ready_revision away, so deployments keep shipping
-- the last good revision until the new one is ready.
ALTER TABLE media_assets ADD COLUMN ready_revision INTEGER NOT NULL DEFAULT 0;

UPDATE media_assets SET ready_revision = revision WHERE status = 'ready';

UPDATE media_assets SET ready_revision = COALESCE(
  (SELECT MAX(v.revision) FROM media_variants v WHERE v.asset_id = media_assets.id), 0)
 WHERE status <> 'ready';
