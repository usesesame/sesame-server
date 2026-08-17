-- Account-visible desktop metadata. These values are supplied by the desktop
-- client and are intentionally limited to device/app capability information.
ALTER TABLE sesame_desktop_connections
    ADD COLUMN IF NOT EXISTS app_version TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS platform TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS architecture TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS update_channel TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS protocol_version INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS browser_helper_capable BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS browser_helper_last_observed_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_desktop_connections_account_seen
    ON sesame_desktop_connections (account_id, last_seen_at DESC);
