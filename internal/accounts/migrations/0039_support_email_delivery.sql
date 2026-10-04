ALTER TABLE sesame_account_notification_preferences
    ALTER COLUMN support_replies SET DEFAULT TRUE;

UPDATE sesame_account_notification_preferences
    SET support_replies = TRUE, updated_at = NOW()
    WHERE support_replies = FALSE;

ALTER TABLE sesame_email_outbox DROP CONSTRAINT IF EXISTS sesame_email_outbox_kind_check;
ALTER TABLE sesame_email_outbox ADD CONSTRAINT sesame_email_outbox_kind_check CHECK (kind IN (
    'verify-email', 'recover-password', 'change-email',
    'security-sign-in', 'security-password-changed', 'security-email-changed',
    'security-passkey-added', 'security-passkey-removed', 'security-desktop-linked', 'security-desktop-revoked',
    'beta-release', 'support-reply', 'product-announcement',
    'support-receipt', 'support-staff-notify'
));
