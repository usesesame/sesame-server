-- 0010_email_outbox: durable, retryable queue for account-action emails.
-- Messages are written transactionally with the account change, then delivered
-- by a background worker. Status moves through a leased processing claim
-- before becoming delivered, pending for retry, or terminally failed.
-- Delivered and expired failed rows are purged by scheduled maintenance.

CREATE TABLE IF NOT EXISTS sesame_email_outbox (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind            TEXT NOT NULL CHECK (kind IN ('verify-email', 'recover-password', 'change-email')),
    to_email        TEXT NOT NULL,
    action_url      TEXT NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'delivered', 'failed')),
    attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    error_message   TEXT,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_until     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_email_outbox_pending
    ON sesame_email_outbox (next_attempt_at, lease_until, status)
    WHERE status IN ('pending', 'processing');

CREATE INDEX IF NOT EXISTS idx_email_outbox_stale
    ON sesame_email_outbox (updated_at, status)
    WHERE status IN ('delivered', 'failed');
