-- Artifact paths are opaque object keys. The API never stores or redirects to
-- a permanent public artifact URL for new releases.
ALTER TABLE sesame_releases
  ADD COLUMN IF NOT EXISTS artifact_object_key TEXT NOT NULL DEFAULT '';
ALTER TABLE sesame_release_artifacts
  ADD COLUMN IF NOT EXISTS artifact_object_key TEXT NOT NULL DEFAULT '';
ALTER TABLE sesame_download_tickets
  ADD COLUMN IF NOT EXISTS artifact_object_key TEXT NOT NULL DEFAULT '';
ALTER TABLE sesame_desktop_update_tickets
  ADD COLUMN IF NOT EXISTS artifact_object_key TEXT NOT NULL DEFAULT '';

ALTER TABLE sesame_releases
  DROP CONSTRAINT IF EXISTS sesame_releases_download_url_check;
ALTER TABLE sesame_releases
  ADD CONSTRAINT sesame_releases_download_url_check CHECK (download_url = '' OR download_url LIKE 'https://%');

ALTER TABLE sesame_release_artifacts
  DROP CONSTRAINT IF EXISTS sesame_release_artifacts_artifact_url_check;
ALTER TABLE sesame_release_artifacts
  ADD CONSTRAINT sesame_release_artifacts_artifact_url_check CHECK (artifact_url = '' OR artifact_url LIKE 'https://%');

ALTER TABLE sesame_download_tickets
  DROP CONSTRAINT IF EXISTS sesame_download_tickets_artifact_url_check;
ALTER TABLE sesame_download_tickets
  ADD CONSTRAINT sesame_download_tickets_artifact_url_check CHECK (artifact_url = '' OR artifact_url LIKE 'https://%');

ALTER TABLE sesame_desktop_update_tickets
  DROP CONSTRAINT IF EXISTS sesame_desktop_update_tickets_artifact_url_check;
ALTER TABLE sesame_desktop_update_tickets
  ADD CONSTRAINT sesame_desktop_update_tickets_artifact_url_check CHECK (artifact_url = '' OR artifact_url LIKE 'https://%');

ALTER TABLE sesame_releases
  ADD CONSTRAINT sesame_releases_artifact_object_key_check
  CHECK (artifact_object_key = '' OR artifact_object_key ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,1023}$');
ALTER TABLE sesame_release_artifacts
  ADD CONSTRAINT sesame_release_artifacts_object_key_check
  CHECK (artifact_object_key = '' OR artifact_object_key ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,1023}$');
ALTER TABLE sesame_download_tickets
  ADD CONSTRAINT sesame_download_tickets_object_key_check
  CHECK (artifact_object_key = '' OR artifact_object_key ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,1023}$');
ALTER TABLE sesame_desktop_update_tickets
  ADD CONSTRAINT sesame_desktop_update_tickets_object_key_check
  CHECK (artifact_object_key = '' OR artifact_object_key ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,1023}$');

ALTER TABLE sesame_releases
  ADD CONSTRAINT sesame_releases_channel_check CHECK (channel IN ('owner', 'beta')) NOT VALID;
