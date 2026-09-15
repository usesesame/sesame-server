# Deploying Sesame

This guide runs Sesame as the operator of a public service, on one Debian
host, for the four origins the product expects. A self-hoster follows the
same steps with their own domain.

## What runs where

| Origin | What serves it | Source |
| --- | --- | --- |
| `usesesame.app` | Caddy, static files | `sesame-website`, built separately |
| `api.usesesame.app` | Go API container on `127.0.0.1:8787` | this repository |
| `account.usesesame.app` | nginx container on `127.0.0.1:4175` | `web/account` |
| `admin.usesesame.app` | nginx container on `127.0.0.1:4174` | `web/admin` |

Keep the four origins separate. They are a security boundary. The API accepts
credentialed browser traffic only from the exact account origin, and it
accepts `/v1/admin/*` only from the exact admin origin. The marketing site
holds no session at all, which is what lets its policy stay
`connect-src 'self'` plus the API. Serving everything from one host removes
the separation the API is written to enforce.

Only the reverse proxy listens on a public interface. Every container binds
to loopback.

## Before you start

- A Debian host with Docker Engine, the Compose plugin, and Caddy.
- DNS A and AAAA records for `usesesame.app`, `www`, `api`, `account`, and
  `admin`, all pointing at the host. Caddy cannot issue certificates until
  these resolve.
- An SMTP account that supports STARTTLS. Without working mail there is no
  email verification, no password recovery, and no email change.
- Node.js 24.20 to build the website.

## 1. Configure

```bash
git clone https://github.com/usesesame/sesame-server.git
cd sesame-server
npm ci
npm run setup
```

`npm run setup` generates this deployment's own secrets and resolves the
build contexts. It writes `deploy/compose/.env`, the development file.

```bash
cp deploy/compose/.env.production.example deploy/compose/.env.production
chmod 600 deploy/compose/.env.production
```

Fill in `.env.production`. Copy the four generated secrets from
`deploy/compose/.env`. Copy the API, account, and admin digest references
from `server-release.json` into the three image fields. Get
`server-release.json` from the protected server release workflow for the
version being deployed. Production Compose has no build contexts and does
not rebuild source on the host.

Three values are fixed by the product. The comments in the example explain
them as well.

- `SESAME_ADMIN_ENCRYPTION_KEY` encrypts every administrator's MFA secret.
  If it changes, those secrets become unreadable and sign-in fails with the
  same message a wrong password gets. Back it up somewhere that survives
  this disk.
- `SESAME_RP_ID` must be the account portal's registrable domain. Changing
  it later invalidates every passkey already registered.
- `SESAME_TRUSTED_PROXIES` must name the proxy's network and nothing wider.
  Every request arrives through the proxy, so without it the rate limiter
  and the admin audit log see a single client address for the entire
  internet. Widening it to `0.0.0.0/0` lets any client forge its own
  address and defeat rate limiting. Verify the network with
  `docker network inspect sesame-prod_default`.

## 2. Start the stack

```bash
docker compose -f deploy/compose/compose.prod.yaml \
  --env-file deploy/compose/.env.production pull
docker compose -f deploy/compose/compose.prod.yaml \
  --env-file deploy/compose/.env.production up -d
```

`compose.prod.yaml` is separate from `compose.yaml` on purpose. The
development file turns cookie security off, sets `SESAME_ENV=development`,
and sends mail to a local catcher. Do not run it on a public host.

Migrations run once, as their own service, before the API starts. The API
refuses to start if a required value is missing or if an HTTPS origin is
paired with insecure cookies. A misconfiguration fails closed instead of
serving insecurely.

Check it:

```bash
docker compose -f deploy/compose/compose.prod.yaml \
  --env-file deploy/compose/.env.production ps
curl -fsS http://127.0.0.1:8787/livez
```

`/livez` reports the version and full source commit baked into the released
API image. The account and admin images expose the same identity in
`/release.json`.

## 3. Build and place the website

The marketing site is a separate repository and is deliberately not part of
the server stack. A deployment of the API must not depend on it.

```bash
git clone https://github.com/usesesame/sesame-website.git
cd sesame-website
npm ci
VITE_SESAME_SITE_ORIGIN=https://usesesame.app \
VITE_SESAME_API_URL=https://api.usesesame.app \
VITE_SESAME_ACCOUNT_URL=https://account.usesesame.app \
VITE_SESAME_PRIVACY_EMAIL=privacy@usesesame.app \
npm run build
sudo rsync -a --delete dist/ /srv/usesesame.app/
```

No production origin is compiled into the site, so these must be supplied
on every build. A wrong `VITE_SESAME_SITE_ORIGIN` ships silently as an SEO
defect, which is why an absent one fails the build.

## 4. Put the proxy in front

```bash
sudo cp deploy/caddy/Caddyfile.example /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

Caddy issues and renews certificates for all four names on its own.

Keep the website's headers in the Caddyfile in step with `public/_headers`
in the website repository, including the inline script hash in its CSP.
Both portals set their own headers from the policy compiled into their
image, so the proxy must not add a second Content-Security-Policy for them.

## 5. Create the first administrator

```bash
docker compose -f deploy/compose/compose.prod.yaml \
  --env-file deploy/compose/.env.production \
  run --rm --entrypoint /sesame-adminctl api bootstrap you@usesesame.app
```

This prints a one-time setup link that expires after one hour. Opening it
consumes it: the enrollment secret is shown once, and the administrator has
30 minutes from that first open to set a password and TOTP. After that the
link is spent and `adminctl reset` issues a fresh one. There is no public
administrator registration, and no session is issued until TOTP is
configured.

Every administrator mutation writes its audit row in the same database
transaction as the change.

## Updating to a new release

A semantic version tag starts the protected `server-release` workflow. The
workflow runs the server, portal, migration, compose, and container smoke
gates before it pushes any image. It publishes three version tags to GHCR,
records their digest references in `server-release.json`, and attaches a
dependency SBOM and signed provenance to each digest. Production uses the
digest references, never the version tags.

The GitHub `server-release` environment must require release approval and
define `SESAME_API_ORIGIN`, `SESAME_PUBLIC_SITE_ORIGIN`, and
`SESAME_CAPABILITY_PUBLIC_KEY`. The key is the public half of the
production capability signing key. GitHub supplies registry and
attestation credentials only to the workflow. Host and deployment
credentials never enter the release job.

Deploy one release artifact set end to end. Run this on the host, from a
checkout at or newer than the revision that built the images:

```bash
npm ci
npm run deploy:release -- plan server-release.json     # what would happen, nothing changes
npm run deploy:release -- deploy server-release.json
npm run deploy:release -- status
npm run deploy:release -- rollback [version]
```

The deploy tool runs these stages, and every stage is safe to retry after
an interruption:

1. Pull and verify all three images by digest and identity labels.
2. Take a verified `pg_dump` backup.
3. Restore that backup into a scratch database and run the candidate
   migration and the previous revision's API against it. This is the
   rehearsal.
4. Apply the migration to the production database.
5. Start the candidate API beside the live stack on a port Caddy does not
   route, and probe `/livez` and `/readyz`.
6. Rename the staged env file over `.env.production` and bring the
   production stack up with a bounded readiness wait.
7. Probe the live endpoints and both portals, then record the deployment.

If a stage fails, the failure is contained and recorded:

- A failed health check before the switch changes no traffic. The previous
  revision keeps serving, and the env file is untouched.
- A failed health check after the switch rolls back automatically. The tool
  restores the recorded previous env snapshot, restarts the stack, probes
  it, and records the rollback. The interrupted attempt is recorded, and
  rerunning `deploy` with the same `server-release.json` converges on that
  revision instead of duplicating it.
- Deploying an older release than the recorded one is refused. Re-presenting
  the same release with changed bytes is refused. Artifact identity is
  immutable.
- The rehearsal proves the previous revision still runs against the
  migrated schema, so an ordinary rollback after migration is safe.
  Rollback restores images and configuration only. It never reverts
  migrations. If a release ships an incompatible database contraction,
  recovery means restoring the recorded backup,
  `deploy/state/backups/sesame-<version>-<timestamp>.sql.gz`, onto the
  previous revision by hand. That is a deliberate operator procedure.

State, env snapshots, and backups live under `deploy/state/`, which is
gitignored: `deployed.json`, `pending.json`, `backups/`, and
`history/<version>/`. Never delete `history/` while an older version is
still a wanted rollback target. Secrets from `.env.production` are read by
the tool, mode 0600, never printed, and copied into the per-version
snapshots under `deploy/state/history/`. Back that directory up with the
same care as the env file itself.

## Backups

Two things matter, and they fail differently.

```bash
docker compose -f deploy/compose/compose.prod.yaml \
  --env-file deploy/compose/.env.production \
  exec -T db pg_dump -U sesame sesame | gzip > sesame-$(date +%F).sql.gz
```

A lost database loses accounts. A lost `SESAME_ADMIN_ENCRYPTION_KEY` locks
every administrator out while the database stays perfectly intact, which is
the harder failure to recover from. Back up `deploy/compose/.env.production`
separately, somewhere other than this host.

## Operating notes

- **Registration** defaults to `invite`. With the administration portal
  running, the `registration_mode` feature flag in the database wins over
  the environment variable.
- **Sync stays disabled.** The `cloud_sync_available` flag exists, but the
  protocol has not passed its security review. A green deployment is not
  that review.
- **Public downloads stay off** until the Windows release gate is met: a
  versioned artifact built from recorded source, a valid Tauri updater
  signature, a recorded SHA-256, clean-profile verification, and
  Authenticode. Candidates without verified Authenticode evidence are
  lab-only and must not reach any download channel.
- **Artifact delivery is a separate gate from Authenticode.** Authenticode
  signs the executable. Delivering it needs `SESAME_ARTIFACT_GATEWAY_URL`
  and `SESAME_ARTIFACT_GATEWAY_SIGNING_KEY` set together, plus
  `SESAME_DESKTOP_UPDATE_BASE_URL` for the Tauri updater format. The
  gateway must serve release files at their object key and reject requests
  without a valid `expires` and `signature` pair. Public buckets are not
  supported. While those values are unset the stack runs, but desktop
  download and update delivery is off and the System page reports artifact
  delivery as not configured.
- **Trusted proxy ranges** are a reviewed configuration change, since they
  decide how a client address is trusted before authentication. The
  dashboard shows the active count and cannot edit it at runtime.
- The API never accepts a vault. That boundary needs its own threat model
  and is outside anything here.

## Before opening registration to the public

The privacy policy and terms currently describe an invite-only beta and
defer the operator's legal identity. Before public registration is enabled,
replace that wording with the legal operator's name and postal contact,
name the hosting and other processors the deployment actually uses, state
retention periods and transfer safeguards, and complete the governing-law
and consumer information sections. Taking payment for Sync makes this a
legal requirement rather than a tidy-up. These details must match the real
business. Do not invent them in copy.
