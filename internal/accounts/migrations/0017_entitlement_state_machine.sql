-- 0017_entitlement_state_machine: internal entitlement lifecycle.
--
-- Sesame stores entitlement decisions and provider receipt references, never
-- card numbers or payment credentials. State changes are constrained in the
-- database so every API replica enforces the same lifecycle.

ALTER TABLE sesame_licences
  DROP CONSTRAINT IF EXISTS sesame_licences_status_check;

ALTER TABLE sesame_licences
  ADD COLUMN IF NOT EXISTS grace_ends_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS receipt_reference TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE sesame_licences
  ADD CONSTRAINT sesame_licences_status_check
  CHECK (status IN ('pending', 'active', 'grace_period', 'expired', 'revoked'));

CREATE TABLE IF NOT EXISTS sesame_entitlement_events (
  id BIGSERIAL PRIMARY KEY,
  licence_id TEXT NOT NULL REFERENCES sesame_licences(id) ON DELETE CASCADE,
  account_id TEXT NOT NULL REFERENCES sesame_accounts(id) ON DELETE CASCADE,
  from_status TEXT,
  to_status TEXT NOT NULL CHECK (to_status IN ('pending', 'active', 'grace_period', 'expired', 'revoked')),
  reason TEXT NOT NULL DEFAULT '',
  provider_event_reference TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sesame_entitlement_events_licence_idx ON sesame_entitlement_events(licence_id, created_at DESC);
CREATE INDEX IF NOT EXISTS sesame_entitlement_events_account_idx ON sesame_entitlement_events(account_id, created_at DESC);

CREATE OR REPLACE FUNCTION sesame_validate_licence_transition()
RETURNS TRIGGER AS $$
BEGIN
  IF NEW.status = 'grace_period' AND NEW.grace_ends_at IS NULL THEN
    RAISE EXCEPTION 'grace_period requires grace_ends_at';
  END IF;
  IF NEW.status <> 'grace_period' THEN
    NEW.grace_ends_at := NULL;
  END IF;
  IF TG_OP = 'UPDATE' AND NEW.status <> OLD.status AND NOT (
    (OLD.status = 'pending' AND NEW.status IN ('active', 'revoked')) OR
    (OLD.status = 'active' AND NEW.status IN ('grace_period', 'expired', 'revoked')) OR
    (OLD.status = 'grace_period' AND NEW.status IN ('active', 'expired', 'revoked'))
  ) THEN
    RAISE EXCEPTION 'invalid entitlement transition: % to %', OLD.status, NEW.status;
  END IF;
  NEW.updated_at := NOW();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS sesame_licence_transition_guard ON sesame_licences;
CREATE TRIGGER sesame_licence_transition_guard
BEFORE INSERT OR UPDATE ON sesame_licences
FOR EACH ROW EXECUTE FUNCTION sesame_validate_licence_transition();

CREATE OR REPLACE FUNCTION sesame_record_entitlement_transition()
RETURNS TRIGGER AS $$
BEGIN
  IF TG_OP = 'INSERT' OR NEW.status <> OLD.status THEN
    INSERT INTO sesame_entitlement_events (licence_id, account_id, from_status, to_status, provider_event_reference)
    VALUES (NEW.id, NEW.account_id, CASE WHEN TG_OP = 'INSERT' THEN NULL ELSE OLD.status END, NEW.status, NEW.receipt_reference);
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS sesame_licence_transition_audit ON sesame_licences;
CREATE TRIGGER sesame_licence_transition_audit
AFTER INSERT OR UPDATE ON sesame_licences
FOR EACH ROW EXECUTE FUNCTION sesame_record_entitlement_transition();
