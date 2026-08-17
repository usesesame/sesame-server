-- 0011_admin_totp_replay: prevent replay of admin TOTP codes by recording the
-- last accepted time-step counter per administrator. A code is accepted only
-- if its counter is greater than the stored value.

ALTER TABLE sesame_admin_accounts
    ADD COLUMN IF NOT EXISTS totp_last_used_counter BIGINT NOT NULL DEFAULT 0;

COMMENT ON COLUMN sesame_admin_accounts.totp_last_used_counter IS
    'The TOTP time-step counter of the most recently accepted code; used for replay prevention.';
