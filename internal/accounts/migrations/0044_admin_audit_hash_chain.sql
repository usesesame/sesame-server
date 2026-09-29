CREATE FUNCTION sesame_admin_audit_row_hash(
  previous_hash BYTEA,
  chain_seq BIGINT,
  row_id BIGINT,
  admin_id TEXT,
  admin_email TEXT,
  action TEXT,
  target_type TEXT,
  target_id TEXT,
  detail JSONB,
  ip_hash TEXT,
  created_at TIMESTAMPTZ
) RETURNS BYTEA AS $$
  SELECT sha256(previous_hash || convert_to(jsonb_build_array(
    chain_seq, row_id, admin_id, admin_email, action, target_type, target_id, detail, ip_hash,
    to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
  )::text, 'UTF8'))
$$ LANGUAGE sql IMMUTABLE;

ALTER TABLE sesame_admin_audit_log
  ADD COLUMN IF NOT EXISTS chain_seq BIGINT,
  ADD COLUMN IF NOT EXISTS prev_hash BYTEA,
  ADD COLUMN IF NOT EXISTS hash BYTEA;

DROP TRIGGER IF EXISTS sesame_admin_audit_log_append_only ON sesame_admin_audit_log;

DO $$
DECLARE
  audit_row RECORD;
  previous_hash BYTEA := decode(repeat('00', 32), 'hex');
  chain_position BIGINT := 0;
  row_hash BYTEA;
BEGIN
  FOR audit_row IN SELECT * FROM sesame_admin_audit_log ORDER BY id LOOP
    chain_position := chain_position + 1;
    row_hash := sesame_admin_audit_row_hash(
      previous_hash, chain_position, audit_row.id, audit_row.admin_id, audit_row.admin_email,
      audit_row.action, audit_row.target_type, audit_row.target_id, audit_row.detail,
      audit_row.ip_hash, audit_row.created_at
    );
    UPDATE sesame_admin_audit_log
      SET chain_seq = chain_position, prev_hash = previous_hash, hash = row_hash
      WHERE id = audit_row.id;
    previous_hash := row_hash;
  END LOOP;
END $$;

CREATE TRIGGER sesame_admin_audit_log_append_only
BEFORE UPDATE OR DELETE ON sesame_admin_audit_log
FOR EACH ROW EXECUTE FUNCTION sesame_reject_admin_audit_mutation();

ALTER TABLE sesame_admin_audit_log
  ALTER COLUMN chain_seq SET NOT NULL,
  ALTER COLUMN prev_hash SET NOT NULL,
  ALTER COLUMN hash SET NOT NULL,
  ADD CONSTRAINT sesame_admin_audit_log_chain_seq_key UNIQUE (chain_seq),
  ADD CONSTRAINT sesame_admin_audit_log_prev_hash_length CHECK (octet_length(prev_hash) = 32),
  ADD CONSTRAINT sesame_admin_audit_log_hash_length CHECK (octet_length(hash) = 32);

CREATE TABLE IF NOT EXISTS sesame_admin_audit_chain_head (
  singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
  head_seq BIGINT NOT NULL,
  head_hash BYTEA NOT NULL,
  CONSTRAINT sesame_admin_audit_chain_head_hash_length CHECK (octet_length(head_hash) = 32)
);

INSERT INTO sesame_admin_audit_chain_head (singleton, head_seq, head_hash)
VALUES (TRUE, 0, decode(repeat('00', 32), 'hex'))
ON CONFLICT (singleton) DO NOTHING;

UPDATE sesame_admin_audit_chain_head head
SET head_seq = COALESCE((SELECT MAX(chain_seq) FROM sesame_admin_audit_log), 0),
    head_hash = COALESCE((SELECT hash FROM sesame_admin_audit_log ORDER BY chain_seq DESC LIMIT 1), decode(repeat('00', 32), 'hex'))
WHERE singleton;

CREATE OR REPLACE FUNCTION sesame_chain_admin_audit_row() RETURNS trigger
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
  previous_hash BYTEA;
  previous_seq BIGINT;
BEGIN
  SELECT head_seq, head_hash INTO previous_seq, previous_hash
  FROM sesame_admin_audit_chain_head WHERE singleton FOR UPDATE;
  IF previous_seq IS NULL OR previous_hash IS NULL THEN
    RAISE EXCEPTION 'sesame admin audit chain head is missing';
  END IF;
  NEW.chain_seq := previous_seq + 1;
  NEW.prev_hash := previous_hash;
  NEW.hash := sesame_admin_audit_row_hash(
    previous_hash, NEW.chain_seq, NEW.id, NEW.admin_id, NEW.admin_email, NEW.action,
    NEW.target_type, NEW.target_id, NEW.detail, NEW.ip_hash, NEW.created_at
  );
  UPDATE sesame_admin_audit_chain_head SET head_seq = NEW.chain_seq, head_hash = NEW.hash WHERE singleton;
  RETURN NEW;
END $$ LANGUAGE plpgsql;

CREATE TRIGGER sesame_admin_audit_chain
BEFORE INSERT ON sesame_admin_audit_log
FOR EACH ROW EXECUTE FUNCTION sesame_chain_admin_audit_row();

CREATE OR REPLACE FUNCTION sesame_reject_admin_audit_truncate() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'sesame_admin_audit_log cannot be truncated';
END $$ LANGUAGE plpgsql;

CREATE TRIGGER sesame_admin_audit_chain_truncate
BEFORE TRUNCATE ON sesame_admin_audit_log
FOR EACH STATEMENT EXECUTE FUNCTION sesame_reject_admin_audit_truncate();

CREATE TABLE IF NOT EXISTS sesame_admin_audit_checkpoints (
  id BIGSERIAL PRIMARY KEY,
  cover_seq BIGINT NOT NULL UNIQUE,
  chain_hash BYTEA NOT NULL,
  key_id TEXT NOT NULL,
  signed_at TIMESTAMPTZ NOT NULL,
  signature BYTEA NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT sesame_admin_audit_checkpoints_chain_hash_length CHECK (octet_length(chain_hash) = 32),
  CONSTRAINT sesame_admin_audit_checkpoints_signature_length CHECK (octet_length(signature) = 64)
);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sesame_app') THEN
    REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON TABLE sesame_admin_audit_chain_head FROM sesame_app;
    REVOKE UPDATE, DELETE ON TABLE sesame_admin_audit_checkpoints FROM sesame_app;
  END IF;
END $$;
