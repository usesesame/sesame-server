-- Account-side support state is deliberately separate from the message body.
-- It gives people a reliable unread indicator and a bounded way to reopen a
-- recently resolved request without retaining or copying support content.
ALTER TABLE sesame_support_requests
  ADD COLUMN IF NOT EXISTS account_last_read_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  ADD COLUMN IF NOT EXISTS account_reopen_until TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS browser_integration TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS request_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS sesame_support_messages_unread_idx
  ON sesame_support_messages(ticket_id, author_role, created_at);

-- Relate opt-in support-reply email to its safe, already-reviewed staff
-- message. The outbox retains delivery state and retry scheduling; it never
-- contains vault material because the support-message validation applies first.
ALTER TABLE sesame_email_outbox
  ADD COLUMN IF NOT EXISTS support_message_id TEXT
    REFERENCES sesame_support_messages(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS sesame_email_outbox_support_message_idx
  ON sesame_email_outbox(support_message_id)
  WHERE support_message_id IS NOT NULL;
