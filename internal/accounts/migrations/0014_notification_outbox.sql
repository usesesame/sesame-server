-- Notification content is stored in the durable queue so retries preserve the
-- exact user-visible message. Security notifications do not depend on prefs.
ALTER TABLE sesame_email_outbox
    ADD COLUMN IF NOT EXISTS subject TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS body TEXT NOT NULL DEFAULT '';

ALTER TABLE sesame_email_outbox DROP CONSTRAINT IF EXISTS sesame_email_outbox_kind_check;
ALTER TABLE sesame_email_outbox ADD CONSTRAINT sesame_email_outbox_kind_check CHECK (kind IN (
    'verify-email', 'recover-password', 'change-email',
    'security-sign-in', 'security-password-changed', 'security-email-changed',
    'security-passkey-added', 'security-passkey-removed', 'security-desktop-linked', 'security-desktop-revoked',
    'beta-release', 'support-reply', 'product-announcement'
));
