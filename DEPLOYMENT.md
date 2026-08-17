# Website and API deployment

## Public website

The website is a static Vite build.

```powershell
npm.cmd run website:check
# Set these deployment-owned values before every website build.
$env:VITE_SESAME_SITE_ORIGIN = 'https://your-public-site.example'
$env:VITE_SESAME_API_URL = 'https://your-account-api.example'
$env:VITE_SESAME_PRIVACY_EMAIL = 'privacy@your-public-site.example'
npm.cmd run website:build
```

Publish `website/dist` to Cloudflare Pages, Netlify, Vercel static hosting, or an equivalent immutable host. On hosts that support the `_headers` format, the included file sets a restrictive CSP and other browser security headers.

## Legal launch gate

The current site is an invite-only beta. Before public registration is enabled,
replace the beta-only controller wording in the public policies with the legal
operator's name and postal contact, name the deployed hosting and other
processors, state the applicable retention periods and transfer safeguards, and
complete the governing-law and consumer-information sections. Do not invent
these details in copy: they must match the deployed business and service
providers.

Do not add a vault credential form, web vault, import upload, vault analytics, or third-party session replay. The separate website-account form may collect only an email address and a website-account password. Public download links stay absent until verified updater artifacts and hashes pass the Windows release gate. Until `REL-002` supplies Authenticode signatures for the installer and every shipped binary, do not distribute even an account-gated Windows beta: unsigned installers are lab evidence only.

## Go API

The API uses the Go standard library and can run as a small container or native service. Production requires this configuration:

- `SESAME_API_ADDR`, normally `0.0.0.0:8787` inside a container.
- `SESAME_API_VERSION`, set to the immutable deployed build version.
- `SESAME_WEB_ORIGIN`, exactly `https://usesesame.app` in production.
- `DATABASE_URL`, a restricted PostgreSQL connection string.
- `SESAME_SESSION_SECURE=true` in production. Set it to `false` only for local HTTP development.
- `SESAME_ADMIN_ORIGIN`, exactly `https://admin.usesesame.app` in production.
- `SESAME_ADMIN_SESSION_SECURE=true` in production.
- `SESAME_ADMIN_ENCRYPTION_KEY`, a secret 32-byte key encoded as hex or Base64. This encrypts admin TOTP secrets and must be backed up in the production secret manager.
- `SESAME_ADMIN_IP_PEPPER`, an independent high-entropy secret used only to pseudonymise IP addresses in the admin audit log.

The API deliberately has no default website or admin origin. Set both origins
explicitly in the deployment environment. If SMTP is enabled, set
`SESAME_SMTP_FROM` explicitly as well.

### Losing the admin encryption key

`SESAME_ADMIN_ENCRYPTION_KEY` encrypts every administrator's MFA secret. If the
key changes, those secrets can no longer be decrypted and sign-in fails with
"Email, password, or MFA code is incorrect", the same answer a wrong password
gets. The password stays intact; the MFA secret becomes unreadable.

Locally the key lives in the gitignored root `.env`, written once by
`npm run api:up`. Deleting that file, cleaning the working tree, or cloning
fresh generates a new key, while the database keeps the accounts created under
the old one, so `npm run api:up` now says so when it writes a new key.

The API reports the condition at startup:

```
WARN Sesame admin accounts cannot be read with the configured key accounts=2
```

Restore the original key if you still have it. Otherwise issue a new setup link
per account, which rotates the MFA secret and clears the password:

```bash
npm run backend:admin:bootstrap -- reset admin@example.com
```

For local development, run the complete service with PostgreSQL:

```powershell
docker compose up --build
```

Run tests before building:

```powershell
go -C backend test ./...
docker build -t sesame-api:0.1.0 backend
```

Terminate TLS at the deployment platform or a maintained reverse proxy. Expose the API as `https://api.usesesame.app`, apply request-rate and body-size limits at the edge, and keep access logs free of sensitive query values. Use unique production database credentials and a managed secret store; the Compose password is development-only by design.

## Administration app

The administration frontend is a separate static Vite build with a separate origin and cookie boundary:

```powershell
npm.cmd run admin:check
# Reuse the explicitly configured VITE_SESAME_API_URL.
npm.cmd run admin:build
```

Publish `admin/dist` to `admin.usesesame.app`. Do not merge it into the public website build. Preserve the included no-index, no-store, frame-denial and restrictive CSP headers. The Go API accepts `/v1/admin/*` browser traffic only from the exact `SESAME_ADMIN_ORIGIN`.

## Desktop account service

Set `SESAME_API_BASE_URL` when building the desktop application. It must be the
absolute HTTPS API origin (loopback HTTP is permitted only for development).
Without this build-time configuration the desktop fails closed, so a token is
never sent to a compiled-in or ambient endpoint.

For local desktop development only, copy `src-tauri/.env.example` to the
gitignored `src-tauri/.env.local`. Debug builds read that file; release builds
ignore it and require `SESAME_API_BASE_URL` from the build environment.

For local website development, the public pages can render without a config
file. To exercise account APIs against Compose, copy `website/.env.example` to
the gitignored `website/.env.local` first.

For the local admin UI, copy `admin/.env.example` to the gitignored
`admin/.env.local` before running `npm run admin:dev`.

For the desktop app, copy `.env.example` to the gitignored `.env.local` and
set `VITE_SESAME_SITE_ORIGIN` to the public website origin. The desktop uses
this value only to open the support portal; it has no compiled production
domain fallback.

Generate an Ed25519 capability signing seed outside the repository and set its
base64url form as `SESAME_CAPABILITY_SIGNING_KEY` for the API. Build the
website with the corresponding `VITE_SESAME_CAPABILITY_PUBLIC_KEY` and the
desktop with `SESAME_CAPABILITY_PUBLIC_KEY`. When the signed configuration
cannot be verified, clients disable sensitive account capabilities.

After the database migration and API deployment, bootstrap the first super administrator from a trusted operator shell:

```powershell
$env:DATABASE_URL = '<production database URL>'
$env:SESAME_ADMIN_ENCRYPTION_KEY = '<secret 32-byte key>'
$env:SESAME_ADMIN_ORIGIN = 'https://admin.usesesame.app'
go -C backend run ./cmd/adminctl bootstrap admin@example.com
```

The command prints a one-time setup link that expires after one hour. Transfer it through an approved secure channel. Opening the link consumes it: the enrollment secret is returned once, and the administrator must finish setting their password and MFA within 30 minutes of that first open, after which the link is spent and `adminctl reset` issues a fresh one. There is no public administrator registration. Every administrator must configure TOTP before a session is issued, and all mutations write their audit row in the same database transaction as the change.

Changing trusted proxy ranges remains a reviewed deployment configuration change, since it affects how client addresses are trusted before authentication. The dashboard shows the active count but cannot edit it at runtime.

## Release metadata

The public release endpoint must continue returning `available: false` until all of the following exist:

1. A versioned Windows artifact built from recorded source.
2. A valid Tauri updater signature over the exact updater artifact.
3. A recorded SHA-256 hash.
4. Clean-profile beta verification and rollback evidence.

Authenticode is a blocker for every Windows distribution. Candidate records
without verified Authenticode evidence are lab-only and must not be made
available through account pages, a beta ring, or any download channel. Do not
publish a private key or ask users to lower Windows security settings.

Website and API deployment never authorizes uploading a user vault. That boundary requires a separate sync threat model and is explicitly outside these services.
