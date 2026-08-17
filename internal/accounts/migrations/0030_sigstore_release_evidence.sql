-- Sigstore is publisher/provenance evidence for the exact artifact. It does
-- not become Windows publisher signing. Existing rows default to lab and stay
-- ineligible until a newly signed candidate supplies the complete record.
ALTER TABLE sesame_release_artifacts
  ADD COLUMN IF NOT EXISTS distribution_class TEXT NOT NULL DEFAULT 'lab',
  ADD COLUMN IF NOT EXISTS sigstore_evidence JSONB,
  ADD COLUMN IF NOT EXISTS sigstore_verified BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS sigstore_issuer TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS sigstore_identity TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS sigstore_bundle_sha256 TEXT NOT NULL DEFAULT '';

ALTER TABLE sesame_release_artifacts
  ADD CONSTRAINT sesame_release_artifacts_distribution_class_check
  CHECK (distribution_class IN ('lab', 'early_access', 'production')),
  ADD CONSTRAINT sesame_release_artifacts_sigstore_evidence_check
  CHECK (
    (sigstore_verified = FALSE AND sigstore_issuer = '' AND sigstore_identity = '' AND sigstore_bundle_sha256 = '')
    OR
    (sigstore_verified = TRUE AND sigstore_evidence IS NOT NULL AND sigstore_issuer <> '' AND sigstore_identity <> '' AND sigstore_bundle_sha256 ~ '^[0-9a-f]{64}$')
  ),
  ADD CONSTRAINT sesame_release_artifacts_distribution_trust_check
  CHECK (
    distribution_class = 'lab'
    OR (distribution_class = 'early_access' AND sigstore_verified = TRUE AND authenticode_verified = FALSE)
    OR (distribution_class = 'production' AND sigstore_verified = TRUE AND authenticode_verified = TRUE)
  );
