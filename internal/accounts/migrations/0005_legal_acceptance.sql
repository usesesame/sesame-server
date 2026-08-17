-- 0005_legal_acceptance: preserve the legal documents acknowledged when a
-- website account is created. These fields are deliberately account-only;
-- the service remains vault-blind.

ALTER TABLE sesame_accounts
  ADD COLUMN IF NOT EXISTS terms_accepted_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS terms_version TEXT,
  ADD COLUMN IF NOT EXISTS privacy_acknowledged_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS privacy_version TEXT;
