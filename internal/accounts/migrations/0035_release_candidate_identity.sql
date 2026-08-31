ALTER TABLE sesame_release_artifacts
  ADD COLUMN IF NOT EXISTS candidate_identity TEXT NOT NULL DEFAULT '';

ALTER TABLE sesame_release_artifacts
  ADD CONSTRAINT sesame_release_artifacts_candidate_identity_check
  CHECK (candidate_identity = '' OR candidate_identity ~ '^[0-9a-f]{64}$');
