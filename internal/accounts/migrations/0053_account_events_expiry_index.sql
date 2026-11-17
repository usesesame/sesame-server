-- sesame-migration: create-index-concurrently
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_account_events_expires_at
    ON sesame_account_events (expires_at);
