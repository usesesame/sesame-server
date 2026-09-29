UPDATE sesame_email_outbox
SET status = 'failed',
    error_message = 'action_url_encryption_upgrade',
    lease_until = NULL,
    action_url = '',
    updated_at = now()
WHERE action_url <> '' AND status IN ('pending', 'processing');

UPDATE sesame_email_outbox
SET action_url = ''
WHERE action_url <> '';
