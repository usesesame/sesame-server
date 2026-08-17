CREATE TABLE IF NOT EXISTS sesame_account_notification_preferences (
    account_id             TEXT PRIMARY KEY REFERENCES sesame_accounts(id) ON DELETE CASCADE,
    beta_releases          BOOLEAN NOT NULL DEFAULT FALSE,
    support_replies        BOOLEAN NOT NULL DEFAULT FALSE,
    product_announcements  BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
