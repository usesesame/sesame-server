UPDATE sesame_email_outbox
SET status = 'failed',
    error_message = 'action_url_encryption_upgrade',
    lease_until = NULL,
    action_url = '',
    updated_at = now()
WHERE kind IN ('verify-email', 'recover-password', 'change-email', 'support-reply')
  AND action_url LIKE '%#token=%'
  AND status IN ('pending', 'processing');

UPDATE sesame_email_outbox
SET action_url = ''
WHERE action_url <> '';
