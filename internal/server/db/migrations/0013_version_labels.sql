-- Named versions of a presentation ("Summer menu 2027"). The version history keeps named
-- versions for good: they are never thinned out or removed by the version limit.
ALTER TABLE presentation_versions ADD COLUMN label TEXT NOT NULL DEFAULT '';
