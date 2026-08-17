# Sesame Go API

This service is vault-blind by design. It publishes public product plans, product status, release availability, support status, and the server boundary. Its separate website account has four jobs: beta eligibility, signed-download access, licence metadata, and website/desktop device management. Payment processing and offline licence issuance are not implemented.

It must never accept vault passwords, TOTP seeds, recovery details, import files, encrypted vaults, or encryption keys. Website-account passwords are an explicit exception: they are Argon2id-hashed before storage and are not connected to a local vault.

Metadata endpoints are read-only and reject every request body without parsing or logging it. Account endpoints use small, route-specific JSON schemas, store Argon2id password hashes and hashed opaque tokens, and reject unknown or vault-shaped fields. There is no vault endpoint.

The desktop-link flow is separate: a signed-in website account creates a one-time code that expires after ten minutes. The installed Windows app redeems it directly with the API and receives its own opaque device token. The token is stored locally under the current Windows profile. This links only the account service; it does not upload, identify, or unlock a vault, and it does not enable Sync.

## Self-hosting

One flow, from a fresh clone, with no Sesame-owned key, domain, or account:

```bash
npm install
npm run setup
npm run compose:up
```

`npm run setup` writes this deployment's own database password, capability
signing key, admin encryption key, and admin IP pepper into the gitignored
`deploy/compose/.env`, and resolves the build contexts for the API and both
portals. Re-running keeps every existing value, because rotating the admin
encryption key locks out administrators whose MFA secret was written under the
old one.

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

Registration defaults to `invite`. Once an admin store exists, which this
deployment always has, the `registration_mode` feature flag in the database is
what the API reads: `SESAME_REGISTRATION_MODE` applies only to a deployment
running without administration. Change it from the administration portal, or
issue invitations from there.

The public marketing site is not part of this deployment and is not required
by it. If you run one, set `SESAME_PUBLIC_SITE_ORIGIN` to its origin: it may
then read published metadata anonymously, with no credentials and no unsafe
method.

Everything above runs over HTTP on loopback. Behind TLS, set
`SESAME_SESSION_SECURE` and `SESAME_ADMIN_SESSION_SECURE` to `true` and give
each origin its real HTTPS address.

## Run locally in the monorepo

The API and website are separate processes. Run both from the repository root.

```powershell
npm run api:up
```

This starts PostgreSQL and the API on `http://localhost:8787`, generating the local secrets in the gitignored root `.env` first if they are missing. It is the only supported way to start the stack, because a Compose command that skips that step can leave `SESAME_ADMIN_ENCRYPTION_KEY` out of step with the database. The Compose credentials are local-development-only. Do not also run `npm run backend:dev` while the Compose API is using port 8787.

For native API development, run:

```powershell
npm run backend:dev
```

When `DATABASE_URL` is unset, this command stops the containerized API, starts
the PostgreSQL service from `compose.yaml`, waits for it to become healthy, and
supplies the explicit development-only connection string to the Go process.
The database is exposed on `127.0.0.1:5432`, never on the network interface.
Production still fails closed when `DATABASE_URL` is missing. Set
`DATABASE_URL` yourself to use an existing PostgreSQL instance; in that case
Docker is not touched.

The default address is `127.0.0.1:8787`. Compose also starts a local Mailpit
inbox at `http://localhost:8025`, so verification and recovery mail can be
tested without an external provider. It is a development-only capture service.

Configuration:

- `SESAME_API_ADDR`
- `SESAME_API_VERSION`
- `SESAME_WEB_ORIGIN`
- `DATABASE_URL`
- `SESAME_SESSION_SECURE` (`false` for local HTTP only)
- `SESAME_SESSION_DOMAIN` (optional cookie domain)
- `SESAME_REGISTRATION_MODE` (`closed`, `invite`, or `public`; defaults to `invite`)
- `SESAME_SMTP_ADDR` (`host:port`; when set, STARTTLS is mandatory)
- `SESAME_SMTP_USERNAME`
- `SESAME_SMTP_PASSWORD`
- `SESAME_SMTP_FROM`

`SESAME_SMTP_ALLOW_INSECURE_LOCAL=true` is permitted only with
`SESAME_ENV=development`, has no SMTP credentials, and is used only for the
isolated local Mailpit container. Every other SMTP configuration requires
STARTTLS.
- `SESAME_RP_ID` and `SESAME_RP_NAME` (website passkeys)

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

The root Compose stack runs the `migrate` job after PostgreSQL is healthy and
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

The `backend/` subtree owns its Node and Go command wrappers, lockfile, build,
vet, race-test, vulnerability, generated OpenAPI, and future CI entry points.
`npm run openapi:generate` regenerates `openapi/openapi.json` from the Go mux
registrations; `npm run openapi:check` fails if that checked-in inventory is
stale or incomplete. Copying the exact
subtree into an empty directory does not require a desktop, website, admin, or
extension checkout. PostgreSQL integration cases run when
`SESAME_TEST_DATABASE_URL` is set and otherwise skip; the future CI workflow
supplies a fictional test database. The root Compose flow remains the only
supported local stack entry point because it generates local secrets before
starting services.

Card processing remains deliberately outside Sesame. A regulated provider must
handle card data; Sesame records only its internal entitlement and receipt
references after a verified provider event.

## Account boundary and API

The website account exists for four jobs only: beta eligibility, access to
signed downloads, licence metadata, and browser/desktop device management. It
does not identify, receive, sync, or unlock a local vault.

See [openapi/openapi.json](./openapi/openapi.json) for the generated endpoint,
method, authentication/CSRF, availability, and handler-ownership inventory.
See [API.md](./API.md) for the detailed closed request and response schemas,
recent-authentication rules, email-token lifetimes, desktop-link states,
release metadata, and support intake restrictions. Account password changes
and password-recovery completion update the password and revoke/replace
browser sessions inside one PostgreSQL transaction.
