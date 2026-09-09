CREATE TABLE sesame_extension_publications (
  id TEXT PRIMARY KEY,
  store TEXT NOT NULL,
  version TEXT NOT NULL,
  package_sha256 TEXT NOT NULL,
  package_bytes BIGINT NOT NULL,
  filename TEXT NOT NULL,
  evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
  status TEXT NOT NULL DEFAULT 'built',
  state_revision BIGINT NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT sesame_extension_publications_store_check
    CHECK (store IN ('chrome', 'edge', 'firefox')),
  CONSTRAINT sesame_extension_publications_version_check
    CHECK (version ~ '^[0-9A-Za-z.+-]{1,40}$'),
  CONSTRAINT sesame_extension_publications_sha256_check
    CHECK (package_sha256 ~ '^[0-9a-f]{64}$'),
  CONSTRAINT sesame_extension_publications_bytes_check
    CHECK (package_bytes BETWEEN 1 AND 68574592),
  CONSTRAINT sesame_extension_publications_filename_check
    CHECK (filename <> '' AND LENGTH(filename) <= 200 AND position('/' in filename) = 0 AND position('\' in filename) = 0),
  CONSTRAINT sesame_extension_publications_status_check
    CHECK (status IN ('built', 'uploaded', 'submitted', 'approved', 'published', 'withdrawn')),
  CONSTRAINT sesame_extension_publications_state_revision_check
    CHECK (state_revision >= 1),
  CONSTRAINT sesame_extension_publications_identity_key
    UNIQUE (store, version, package_sha256)
);

CREATE OR REPLACE FUNCTION sesame_extension_publication_identity_is_immutable()
RETURNS TRIGGER AS $$
BEGIN
  IF NEW.store IS DISTINCT FROM OLD.store
    OR NEW.version IS DISTINCT FROM OLD.version
    OR NEW.package_sha256 IS DISTINCT FROM OLD.package_sha256
    OR NEW.package_bytes IS DISTINCT FROM OLD.package_bytes
    OR NEW.filename IS DISTINCT FROM OLD.filename
    OR NEW.created_at IS DISTINCT FROM OLD.created_at
  THEN
    RAISE EXCEPTION 'extension publication identity is immutable';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS sesame_extension_publication_identity_no_update ON sesame_extension_publications;
CREATE TRIGGER sesame_extension_publication_identity_no_update
BEFORE UPDATE ON sesame_extension_publications
FOR EACH ROW
EXECUTE FUNCTION sesame_extension_publication_identity_is_immutable();
