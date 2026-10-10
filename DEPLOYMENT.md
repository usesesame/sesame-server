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
| `downloads.usesesame.app` | artifact gateway container on `127.0.0.1:8791` | this repository |

The first four rows are the origins the product expects. The downloads row
only serves release files the API has signed a short-lived URL for.

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
  these resolve. The Caddyfile example also has blocks for `downloads` and
  `mail`. Point those names at the host too, or delete the blocks you do not
  use.
- An SMTP relay that supports STARTTLS, at a provider or on this host.
  Without working mail there is no email verification, no password recovery,
  and no email change.
- Node.js 24.20 for the website build, the setup script, and the deploy tool.
- The GitHub CLI, authenticated to github.com with `gh auth login`, so the deploy
  tool can verify image provenance.
- The `age` command for encrypted database backups, installed with
  `apt install age`.

## 1. Configure

```bash
git clone https://github.com/usesesame/sesame-server.git
cd sesame-server
npm run setup
```

`npm run setup` generates this deployment's own secrets and resolves the
build contexts. It writes `deploy/compose/.env`, the development file. The
setup script and the deploy tool import only Node builtins, so the host needs
no `npm install` step and no install scripts run on it.

```bash
cp deploy/compose/.env.production.example deploy/compose/.env.production
chmod 600 deploy/compose/.env.production
```

Fill in `.env.production`. Copy the generated secrets from
`deploy/compose/.env`. Copy the API, account, and admin digest references
from `server-release.json` into the three image fields. Get
`server-release.json` from the protected server release workflow for the
version being deployed. Production Compose has no build contexts and does
not rebuild source on the host.

`npm run setup` writes four database secrets: the bootstrap superuser
password and one password for each of the three roles Compose creates. On
the first start of an empty `database` volume, the `db` service runs
`deploy/compose/initdb/10-roles.sh` as the bootstrap superuser and creates
`sesame_owner`, `sesame_app`, and `sesame_backup`. The `migrate` service
connects as `sesame_owner` and owns schema changes, the API connects as
`sesame_app` and can only write data, and `pg_dump` connects as
`sesame_backup`. The bootstrap superuser only starts PostgreSQL and creates
the roles; no service uses it at run time.

These values matter most. The comments in the example explain them as well.

- `SESAME_ADMIN_ENCRYPTION_KEY` encrypts every administrator's MFA secret and
  every queued account-email action link. If it changes, those secrets become
  unreadable and sign-in fails with the same message a wrong password gets,
  and queued email can never be delivered. The API refuses to start when SMTP
  is configured without this key. Back it up somewhere that survives this
  disk.
- `SESAME_RP_ID` must be the account portal's registrable domain. Changing
  it later invalidates every passkey already registered.
- `SESAME_TRUSTED_PROXIES` must name the proxy's network and nothing wider.
  The production stack pins its Compose network to `SESAME_COMPOSE_SUBNET`,
  which the example sets to `172.30.0.0/24`, and the two values must always
  name the same range. Never widen it to `0.0.0.0/0`: any client could then
  forge its own address and defeat rate limiting, and the API refuses to
  start with a range that covers every address. Verify the range with
  `docker network inspect sesame-prod_default`.
- `SESAME_SMTP_ADDR` points at your own STARTTLS relay. It is empty in the
  example, which runs the stack without verification, recovery, or
  email-change mail. Set the username and password unless that relay accepts
  mail only from the pinned Compose network. A relay on this host is reached
  by its certificate name when `SESAME_SMTP_HOST_GATEWAY` names it. Never
  point the address at a relay operated for another deployment.

These settings are optional or have a default:

- `SESAME_DEPLOYMENT_PROFILE` is `operator` unless you set it. Set it to
  `project` only for Sesame's own deployment, which adds the release, plan,
  and download routes described in `API.md`. Any other value stops the API.
- `SESAME_RELEASE_CANDIDATE_PUBLIC_KEY`, `SESAME_RELEASE_CANDIDATE_KEY_ID`,
  and `SESAME_RELEASE_CANDIDATE_TOKEN` open `POST /v1/release-candidates` for
  the desktop repository's CI. The route exists only under the `project`
  profile. Leave them empty to keep it closed.
- `SESAME_SMTP_FROM`, `SESAME_SMTP_USERNAME`, and `SESAME_SMTP_PASSWORD`
  belong to the relay named by `SESAME_SMTP_ADDR`.
  `SESAME_SUPPORT_NOTIFY_EMAIL` receives a short notice for each new support
  request.
- `SESAME_REGISTRATION_MODE` is `closed`, `invite`, or `public`, and
  `SESAME_RP_NAME` is the name a passkey prompt shows.
- The production file sets `SESAME_SESSION_SECURE` and
  `SESAME_ADMIN_SESSION_SECURE` to `true`, and no variable turns them off.
  `deploy/compose/compose.yaml` reads both from `deploy/compose/.env`.
- The production file does not pass `SESAME_SESSION_DOMAIN`,
  `SESAME_ADMIN_SESSION_DOMAIN`, `SESAME_CAPABILITY_KEY_ID`,
  `SESAME_MINIMUM_DESKTOP_VERSION`, or `SESAME_LATEST_DESKTOP_VERSION`. The
  API therefore uses host-only cookies, the key id `capability-v1`, and
  desktop version `0.1.0` for both bounds. Add a variable to the `api`
  service in `compose.prod.yaml` if you need another value.

Set `SESAME_BACKUP_AGE_RECIPIENTS` to one or more age public recipients,
separated by commas. Generate the identity with `age-keygen` on a device that
is not this host and keep the private identity there or on a hardware token.
The host holds only public recipients, so it can encrypt every backup and read
none of them. The deploy tool refuses to take a backup while this value is
empty. The Backups section has the restore procedure and the quarterly drill.

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

Every application container runs with a read-only root filesystem, all Linux
capabilities dropped, `no-new-privileges`, and fixed process and memory
limits. Only `/tmp` is writable in the Go containers. The account and admin
portals also mount the nginx cache and pid directories, and run as the
image's `nginx` user, UID 101. PostgreSQL keeps a writable data volume and
the capabilities its entrypoint needs to own the data directory. An existing
deployment picks the container settings up when
`docker compose -f deploy/compose/compose.prod.yaml --env-file deploy/compose/.env.production up -d`
recreates the changed containers; no extra step is required.

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
sudo systemctl edit caddy
```

In the editor, give Caddy the address it registers with the certificate
authority, then save:

```ini
[Service]
Environment=SESAME_ACME_EMAIL=you@example.com
```

Then install the Caddyfile and reload:

```bash
sudo cp deploy/caddy/Caddyfile.example /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

The Caddyfile example reads the address from `SESAME_ACME_EMAIL` and holds no
mailbox of its own. Caddy issues and renews certificates for every name in the
file on its own.

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
transaction as the change. A database trigger chains each row with a SHA-256
hash over its stored values, so inserts from an earlier revision during an
upgrade are chained too. The hourly maintenance run verifies the chain and
writes a signed checkpoint, covering the newest row, with the capability
signing key whenever new rows exist. It logs the covered sequence, the chain
hash, the signing key id and the signature in hex. Keep that log line outside
the database. It proves the covered history was not rewritten after the
checkpoint. It does not prove the entries were accurate.

## Updating to a new release

A semantic version tag starts the protected `server-release` workflow. The
workflow runs the server, portal, migration, compose, and container smoke
gates before it pushes any image. It publishes three version tags to GHCR,
records their digest references in `server-release.json`, and attaches a
dependency SBOM and signed provenance to each digest. Production uses the
digest references, never the version tags.

Before it changes anything, the deploy tool verifies each image's provenance
attestation with the GitHub CLI against the pinned release repository
`usesesame/sesame-server`, the release workflow, and the release tag. An image
whose attestation is missing, was signed for another ref, or names another
repository stops the deploy. A host with no GitHub access cannot deploy;
verification is not skippable.

The GitHub `server-release` environment must require release approval and
define `SESAME_API_ORIGIN`, `SESAME_PUBLIC_SITE_ORIGIN`, and
`SESAME_CAPABILITY_PUBLIC_KEY`. The key is the public half of the
production capability signing key. GitHub supplies registry and
attestation credentials only to the workflow. Host and deployment
credentials never enter the release job.

Deploy one release artifact set end to end. Check out the release tag on the
host and run the tool with Node. The tool imports only Node builtins, so it
needs no install step.

A deployment that predates the database roles needs one migration step
first. Add `SESAME_DATABASE_OWNER_PASSWORD`, `SESAME_DATABASE_APP_PASSWORD`,
and `SESAME_DATABASE_BACKUP_PASSWORD` from `deploy/compose/.env` to
`.env.production`, start the database, and run the role script once:

```bash
docker compose -f deploy/compose/compose.prod.yaml \
  --env-file deploy/compose/.env.production up -d --wait db
docker compose -f deploy/compose/compose.prod.yaml \
  --env-file deploy/compose/.env.production \
  exec -T db sh /docker-entrypoint-initdb.d/10-roles.sh
```

The script is idempotent. It creates the three roles and hands the existing
tables and functions to `sesame_owner` so the deploy tool can migrate them.
Then run the deploy:

```bash
git fetch --tags
git checkout v<version>
node scripts/deploy-release.mjs plan server-release.json     # what would happen, nothing changes
node scripts/deploy-release.mjs deploy server-release.json
node scripts/deploy-release.mjs status
node scripts/deploy-release.mjs rollback [version]
```

The deploy tool runs these stages, and every stage is safe to retry after
an interruption:

1. Pull all three images and verify their digests, identity labels, and
   GitHub provenance.
2. Take a `pg_dump` backup and encrypt it to the age recipients.
3. Stream a fresh copy of the live database into a scratch database and run
   the candidate migration and the previous revision's API against it. This
   is the rehearsal. The host holds no age identity, so the encrypted backup
   from step 2 cannot be read here.
4. Apply the migration to the production database.
5. Start the candidate API beside the live stack on a port Caddy does not
   route, and probe `/livez` and `/readyz`.
6. Rename the staged env file over `.env.production` and bring the
   production stack up with a bounded readiness wait.
7. Probe the live endpoints and both portals, then record the deployment.

If a stage fails, the failure is contained and recorded:

- A failed image verification stops the run before the backup, the rehearsal,
  or any change.
- A failed health check before the switch changes no traffic. The previous
  revision keeps serving, and the env file is untouched.
- A failed health check after the switch rolls back automatically. The tool
  rewrites the three image lines in `.env.production` back to the recorded
  previous digests, restarts the stack, probes it, and records the rollback.
  The interrupted attempt is recorded, and rerunning `deploy` with the same
  `server-release.json` converges on that revision instead of duplicating it.
- Deploying an older release than the recorded one is refused. Re-presenting
  the same release with changed bytes is refused. Artifact identity is
  immutable.
- The rehearsal proves the previous revision still runs against the
  migrated schema, so an ordinary rollback after migration is safe.
  Rollback restores the recorded image references and leaves every other
  value in `.env.production` in place. It never reverts migrations. If a
  release ships an incompatible database contraction, recovery means
  restoring the recorded backup,
  `deploy/state/backups/sesame-<version>-<timestamp>.sql.gz.age`, onto the
  previous revision by hand with the age identity. That is a deliberate
  operator procedure.

State and backups live under `deploy/state/`, which is gitignored:
`deployed.json`, `pending.json`, and `backups/`. The tool reads
`.env.production` at mode 0600 and never prints it. It records the image
references it needs for rollback in `deployed.json` and writes no copy of the
env file, so the deploy state holds no secrets. The encrypted files in
`backups/` are the only database copies the host keeps. If an earlier
checkout left plaintext env copies under `deploy/state/history/`, delete that
directory; nothing reads it, and it holds every secret from that deployment.

## Upgrading an existing stack

An earlier `compose.prod.yaml` set `SESAME_DEPLOYMENT_PROFILE` to `project` by
itself. It now reads the variable and defaults to `operator`. A deployment that
serves Sesame's release, plan, and download routes must add
`SESAME_DEPLOYMENT_PROFILE=project` to `.env.production` before its next
deploy, because the deploy tool rewrites only the three image lines. Without
the line the API runs as `operator` and answers `404` on the release
administration routes and `503` on the download routes.

An existing `.env.production` keeps its own values. The deploy tool rewrites
only the three image lines, so nothing narrows a wide
`SESAME_TRUSTED_PROXIES` for you. Set it to the pinned Compose subnet,
`172.30.0.0/24` in the example, and keep it equal to
`SESAME_COMPOSE_SUBNET`. The API refuses to start when the value holds an
address outside loopback, private, or link-local space, such as `0.0.0.0/0`,
`0.0.0.0/1`, or `::ffff:0.0.0.0/96`. The old `172.16.0.0/12` still starts
with a warning that the range is wider than `/24`, and it trusts every peer
in that range, not only the proxy.

Changing the Compose `ipam` subnet recreates the `sesame-prod_default`
network on the next `up -d`. Docker stops and recreates every container
attached to that network, including the database, so expect a short outage.

The old `mail.usesesame.app:host-gateway` mapping is gone. When the relay
runs on this host and must be reached by its certificate name, set
`SESAME_SMTP_HOST_GATEWAY` to that name. Review host firewall and Postfix
`mynetworks` rules written for the old bridge subnet. They no longer match.

## Backups

Two things matter, and they fail differently.

Every `npm run deploy:release -- deploy` streams a `pg_dump` through `gzip`
and `age` and writes the result under `deploy/state/backups/` as
`sesame-<version>-<timestamp>.sql.gz.age`. `SESAME_BACKUP_AGE_RECIPIENTS`
supplies the public recipients. The host holds no private identity, so it
cannot read the backups it writes. Keep the identity on a separate device and
keep a copy of the encrypted files off the host as well.

A manual backup looks the same:

```bash
set -o pipefail
out=sesame-$(date +%F).sql.gz.age
docker compose -f deploy/compose/compose.prod.yaml \
  --env-file deploy/compose/.env.production \
  exec -T db pg_dump -U sesame_backup sesame | gzip \
  | age -r age1... > "$out" \
  || { rm -f "$out"; echo "backup failed" >&2; }
```

With `pipefail` set, the pipeline fails when `pg_dump` fails, and the
failure branch removes the partial file and warns on stderr.

Restore is a manual procedure that needs the identity. A scratch database
has only the bootstrap superuser. The dump carries `OWNER TO` and grant
statements for `sesame_owner`, `sesame_app`, and `sesame_backup`, and
`pg_dump` does not dump roles, so create them in the scratch database first:

```bash
docker exec -i <scratch-database> psql -q -v ON_ERROR_STOP=1 -U sesame -d sesame <<'SQL'
CREATE ROLE sesame_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE sesame_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE sesame_backup LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
GRANT pg_read_all_data TO sesame_backup;
GRANT CREATE, USAGE ON SCHEMA public TO sesame_owner;
SQL
```

Then decrypt with the identity and load the dump:

```bash
set -o pipefail
age --decrypt --identity /path/to/backup-identity.txt sesame-<version>.sql.gz.age \
  | gunzip \
  | docker exec -i <scratch-database> psql -q -v ON_ERROR_STOP=1 -U sesame -d sesame \
  || echo "restore failed" >&2
```

Run the restore where the identity is available and the scratch database is
reachable, and do not write the decrypted dump to disk. Check the schema
version and a known account row. Restoring over the live database destroys
it, so rehearse first.

A lost database loses accounts. A lost `SESAME_ADMIN_ENCRYPTION_KEY` locks
every administrator out while the database stays perfectly intact, which is
the harder failure to recover from. Back up `deploy/compose/.env.production`
separately, somewhere other than this host.

Restore one backup at least quarterly: decrypt it with the identity, load it
into a scratch database, and check the schema version and a known account row.
Record the date and the result. A drill that has not run is not a pass.

## Watching a running deployment

The API writes one line per request to its container log:

```bash
docker compose -f deploy/compose/compose.prod.yaml \
  --env-file deploy/compose/.env.production logs -f api
```

Caddy writes one access line per request to `api.usesesame.app` into the
journal, where the Caddy service log lives:

```bash
journalctl -u caddy -f
```

Every request the API answers logs `Sesame API request` with `requestId`,
`method`, `route`, `audience`, `status`, and `durationMs`. `route` is the
registered pattern, so a support request logs as
`/v1/account/support/{ticketID}` and never carries the ticket id. `audience` is
`public`, `website`, `admin`, `desktop`, or `release`, and `unknown` for a
request that matched no route. The response `X-Request-ID` header carries the
same id as the line, and any handler error line for that request carries it
too. The line is INFO, except a response with status 500 or higher, which is
ERROR, and the `/livez`, `/readyz`, and `/healthz` probes, which are DEBUG.

The hourly maintenance run logs `purged delivered email outbox records` and
`purged failed email outbox records` with a `count` field only when it removed
rows. A failed purge logs a warning instead:
`Sesame API could not purge expired security records`,
`Sesame API could not purge delivered email outbox records`, or
`Sesame API could not purge failed email outbox records`. The counters are
normal; the warnings are not. The Sync purge lines,
`purged Sesame Sync enrollment challenges` and
`purged revoked Sesame Sync devices`, appear only in the development preview,
which is not deployed here.

`GET /livez` answers 200 while the process runs. `GET /readyz`, and its
deprecated alias `GET /healthz`, answer 200 only while the database answers,
and 503 with `status` `not_ready` and `accounts` `unavailable` when it does
not.

Alert on:

- The 5xx rate at `api.usesesame.app` from the Caddy access log. It covers the
  502 and 504 responses Caddy sends when the API is down, which the API cannot
  log itself.
- `Sesame API request` lines with status 500 or higher, to name the route and
  the `requestId` behind that rate. One failed request is not an incident;
  alert on the rate over a window.
- A `/readyz` probe that fails twice in a row. `/livez` stays green while the
  database is unreachable.
- Any maintenance warning above.

Do not alert on the DEBUG probe lines, on 4xx statuses, or on `route=unmatched`.
Those are normal.

## Operating notes

- **Email action links are sealed, never stored in plaintext.** The API seals
  each queued action link with a key derived from
  `SESAME_ADMIN_ENCRYPTION_KEY`, and the delivery worker opens it only to send
  the message. A delivered or failed outbox row,
  including its sealed link, is purged seven days after its last update; an
  undelivered row expires at its own deadline first. Verification links live
  24 hours, recovery and email-change links 30 minutes, and support access
  links seven days. Deploying this release fails every queued action email
  that carries a single-use token and clears its link, so those users request
  a new one. Queued notifications without a token stay pending and send, with
  their link cleared.
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
- **Container limits** live in `deploy/compose/compose.prod.yaml`. If a
  service is killed for exceeding its memory limit, `docker inspect` reports
  `OOMKilled` for that container; raise its `mem_limit` and run `up -d`
  again. Keep the limits and the read-only root filesystem in place.
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
