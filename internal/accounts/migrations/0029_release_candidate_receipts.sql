-- Persist the exact pipeline-signed receipt beside the immutable artifact so
-- the account API can carry it to the desktop updater. Existing artifacts do
-- not have a recoverable receipt and therefore remain ineligible for desktop
-- update delivery until a new candidate is submitted.
ALTER TABLE sesame_release_artifacts
  ADD COLUMN IF NOT EXISTS candidate_payload TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS candidate_signing_key_id TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS candidate_signature TEXT NOT NULL DEFAULT '';

ALTER TABLE sesame_release_artifacts
  ADD CONSTRAINT sesame_release_artifacts_candidate_receipt_check
  CHECK (
    (candidate_payload = '' AND candidate_signing_key_id = '' AND candidate_signature = '')
    OR
    (candidate_payload <> '' AND candidate_signing_key_id <> '' AND candidate_signature <> '')
  );
