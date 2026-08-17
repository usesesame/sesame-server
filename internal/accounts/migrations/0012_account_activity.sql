-- User-visible account security history. Metadata is intentionally restricted
-- to safe labels such as a browser family, a device name, or a release version.
-- It must never contain passwords, raw tokens, vault identifiers, or raw IPs.
CREATE TABLE IF NOT EXISTS sesame_account_events (
    id          TEXT PRIMARY KEY,
    account_id  TEXT NOT NULL REFERENCES sesame_accounts(id) ON DELETE CASCADE,
    event_type  TEXT NOT NULL,
    label       TEXT NOT NULL DEFAULT '',
    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_account_events_visible
    ON sesame_account_events (account_id, created_at DESC);
