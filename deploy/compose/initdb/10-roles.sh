#!/bin/sh
set -eu

: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_DB:?POSTGRES_DB is required}"
: "${SESAME_DATABASE_OWNER_PASSWORD:?SESAME_DATABASE_OWNER_PASSWORD is required}"
: "${SESAME_DATABASE_APP_PASSWORD:?SESAME_DATABASE_APP_PASSWORD is required}"
: "${SESAME_DATABASE_BACKUP_PASSWORD:?SESAME_DATABASE_BACKUP_PASSWORD is required}"

psql -X --set ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<'SQL'
\getenv owner_password SESAME_DATABASE_OWNER_PASSWORD
\getenv app_password SESAME_DATABASE_APP_PASSWORD
\getenv backup_password SESAME_DATABASE_BACKUP_PASSWORD

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sesame_owner') THEN
    EXECUTE 'CREATE ROLE sesame_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sesame_app') THEN
    EXECUTE 'CREATE ROLE sesame_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sesame_backup') THEN
    EXECUTE 'CREATE ROLE sesame_backup LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS';
  END IF;
END
$$;

ALTER ROLE sesame_owner WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'owner_password';
ALTER ROLE sesame_app WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'app_password';
ALTER ROLE sesame_backup WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'backup_password';

GRANT CREATE, USAGE ON SCHEMA public TO sesame_owner;
GRANT pg_read_all_data TO sesame_backup;

DO $$
DECLARE
  relation RECORD;
  routine RECORD;
BEGIN
  FOR relation IN
    SELECT c.oid::regclass AS name, c.relkind
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public'
      AND pg_get_userbyid(c.relowner) = CURRENT_USER
      AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
  LOOP
    IF relation.relkind = 'm' THEN
      EXECUTE format('ALTER MATERIALIZED VIEW %s OWNER TO sesame_owner', relation.name);
    ELSIF relation.relkind = 'v' THEN
      EXECUTE format('ALTER VIEW %s OWNER TO sesame_owner', relation.name);
    ELSIF relation.relkind = 'f' THEN
      EXECUTE format('ALTER FOREIGN TABLE %s OWNER TO sesame_owner', relation.name);
    ELSE
      EXECUTE format('ALTER TABLE %s OWNER TO sesame_owner', relation.name);
    END IF;
  END LOOP;
  FOR routine IN
    SELECT p.oid::regprocedure AS name
    FROM pg_proc p
    JOIN pg_namespace n ON n.oid = p.pronamespace
    WHERE n.nspname = 'public'
      AND p.prokind IN ('f', 'p')
      AND pg_get_userbyid(p.proowner) = CURRENT_USER
  LOOP
    EXECUTE format('ALTER FUNCTION %s OWNER TO sesame_owner', routine.name);
  END LOOP;
END
$$;
SQL
