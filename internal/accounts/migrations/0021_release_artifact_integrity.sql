-- 0021_release_artifact_integrity: release artifacts are pipeline evidence,
-- never editable release-form fields. The control plane can change rollout,
-- state, and kill-switch policy without changing the artifact people install.

ALTER TABLE sesame_releases
  DROP CONSTRAINT IF EXISTS sesame_releases_channel_platform_version_key;

ALTER TABLE sesame_releases
  ADD CONSTRAINT sesame_releases_channel_platform_architecture_version_key
  UNIQUE (channel, platform, architecture, version);

CREATE TABLE IF NOT EXISTS sesame_release_artifacts (
  id                          TEXT PRIMARY KEY,
  release_id                  TEXT NOT NULL REFERENCES sesame_releases(id) ON DELETE RESTRICT,
  artifact_url                TEXT NOT NULL CHECK (artifact_url LIKE 'https://%'),
  artifact_sha256             TEXT NOT NULL CHECK (artifact_sha256 ~ '^[0-9a-f]{64}$'),
  artifact_bytes              BIGINT NOT NULL CHECK (artifact_bytes > 0),
  updater_signature           TEXT NOT NULL CHECK (LENGTH(updater_signature) >= 64),
  updater_signing_key_id      TEXT NOT NULL CHECK (LENGTH(updater_signing_key_id) BETWEEN 1 AND 120),
  authenticode_evidence       JSONB,
  authenticode_verified       BOOLEAN NOT NULL DEFAULT FALSE,
  authenticode_subject        TEXT NOT NULL DEFAULT '',
  authenticode_thumbprint     TEXT NOT NULL DEFAULT '',
  verified_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  created_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (release_id, artifact_sha256)
);

CREATE INDEX IF NOT EXISTS sesame_release_artifacts_release_idx
  ON sesame_release_artifacts (release_id, created_at DESC);

CREATE OR REPLACE FUNCTION sesame_release_artifact_is_immutable()
RETURNS TRIGGER AS $$
BEGIN
  RAISE EXCEPTION 'release artifact evidence is immutable';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS sesame_release_artifact_no_update ON sesame_release_artifacts;
CREATE TRIGGER sesame_release_artifact_no_update
BEFORE UPDATE OR DELETE ON sesame_release_artifacts
FOR EACH ROW EXECUTE FUNCTION sesame_release_artifact_is_immutable();

-- Existing release rows are legacy manifests. They remain readable for
-- rollback, but newly accepted candidates must create an artifact row.
