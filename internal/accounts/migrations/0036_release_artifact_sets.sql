ALTER TABLE sesame_releases
  ADD COLUMN IF NOT EXISTS release_set_digest TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS release_set_verified_at TIMESTAMPTZ;

ALTER TABLE sesame_releases
  ADD CONSTRAINT sesame_releases_release_set_digest_check
  CHECK (
    (release_set_digest = '' AND release_set_verified_at IS NULL)
    OR (release_set_digest ~ '^[0-9a-f]{64}$' AND release_set_verified_at IS NOT NULL)
  );

ALTER TABLE sesame_release_artifacts
  ADD COLUMN IF NOT EXISTS format TEXT NOT NULL DEFAULT 'nsis',
  ADD COLUMN IF NOT EXISTS architecture TEXT NOT NULL DEFAULT 'x86_64',
  ADD COLUMN IF NOT EXISTS updater_capable BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE sesame_release_artifacts
  DROP CONSTRAINT IF EXISTS sesame_release_artifacts_updater_signature_check,
  DROP CONSTRAINT IF EXISTS sesame_release_artifacts_updater_signing_key_id_check,
  DROP CONSTRAINT IF EXISTS sesame_release_artifacts_release_id_artifact_sha256_key;

ALTER TABLE sesame_release_artifacts
  ADD CONSTRAINT sesame_release_artifacts_format_check
    CHECK (format IN ('nsis', 'appimage', 'deb', 'rpm')),
  ADD CONSTRAINT sesame_release_artifacts_architecture_check
    CHECK (architecture IN ('x86_64', 'aarch64')),
  ADD CONSTRAINT sesame_release_artifacts_updater_evidence_check
    CHECK (
      (updater_capable AND LENGTH(updater_signature) >= 64 AND LENGTH(updater_signing_key_id) BETWEEN 1 AND 120)
      OR (NOT updater_capable AND updater_signature = '' AND updater_signing_key_id = '')
    ),
  ADD CONSTRAINT sesame_release_artifacts_release_format_architecture_key
    UNIQUE (release_id, format, architecture);

CREATE OR REPLACE FUNCTION sesame_release_set_is_immutable()
RETURNS TRIGGER AS $$
BEGIN
  IF NEW.channel IS DISTINCT FROM OLD.channel
    OR NEW.platform IS DISTINCT FROM OLD.platform
    OR NEW.architecture IS DISTINCT FROM OLD.architecture
    OR NEW.version IS DISTINCT FROM OLD.version
    OR NEW.download_url IS DISTINCT FROM OLD.download_url
    OR NEW.artifact_object_key IS DISTINCT FROM OLD.artifact_object_key
    OR NEW.sha256 IS DISTINCT FROM OLD.sha256
    OR NEW.signature IS DISTINCT FROM OLD.signature
    OR NEW.signing_key_id IS DISTINCT FROM OLD.signing_key_id
    OR NEW.supported_windows IS DISTINCT FROM OLD.supported_windows
    OR NEW.release_notes_url IS DISTINCT FROM OLD.release_notes_url
    OR NEW.release_set_digest IS DISTINCT FROM OLD.release_set_digest
    OR NEW.release_set_verified_at IS DISTINCT FROM OLD.release_set_verified_at
  THEN
    RAISE EXCEPTION 'verified release set is immutable';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS sesame_release_set_no_update ON sesame_releases;
CREATE TRIGGER sesame_release_set_no_update
BEFORE UPDATE ON sesame_releases
FOR EACH ROW
WHEN (OLD.release_set_verified_at IS NOT NULL)
EXECUTE FUNCTION sesame_release_set_is_immutable();
