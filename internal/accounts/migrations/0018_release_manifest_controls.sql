-- 0018_release_manifest_controls: canonical update-manifest controls.
--
-- A release row is the canonical signed artifact manifest for one
-- channel/platform/architecture tuple. Rollout and kill-switch decisions are
-- made from these fields, not from frontend fallback data.

ALTER TABLE sesame_releases
  ADD COLUMN IF NOT EXISTS architecture TEXT NOT NULL DEFAULT 'x86_64',
  ADD COLUMN IF NOT EXISTS rollout_percent INTEGER NOT NULL DEFAULT 100,
  ADD COLUMN IF NOT EXISTS update_enabled BOOLEAN NOT NULL DEFAULT TRUE,
  ADD COLUMN IF NOT EXISTS kill_switch BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS manifest_revision BIGINT NOT NULL DEFAULT 1;

ALTER TABLE sesame_releases
  ADD CONSTRAINT sesame_releases_rollout_percent_check CHECK (rollout_percent BETWEEN 0 AND 100);

CREATE INDEX IF NOT EXISTS sesame_releases_update_manifest_idx
  ON sesame_releases(channel, platform, architecture, published_at DESC)
  WHERE status = 'published' AND update_enabled = TRUE AND kill_switch = FALSE;

CREATE OR REPLACE FUNCTION sesame_increment_release_manifest_revision()
RETURNS TRIGGER AS $$
BEGIN
  IF TG_OP = 'UPDATE' AND (
    NEW.download_url IS DISTINCT FROM OLD.download_url OR
    NEW.sha256 IS DISTINCT FROM OLD.sha256 OR
    NEW.signature IS DISTINCT FROM OLD.signature OR
    NEW.signing_key_id IS DISTINCT FROM OLD.signing_key_id OR
    NEW.supported_windows IS DISTINCT FROM OLD.supported_windows OR
    NEW.rollback_notice IS DISTINCT FROM OLD.rollback_notice OR
    NEW.status IS DISTINCT FROM OLD.status OR
    NEW.rollout_percent IS DISTINCT FROM OLD.rollout_percent OR
    NEW.update_enabled IS DISTINCT FROM OLD.update_enabled OR
    NEW.kill_switch IS DISTINCT FROM OLD.kill_switch
  ) THEN
    NEW.manifest_revision := OLD.manifest_revision + 1;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS sesame_release_manifest_revision ON sesame_releases;
CREATE TRIGGER sesame_release_manifest_revision
BEFORE UPDATE ON sesame_releases
FOR EACH ROW EXECUTE FUNCTION sesame_increment_release_manifest_revision();
