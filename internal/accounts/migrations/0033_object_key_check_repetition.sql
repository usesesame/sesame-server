-- The object-key constraints added in 0023 use `{0,1023}`, but PostgreSQL caps
-- a bounded repetition at 255 and raises "invalid repetition count(s)" when it
-- evaluates one. The regex only runs when the key is non-empty, so every table
-- accepted an empty key and rejected every real one. Nothing had ever submitted
-- a release with an object key until now, which is why it stayed hidden.
--
-- The length is enforced separately so the bound no longer lives in the regex.

ALTER TABLE sesame_releases DROP CONSTRAINT IF EXISTS sesame_releases_artifact_object_key_check;
ALTER TABLE sesame_releases
  ADD CONSTRAINT sesame_releases_artifact_object_key_check
  CHECK (artifact_object_key = '' OR (length(artifact_object_key) <= 1024 AND artifact_object_key ~ '^[A-Za-z0-9][A-Za-z0-9._/-]*$'));

ALTER TABLE sesame_release_artifacts DROP CONSTRAINT IF EXISTS sesame_release_artifacts_object_key_check;
ALTER TABLE sesame_release_artifacts
  ADD CONSTRAINT sesame_release_artifacts_object_key_check
  CHECK (artifact_object_key = '' OR (length(artifact_object_key) <= 1024 AND artifact_object_key ~ '^[A-Za-z0-9][A-Za-z0-9._/-]*$'));

ALTER TABLE sesame_download_tickets DROP CONSTRAINT IF EXISTS sesame_download_tickets_object_key_check;
ALTER TABLE sesame_download_tickets
  ADD CONSTRAINT sesame_download_tickets_object_key_check
  CHECK (artifact_object_key = '' OR (length(artifact_object_key) <= 1024 AND artifact_object_key ~ '^[A-Za-z0-9][A-Za-z0-9._/-]*$'));

ALTER TABLE sesame_desktop_update_tickets DROP CONSTRAINT IF EXISTS sesame_desktop_update_tickets_object_key_check;
ALTER TABLE sesame_desktop_update_tickets
  ADD CONSTRAINT sesame_desktop_update_tickets_object_key_check
  CHECK (artifact_object_key = '' OR (length(artifact_object_key) <= 1024 AND artifact_object_key ~ '^[A-Za-z0-9][A-Za-z0-9._/-]*$'));
