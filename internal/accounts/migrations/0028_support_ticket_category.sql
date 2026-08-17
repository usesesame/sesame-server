-- 0028_support_ticket_category: let a user categorize a support request at
-- intake (account, import, sync, browser helper, billing, bug, or general)
-- so staff can triage without reading the message first, and filter the
-- admin ticket queue by it the same way it already filters by status and
-- priority. Existing rows default to 'general', which is also the default
-- for a request that omits the field.

ALTER TABLE sesame_support_requests
  ADD COLUMN IF NOT EXISTS category TEXT NOT NULL DEFAULT 'general'
  CHECK (category IN ('general', 'account', 'import', 'sync', 'browser_helper', 'billing', 'bug'));

CREATE INDEX IF NOT EXISTS sesame_support_requests_category_idx
  ON sesame_support_requests(category);
