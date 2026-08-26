-- The publish guard and the update-manifest query each spelled out the
-- distribution-eligibility rule separately, and the copies had drifted: both
-- accepted an early_access artifact regardless of authenticode_verified,
-- where the release-candidate ingest rule (release_candidates.go) only ever
-- stores an early_access artifact with authenticode_verified = FALSE. A
-- generated column makes the rule one expression that every query reads.
ALTER TABLE sesame_release_artifacts
  ADD COLUMN IF NOT EXISTS eligible_for_distribution BOOLEAN GENERATED ALWAYS AS (
    sigstore_verified AND (
      (distribution_class = 'early_access' AND NOT authenticode_verified) OR
      (distribution_class = 'production' AND authenticode_verified)
    )
  ) STORED;

CREATE INDEX IF NOT EXISTS sesame_release_artifacts_eligible_idx
  ON sesame_release_artifacts(release_id)
  WHERE eligible_for_distribution;
