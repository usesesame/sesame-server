-- 0007_admin_audit_immutability: retain audit rows exactly as written, even
-- when an administrator account is later deleted.

ALTER TABLE sesame_admin_audit_log
  DROP CONSTRAINT IF EXISTS sesame_admin_audit_log_admin_id_fkey;

CREATE OR REPLACE FUNCTION sesame_reject_admin_audit_mutation()
RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'sesame_admin_audit_log is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS sesame_admin_audit_log_append_only ON sesame_admin_audit_log;
CREATE TRIGGER sesame_admin_audit_log_append_only
BEFORE UPDATE OR DELETE ON sesame_admin_audit_log
FOR EACH ROW EXECUTE FUNCTION sesame_reject_admin_audit_mutation();
