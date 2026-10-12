ALTER TABLE sesame_accounts
  ADD COLUMN IF NOT EXISTS security_generation BIGINT NOT NULL DEFAULT 0;

ALTER TABLE sesame_account_tokens
  ADD COLUMN IF NOT EXISTS security_generation BIGINT NOT NULL DEFAULT 0;

ALTER TABLE sesame_desktop_link_codes
  ADD COLUMN IF NOT EXISTS security_generation BIGINT NOT NULL DEFAULT 0;
