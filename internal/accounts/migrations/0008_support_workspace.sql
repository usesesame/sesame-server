-- 0008_support_workspace: turn the flat support intake into a ticketing system.
-- Vault-blind: replies and notes go through the same secret-shape detection
-- as intake. No vault data, passwords, TOTP seeds, or keys are ever stored.

ALTER TABLE sesame_support_requests
  ADD COLUMN IF NOT EXISTS assigned_admin_id TEXT
    REFERENCES sesame_admin_accounts(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS priority TEXT NOT NULL DEFAULT 'normal'
    CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
  ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  ADD COLUMN IF NOT EXISTS closed_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS closed_by TEXT
    REFERENCES sesame_admin_accounts(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS first_response_at TIMESTAMPTZ;

-- Replace the old two-value status check with the full workflow set.
-- Existing 'received' rows become 'open'; existing 'closed' rows stay 'closed'.
ALTER TABLE sesame_support_requests DROP CONSTRAINT IF EXISTS sesame_support_requests_status_check;
UPDATE sesame_support_requests SET status = 'open' WHERE status = 'received';
ALTER TABLE sesame_support_requests
  ADD CONSTRAINT sesame_support_requests_status_check
  CHECK (status IN ('open', 'in_progress', 'waiting', 'closed'));

CREATE INDEX IF NOT EXISTS sesame_support_requests_status_idx
  ON sesame_support_requests(status, updated_at DESC);
CREATE INDEX IF NOT EXISTS sesame_support_requests_assigned_idx
  ON sesame_support_requests(assigned_admin_id) WHERE assigned_admin_id IS NOT NULL;

-- Conversation thread: each row is either an inbound user message (the
-- original intake or a future user reply) or an outbound staff reply.
CREATE TABLE IF NOT EXISTS sesame_support_messages (
  id             TEXT PRIMARY KEY,
  ticket_id      TEXT NOT NULL REFERENCES sesame_support_requests(id) ON DELETE CASCADE,
  author_role    TEXT NOT NULL CHECK (author_role IN ('user', 'staff')),
  admin_id       TEXT REFERENCES sesame_admin_accounts(id) ON DELETE SET NULL,
  admin_email    TEXT NOT NULL DEFAULT '',
  body           TEXT NOT NULL CHECK (LENGTH(body) > 0 AND LENGTH(body) <= 8000),
  sent_via_email BOOLEAN NOT NULL DEFAULT FALSE,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sesame_support_messages_ticket_idx
  ON sesame_support_messages(ticket_id, created_at);

-- Internal notes: visible only to staff, never sent to the user.
CREATE TABLE IF NOT EXISTS sesame_support_notes (
  id          TEXT PRIMARY KEY,
  ticket_id   TEXT NOT NULL REFERENCES sesame_support_requests(id) ON DELETE CASCADE,
  admin_id    TEXT NOT NULL REFERENCES sesame_admin_accounts(id) ON DELETE SET NULL,
  admin_email TEXT NOT NULL,
  body        TEXT NOT NULL CHECK (LENGTH(body) > 0 AND LENGTH(body) <= 4000),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sesame_support_notes_ticket_idx
  ON sesame_support_notes(ticket_id, created_at);

-- Migrate the original intake message into the messages table so the
-- conversation thread starts complete.
INSERT INTO sesame_support_messages (id, ticket_id, author_role, body, created_at)
SELECT id, id, 'user', message, created_at
FROM sesame_support_requests
WHERE NOT EXISTS (
  SELECT 1 FROM sesame_support_messages m WHERE m.ticket_id = sesame_support_requests.id
);
