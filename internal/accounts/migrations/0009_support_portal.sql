-- 0009_support_portal: align new support tickets with the support workspace.
-- Migration 0008 replaced the allowed status values but intentionally did not
-- rewrite the existing column default. New tickets now start in the open queue.

ALTER TABLE sesame_support_requests
  ALTER COLUMN status SET DEFAULT 'open';
