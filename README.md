# Sesame Go API

This is a vault-blind account service. It publishes product, release, and
support metadata. It also provides website accounts, beta eligibility,
download metadata, desktop linking, and support requests. It does not process
payments or issue offline licences.

The service never accepts a vault password, TOTP seed, recovery detail, import
file, encrypted vault, or encryption key. Website-account passwords are the
only exception. They are Argon2id-hashed and are not connected to a vault.

Metadata endpoints are read-only. They reject request bodies without parsing
or logging them. Account endpoints use small, route-specific JSON schemas and
reject unknown or vault-shaped fields. There is no vault endpoint.

A signed-in account can create a one-time desktop-link code. It expires after
ten minutes. The installed Windows app redeems it directly with the API and
receives an opaque device token. The token links the account service only. It
does not upload, identify, or unlock a vault, and it does not enable Sync.

## Self-hosting

From a fresh clone:

```bash
npm ci
npm run setup
npm run compose:up
```

`npm run setup` writes local secrets to the gitignored
`deploy/compose/.env`. It also resolves the build contexts for the API and both
portals. It keeps existing values on later runs. Rotating the admin encryption
key makes existing MFA secrets unreadable.

`npm run compose:up` starts PostgreSQL, applies migrations, and runs the API,
the account portal, the administration portal, and a local mail catcher:

| Service | Address |
| --- | --- |
| Account portal | `http://localhost:4175` |
| Administration portal | `http://localhost:4174` |
| API | `http://localhost:8787` |
| Mail catcher | `http://localhost:8025` |

Then create the first administrator, which prints a one-time setup link:

```bash
npm run admin:bootstrap -- bootstrap you@example.com
```

Registration defaults to `invite`. Deployments with administration use the
database `registration_mode` flag. `SESAME_REGISTRATION_MODE` applies only
without administration. Change the flag or issue invitations in the portal.

The public marketing site is not part of this deployment and is not required
by it. If you run one, set `SESAME_PUBLIC_SITE_ORIGIN` to its origin: it may
then read published metadata anonymously, with no credentials and no unsafe
method.

Everything above runs over HTTP on loopback. Behind TLS, set
`SESAME_SESSION_SECURE` and `SESAME_ADMIN_SESSION_SECURE` to `true` and give
each origin its real HTTPS address.

## Local configuration

Use the self-hosting flow above for local development too. `npm run setup`
must run before Compose because it creates deployment-specific secrets and
build contexts in `deploy/compose/.env`. That file is for local development
only. The complete API configuration template is [.env.example](.env.example),
and the production Compose values are documented in
[DEPLOYMENT.md](DEPLOYMENT.md).

The default API address is `127.0.0.1:8787`. Compose also starts a local
Mailpit inbox at `http://localhost:8025`, so verification and recovery mail can
be tested without an external provider. It is a development-only capture
service.

`SESAME_SMTP_ALLOW_INSECURE_LOCAL=true` is accepted only with
`SESAME_ENV=development` and no SMTP credentials. Every other SMTP
configuration requires STARTTLS.

`invite` mode accepts either a row in `sesame_beta_eligibility` with status
`eligible`, or a live single-use value in `sesame_beta_invites`. Invite values
are SHA-256 hashed before storage. `closed` mode rejects registration even if a
client posts directly to the endpoint. Do not use `public` until registration
has a real public purpose and abuse controls have been reviewed.

Account-action email is disabled unless SMTP is configured. When configured,
outbound messages are written to a durable `sesame_email_outbox` table and
delivered by a background worker with bounded exponential backoff. The SMTP
transport requires STARTTLS and never logs action URLs. Registration remains
possible for an eligible user when mail is temporarily unavailable, but the
response reports `verificationQueued: false` and signed downloads remain
unavailable until the email is verified.

## Migrations and deployment

Migrations are applied by a separate deployment job, not by running API replicas.
Run the migration command before starting the API:

```powershell
go run ./cmd/migrate
```

The API process (`cmd/api`) opens the database without migrating and will exit
if the schema is not current. This prevents multiple replicas from racing to
apply the same migrations and keeps deployment rollback simple.

The Compose stack runs the `migrate` job after PostgreSQL is healthy and
before the API starts. Production deployments should run the same
`cmd/migrate` job once per release before starting API replicas.

## Health endpoints

- `GET /livez`: lightweight liveness probe.
- `GET /readyz`: readiness probe that pings the database; returns `503` when
  the database is unreachable.
- `GET /healthz` is a deprecated alias for `/readyz`.

## Retention and backups

Scheduled maintenance (hourly) purges:

- expired security records (verification, recovery, email-change, and desktop-link tokens),
- delivered email-outbox rows older than seven days,
- failed email-outbox rows older than seven days.

Encrypted PostgreSQL backups should be taken at least daily and retained with a
30-day active retention plus an annual archive. The database contains Argon2id
password hashes, SHA-256 token hashes, and encrypted admin TOTP secrets; it
never contains vault passwords, TOTP seeds, recovery notes, or encrypted vault
blobs.

## Test

```powershell
npm ci
npm run ci
```

This repository owns its Node and Go command wrappers, lockfile, build, vet,
race-test, vulnerability scan, generated OpenAPI inventory, and CI workflow.
`npm run openapi:generate` regenerates `openapi/openapi.json` from the Go mux
registrations; `npm run openapi:check` fails if that checked-in inventory is
stale or incomplete. A checkout does not require the desktop, public website,
or browser extension. PostgreSQL integration cases run when
`SESAME_TEST_DATABASE_URL` is set and otherwise skip; CI supplies a fictional
test database. The Compose flow remains the supported local stack entry point
because setup generates local secrets before starting services.

Card processing remains deliberately outside Sesame. A regulated provider must
handle card data; Sesame records only its internal entitlement and receipt
references after a verified provider event.

## Account boundary and API

The website account owns authentication, account and session management, beta
eligibility, signed-download and licence metadata, desktop linking, notification
preferences, and support history. It does not identify, receive, sync, or
unlock a local vault.

See [openapi/openapi.json](./openapi/openapi.json) for the generated endpoint,
method, authentication/CSRF, availability, and handler-ownership inventory.
See [API.md](./API.md) for the detailed closed request and response schemas,
recent-authentication rules, email-token lifetimes, desktop-link states,
release metadata, and support intake restrictions. Account password changes
and password-recovery completion update the password and revoke/replace
browser sessions inside one PostgreSQL transaction.
