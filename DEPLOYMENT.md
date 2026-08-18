# Deploying Sesame

This describes running Sesame as the operator of a public service, on one
Debian host, for the four origins the product expects. A self-hoster running
the same stack for themselves follows the same steps with their own domain.

## What runs where

| Origin | What serves it | Source |
| --- | --- | --- |
| `usesesame.app` | Caddy, static files | `sesame-website`, built separately |
| `api.usesesame.app` | Go API container on `127.0.0.1:8787` | this repository |
| `account.usesesame.app` | nginx container on `127.0.0.1:4175` | `web/account` |
| `admin.usesesame.app` | nginx container on `127.0.0.1:4174` | `web/admin` |

Four origins is a security boundary, not a preference. The API accepts
credentialed browser traffic only from the exact account origin and `/v1/admin/*`
only from the exact admin origin. The marketing site holds no session at all,
which is what lets its policy stay `connect-src 'self'` plus the API. Collapsing
these onto one host removes the separation the API is written to enforce.

Only the reverse proxy listens on a public interface. Every container binds to
loopback.

## Before you start

- A Debian host with Docker Engine, the Compose plugin, and Caddy.
- DNS A and AAAA records for `usesesame.app`, `www`, `api`, `account`, and
  `admin`, all pointing at the host. Caddy cannot issue certificates until
  these resolve.
- An SMTP account that supports STARTTLS. Without working mail there is no
  email verification, no password recovery, and no email change.
- Node.js 24.13 to build the website.

## 1. Configure

```bash
git clone https://github.com/usesesame/sesame-server.git
cd sesame-server
npm ci
npm run setup
```

`npm run setup` generates this deployment's own secrets and resolves the build
contexts. It writes `deploy/compose/.env`, which is the development file.

```bash
cp deploy/compose/.env.production.example deploy/compose/.env.production
chmod 600 deploy/compose/.env.production
```

Fill it in, copying the four generated secrets and `SESAME_CAPABILITY_PUBLIC_KEY`
across from `deploy/compose/.env`. Read the comments in the example: several
values are not free choices.

- `SESAME_ADMIN_ENCRYPTION_KEY` encrypts every administrator's MFA secret. If
  it changes, those secrets become unreadable and sign-in fails with the same
  message a wrong password gets. Back it up somewhere that survives this disk.
- `SESAME_RP_ID` must be the account portal's registrable domain. Changing it
  later invalidates every passkey already registered.
- `SESAME_TRUSTED_PROXIES` must name the proxy's network and nothing wider.
  Every request arrives from the proxy, so without it the rate limiter and the
  admin audit log see a single client address for the entire internet. Setting
  it to `0.0.0.0/0` is worse than leaving it unset, because then any client can
  forge its own address.

## 2. Start the stack

```bash
docker compose -f deploy/compose/compose.prod.yaml \
  --env-file deploy/compose/.env.production up -d --build
```

This is a separate file from `compose.yaml`, which is development only:
that one disables secure cookies, sets `SESAME_ENV=development`, and sends mail
to a local catcher. Do not run it on a public host.

Migrations run once, as their own service, before the API starts. The API
refuses to start if a required value is missing or if an HTTPS website origin
is paired with insecure cookies, so a misconfiguration fails closed rather than
serving insecurely.

Check it:

```bash
docker compose -f deploy/compose/compose.prod.yaml \
  --env-file deploy/compose/.env.production ps
curl -fsS http://127.0.0.1:8787/v1/product/status
```

## 3. Build and place the website

The marketing site is a separate repository and is deliberately not part of the
server stack. A deployment of the API must not depend on it.

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

No production origin is compiled into the site, so these must be supplied on
every build. A wrong `VITE_SESAME_SITE_ORIGIN` is an SEO defect that ships
silently, which is why an absent one fails the build.

## 4. Put the proxy in front

```bash
sudo cp deploy/caddy/Caddyfile.example /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

Caddy issues and renews certificates for all four names on its own.

The website's headers are written out in the Caddyfile and have to be kept in
step with `public/_headers` in the website repository, including the inline
script hash in its CSP. Both portals set their own headers from the policy
compiled into their image, so the proxy must not add a second
Content-Security-Policy for them.

## 5. Create the first administrator

```bash
docker compose -f deploy/compose/compose.prod.yaml \
  --env-file deploy/compose/.env.production \
  run --rm --entrypoint /sesame-adminctl api bootstrap you@usesesame.app
```

This prints a one-time setup link that expires after one hour. Opening it
consumes it: the enrollment secret is shown once, and the administrator has 30
minutes from that first open to finish setting a password and TOTP. After that
the link is spent and `adminctl reset` issues a fresh one. There is no public
administrator registration, and no session is issued until TOTP is configured.

Every administrator mutation writes its audit row in the same database
transaction as the change.

## Backups

Two things matter, and they fail differently.

```bash
docker compose -f deploy/compose/compose.prod.yaml \
  --env-file deploy/compose/.env.production \
  exec -T db pg_dump -U sesame sesame | gzip > sesame-$(date +%F).sql.gz
```

Back up `deploy/compose/.env.production` separately and somewhere else. A lost
database loses accounts. A lost admin encryption key locks out every
administrator while the database stays perfectly intact, which is the harder
failure to recover from.

## Operating notes

- **Registration** defaults to `invite`. With the administration portal running,
  the `registration_mode` feature flag in the database wins over the
  environment variable.
- **Sync stays disabled.** The `cloud_sync_available` flag exists, but the
  protocol has not passed its security review. A green deployment is not that
  review.
- **Public downloads stay off** until the Windows release gate is met: a
  versioned artifact built from recorded source, a valid Tauri updater
  signature, a recorded SHA-256, clean-profile verification, and Authenticode.
  Candidates without verified Authenticode evidence are lab-only and must not
  reach any download channel.
- **Trusted proxy ranges** are a reviewed configuration change, since they
  decide how a client address is trusted before authentication. The dashboard
  shows the active count but cannot edit it at runtime.
- The API never accepts a vault. That boundary needs its own threat model and
  is outside anything here.

## Before opening registration to the public

The privacy policy and terms currently describe an invite-only beta and defer
the operator's legal identity. Before public registration is enabled, replace
that wording with the legal operator's name and postal contact, name the
hosting and other processors the deployment actually uses, state retention
periods and transfer safeguards, and complete the governing-law and consumer
information sections. Taking payment for Sync makes this a legal requirement
rather than a tidy-up. These details must match the real business; do not
invent them in copy.
