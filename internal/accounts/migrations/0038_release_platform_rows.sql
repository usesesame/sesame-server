-- Migration 0004 shaped every sesame_releases row like the Windows updater:
-- a 64-character updater signature, a signing key id, and non-empty supported
-- Windows versions. Migration 0036 relaxed the same rule on
-- sesame_release_artifacts but missed this table, so the first Linux release
-- candidate failed its INSERT with sesame_releases_signature_check and every
-- Linux submission returned 503.
--
-- A Linux release carries no updater-capable artifact, so its primary
-- artifact contributes an empty signature and signing key id, and it must not
-- declare supported Windows versions. The relaxed checks accept either the
-- Windows shape or that Linux shape, and never a half-filled row.

ALTER TABLE sesame_releases
  DROP CONSTRAINT IF EXISTS sesame_releases_signature_check,
  DROP CONSTRAINT IF EXISTS sesame_releases_signing_key_id_check,
  DROP CONSTRAINT IF EXISTS sesame_releases_supported_windows_check;

ALTER TABLE sesame_releases
  ADD CONSTRAINT sesame_releases_signature_check
    CHECK (
      (LENGTH(signature) >= 64 AND LENGTH(signing_key_id) BETWEEN 1 AND 120)
      OR (signature = '' AND signing_key_id = '')
    ),
  ADD CONSTRAINT sesame_releases_supported_windows_check
    CHECK (supported_windows = '' OR LENGTH(supported_windows) BETWEEN 1 AND 200);
