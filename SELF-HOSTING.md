# Self-hosting Sesame

This page covers the self-hosted Sesame server, `sesame-server`. It is a separate program from the hosted service that the rest of this repository builds, and it shares no data with it. The hosted stack is described in [DEPLOYMENT.md](DEPLOYMENT.md).

## What a self-hosted Sesame does today

A self-hosted Sesame pairs your Sesame desktop apps to a server you run. You sign in to a console in your browser, add the people who use the server, and create one-time pairing links. A desktop app that redeems a link receives a device token, and from then on the console lists that desktop, shows when it last connected and lets you revoke it.

Sync is not available. The server stores no vault data, no vault keys and no passwords for vaults, and it tells every desktop that sync is unavailable. Pairing a desktop to your server gives you a list of connected devices and nothing else.

The server is one static program with a SQLite database in a single data directory. It needs no database server, no mail server and no other service.

It can also tell you when a newer release exists, but only if you allow it. Read [Update notices](#update-notices) for exactly what that sends.

Pairing needs a desktop build that has the setting for connecting to your own server. Check the desktop release notes for the first version that includes it.

It does not offer accounts for the people you add. Read [What you give up without accounts](#what-you-give-up-without-accounts) before you decide who gets a device.

## Quick start

You need a machine with Docker and the Compose plugin, and about five minutes. This starts the server on the same machine, reachable only from that machine, which is enough to try it. The section on [network and HTTPS](#network-and-https) covers reaching it from other computers.

1. Pick the release you want from the repository's releases page and set its version. The examples use 0.1.0.

   ```sh
   mkdir sesame && cd sesame
   export SESAME_IMAGE_TAG=0.1.0
   curl -fsSLO "https://raw.githubusercontent.com/usesesame/sesame-server/v${SESAME_IMAGE_TAG}/deploy/selfhost/compose.yaml"
   ```

2. Start the server and read the setup link from its log.

   ```sh
   docker compose up -d
   docker compose logs sesame | grep 'setup#token'
   ```

3. Open the link in a browser on the same machine. It looks like `http://localhost:8787/setup#token=...`. Use `localhost` and not `127.0.0.1`, because the server answers sign-in requests only at the address in `SESAME_PUBLIC_URL`.

   If the server is on another machine, forward the port first with `ssh -L 8787:127.0.0.1:8787 your-server` and open the same link on your own computer.

4. Choose an owner name and a password of at least 12 characters, add the shown key to an authenticator app, and enter the current six digit code. You are signed in.

The compose file publishes the port on `127.0.0.1` only and keeps the data in a Docker volume named `sesame-selfhost_sesame-data`. To keep the setting between restarts, put `SESAME_IMAGE_TAG` in a `.env` file next to the compose file. [deploy/selfhost/.env.example](deploy/selfhost/.env.example) lists the others.

## First run and pairing

The first start creates the instance secrets, the database and a setup link, and writes the link to the log. The link works once and expires after 24 hours. If it expires before you use it, restart the server and it prints a new one. Once the first owner exists the server stops printing first-run links.

Sign-in always takes the password and a current code from the authenticator app. A code works once, so after a sign-in you wait for the next code before you sign in again.

The console has these pages: Devices, Members, Pairing, Audit log, Owners, Settings and System.

Members are the people who use the server, such as family members or colleagues. A member has a name and holds devices, and cannot sign in anywhere. Owners are the people who can sign in to the console. You can invite more owners on the Owners page, and each invited owner gets a setup link that works once for 24 hours.

To connect a desktop:

1. Open Pairing, choose yourself or a member, and optionally name the device.
2. Copy the link. It is shown once, works once and expires after 10 minutes.
3. In the desktop app, open Settings, choose to connect to your own server, and paste the whole link.

A link has the form `https://your-server/pair#code=...&fp=...`. Opening it in a browser only shows an explanation, because the address is meant for the desktop app. The `fp` part is the fingerprint of the server's signing key. The Settings page shows the same fingerprint, so you can compare the two.

Pairing a device for a member asks for your password and a code again unless you signed in within the last 10 minutes. Revoking a device or removing a member does the same. A revoked device is refused at its next request.

Each device token lasts 90 days from pairing. The server does not extend it when the desktop is used, so a desktop that reaches 90 days must be paired again.

## Network and HTTPS

Passwords, one-time codes and device tokens cross the network between the browser or desktop and the server. For that reason the server refuses to start with an `http://` address in `SESAME_PUBLIC_URL` unless the host is `localhost`, `127.0.0.1` or `::1`. Plain HTTP is for the same machine only. Anything reachable from another computer needs HTTPS in front of the server.

The server does not terminate TLS itself. It listens on a local port, and a reverse proxy or Tailscale presents the HTTPS address. Three settings have to match the proxy.

`SESAME_PUBLIC_URL` is the exact address people type, such as `https://sesame.example.net`. Pairing links are built from it. Every owner and desktop request must also arrive with a `Host` header equal to its host, and every owner request that changes something must carry an `Origin` header equal to it. A request with another host gets 421 `host_mismatch`. The server never reads `X-Forwarded-Host`, so the proxy must pass the original `Host` header. All the recipes below do.

`SESAME_TRUSTED_PROXIES` lists the addresses of the proxy as comma separated CIDR ranges inside loopback, private or link-local space. The server reads the visitor address from `X-Forwarded-For` only when the connection comes from one of these ranges, and it uses the visitor address for rate limits. Leave it empty if nothing sits in front of the server. If it is missing behind a proxy, the log warns that every visitor shares one rate limit.

`SESAME_ADDR` is where the server listens. The default is `127.0.0.1:8787`, and the image sets `0.0.0.0:8787` inside the container, which the compose file publishes on the host loopback only. Keep the published port on loopback so that only the proxy can reach it.

The files in [deploy/selfhost](deploy/selfhost) hold each recipe as plain configuration.

### Caddy on a domain

Point the domain at the machine, run the server with `SESAME_PUBLIC_URL=https://sesame.example.net`, and use [Caddyfile](deploy/selfhost/Caddyfile) with `SESAME_HOST=sesame.example.net` set in the environment of the Caddy service, for example with `systemctl edit caddy`. Caddy obtains the certificate and passes the `Host` header on its own.

If Caddy runs on the host and the server runs from the compose file, set `SESAME_TRUSTED_PROXIES=172.31.88.0/24` in `.env`. The compose file fixes its network to that range, and Docker normally presents traffic from the host to the container as coming from the network's gateway. If the log still warns about `X-Forwarded-For`, the proxy arrives from another range, and `docker network inspect sesame-selfhost_default` shows which.

### nginx on a domain

Copy [nginx.conf](deploy/selfhost/nginx.conf), replace `sesame.example.net` with your name, and point the two certificate paths at your certificate. The file passes `Host` with `$http_host` so that a port in the address survives, appends to `X-Forwarded-For`, and limits request bodies to 64 KiB, which is above the 16 KiB the server accepts. Set `SESAME_TRUSTED_PROXIES` the same way as for Caddy.

### Traefik on a domain

[compose.traefik.yaml](deploy/selfhost/compose.traefik.yaml) is an override for a Traefik that already runs in Docker with a `websecure` entry point and a certificate resolver. It removes the published port, joins the Traefik network and adds the router labels.

```sh
export SESAME_HOST=sesame.example.net
export SESAME_PUBLIC_URL=https://sesame.example.net
export SESAME_TRUSTED_PROXIES=172.18.0.0/16
docker compose -f compose.yaml -f compose.traefik.yaml up -d
```

Replace the trusted range with the subnet of your Traefik network, which `docker network inspect traefik` prints. Set `TRAEFIK_NETWORK` and `TRAEFIK_CERT_RESOLVER` if yours are not named `traefik` and `letsencrypt`. Traefik passes the `Host` header unchanged unless a router turns that off, and the labels set it on explicitly.

### Tailscale

Tailscale gives the server an HTTPS address that only your tailnet can reach, with no domain and no open port. Turn on MagicDNS and HTTPS certificates in the Tailscale admin console first.

```sh
tailscale serve --bg --https=443 http://127.0.0.1:8787
tailscale serve status
```

Set `SESAME_PUBLIC_URL` to the address that `tailscale serve status` prints, for example `https://sesame.tailnet-name.ts.net`, and set `SESAME_TRUSTED_PROXIES=172.31.88.0/24` when you use the compose file. Use `tailscale serve` and not `tailscale funnel`, because funnel opens the server to the whole internet. Desktops that pair to this address must be on the tailnet.

### Binary under systemd

If you do not want Docker, run the static program directly. Copy it out of the release image, which is the same file the image runs:

```sh
docker create --name sesame-extract ghcr.io/usesesame/sesame-server-selfhost:0.1.0
docker cp sesame-extract:/sesame-server ./sesame-server
docker rm sesame-extract
sudo install -m 0755 sesame-server /usr/local/bin/sesame-server
```

Then create the user and the settings file, and install the unit:

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin sesame
sudo install -d -m 0750 -o root -g sesame /etc/sesame
sudo install -m 0640 -o root -g sesame deploy/selfhost/sesame-server.env.example /etc/sesame/sesame-server.env
sudo install -m 0644 deploy/selfhost/sesame-server.service /etc/systemd/system/sesame-server.service
sudoedit /etc/sesame/sesame-server.env
sudo systemctl enable --now sesame-server
sudo journalctl -u sesame-server | grep 'setup#token'
```

The unit runs as the `sesame` user with a private state directory at `/var/lib/sesame`, no capabilities, a read-only view of the rest of the system and a system call filter. `systemd-analyze security` rates it 1.2 on the offline check. The example settings file expects a proxy on the same machine, so it trusts `127.0.0.1/32`.

To run a command such as a backup against this install, load the same settings:

```sh
sudo -u sesame sh -c 'set -a; . /etc/sesame/sesame-server.env; exec /usr/local/bin/sesame-server backup'
```

## Settings

Each setting comes from an environment variable, then from the config file, then from the default. The config file is `config.json` inside the data directory, or the file named by `SESAME_CONFIG_FILE`. It takes the keys in the last column and nothing else.

| Variable | Default | Config key |
| --- | --- | --- |
| `SESAME_PUBLIC_URL` | `http://localhost:<port>` | `publicUrl` |
| `SESAME_ADDR` | `127.0.0.1:8787` | `addr` |
| `SESAME_TRUSTED_PROXIES` | none | `trustedProxies` |
| `SESAME_DATA_DIR` | `/data` | not available |
| `SESAME_LOG_LEVEL` | `info` | `logLevel` |
| `SESAME_METRICS` | `false` | `metrics` |
| `SESAME_BACKUP_INTERVAL` | `24h` | `backupInterval` |
| `SESAME_UPDATE_FEED_URL` | `https://api.usesesame.app/v1/updates/selfhost.json` | not available |
| `SESAME_UPDATE_PUBLIC_KEYS` | the keys compiled into the build, if any | not available |
| `SESAME_INSTALL_KIND` | detected | not available |

`SESAME_METRICS=true` adds a Prometheus endpoint at `/metrics` that answers only direct requests from loopback or private addresses. `SESAME_BACKUP_INTERVAL=0` turns off scheduled backups, and the smallest interval is one minute. The server logs every effective setting and where it came from when it starts. It exits with a message when a setting is invalid, for example when `SESAME_PUBLIC_URL` is plain HTTP on a public name.

`SESAME_UPDATE_FEED_URL`, `SESAME_UPDATE_PUBLIC_KEYS` and `SESAME_INSTALL_KIND` belong to [Update notices](#update-notices). The server exits with a message when `SESAME_UPDATE_FEED_URL` is not an https address, holds a user name or password, or has a query or a fragment.

The server also accepts `SESAME_SMTP_*` settings and checks them, but nothing in the server sends email yet.

## Backup, restore, upgrade and reset

All state is in the data directory: the database `sesame.db`, the directory `secrets` and the optional `config.json`.

### Back up

The server writes a backup to `backups/` in the data directory every 24 hours and keeps the newest seven scheduled ones. Each backup is one tar file with a consistent copy of the database, the three secret files and a checksum for each. You can take one at any time, also while the server runs:

```sh
docker compose exec sesame /sesame-server backup
docker compose exec sesame /sesame-server backup /data/backups/before-trip.tar
```

Copy backups to another machine. The scheduled ones sit on the same volume as the data, so they do not help when the disk is lost. A backup holds the keys that unlock owner sign-in, so keep it as private as the server.

Check a backup without restoring it:

```sh
docker compose exec sesame /sesame-server check /data/backups/before-trip.tar
```

`check` without a file name checks the live database, including the audit chain. `export` writes the members, devices and audit log as JSON without tokens, secrets or password data.

### Restore

Restore needs the server stopped, and it refuses to run while the server uses the data directory. The command moves the old contents aside into a `restore-previous-` directory and installs the backup. It refuses an archive whose checksums fail or whose secrets do not match its database.

```sh
docker compose stop sesame
docker compose run --rm --no-deps sesame restore /data/backups/before-trip.tar
docker compose up -d
```

For a backup file that lives outside the volume, mount it read-only into the one-off container with `-v "$PWD/before-trip.tar:/restore.tar:ro"` and restore `/restore.tar`. A restore interrupted by a crash finishes itself the next time the server or a restore starts.

Devices revoked after the backup was taken become active again when you restore it. Revoke them again after a restore if that matters.

### Upgrade

Change `SESAME_IMAGE_TAG` and restart.

```sh
docker compose pull
docker compose up -d
```

When a release changes the database schema, the server first writes a backup named `sesame-premigration-` in `backups/` and then migrates. A server refuses a database that a newer version wrote, so you cannot run an older release over migrated data. To go back, restore the pre-migration backup with the older image.

Before you upgrade, read the release notes and take a manual backup. Verify the image first if you want to, as described at the end of this page.

The Updates page in the console can tell you when a newer release exists and shows these commands for your install. See [Update notices](#update-notices).

### Reset an owner

If an owner loses the password or the authenticator, run this on the host:

```sh
docker compose exec sesame /sesame-server owner reset "owner name"
```

It prints a setup link for that owner, ends every session of that owner, revokes every device paired to that owner and cancels their unused pairing codes. The owner cannot sign in with the old credentials until the link is used, and must pair those devices again afterwards. Devices that belong to members stay linked. The link works once and expires after 24 hours. Running it needs access to the data directory and the original secrets, so it is a host operation and no one can request it over the network.

## Update notices

The console has an Updates page. When you allow it, the server checks a signed list of releases and shows you whether a newer version exists, what changed and which commands to run. It only notifies. It never downloads a release, installs one, restarts itself or touches the Docker socket, so you run the commands yourself.

You choose at setup whether the server checks. If you skip the question, the choice stays unset, the server makes no request and the Updates page asks you. You can change the choice on that page at any time, and the change needs a sign-in or a password and code confirmation from the last 10 minutes, like other sensitive changes. The audit log records each change.

### What a check sends

A check is one plain `GET` request to a fixed address, `https://api.usesesame.app/v1/updates/selfhost.json` unless you set `SESAME_UPDATE_FEED_URL`. The request has no query string, no instance id, no cookie and no version. Its only headers are `Accept`, `Accept-Encoding` and a `User-Agent` of `sesame-server-update-check` that is the same for every server. The feed host can see the IP address your server connects from and the time of the request, as with any web request. If the server's environment sets `HTTPS_PROXY`, the request goes through that proxy and the proxy sees it too. The code that builds the request is `internal/selfhost/updates/fetch.go`, and a test inspects the request it sends.

The server checks about once a day, and when you press the check button on the Updates page, which allows 3 presses per hour. It waits up to an hour longer than a day so that many servers do not call at the same moment. When it starts and the last good result is less than a day old it does not check again, and otherwise it waits up to 10 minutes first. After a failed fetch it tries again in about an hour. It follows redirects only within the same address, reads at most 256 KiB and gives up after 10 seconds.

### What the server checks and what it does not

The feed is a JSON document signed with an Ed25519 key. The server accepts it only when the signature verifies against a public key pinned in the build or in `SESAME_UPDATE_PUBLIC_KEYS`, the document has not expired, and its sequence number is not lower than the highest one the server has accepted before. A lower number looks like someone replaying an old feed, so the server refuses it, keeps the last good result and shows `feed_rollback`. The server keeps the highest number separately for each signing key, so a newly pinned key starts fresh. If a maintainer published a number that was too high by mistake, `docker compose exec sesame /sesame-server updates reset-sequence` makes the server forget every key's highest number and writes an entry to the audit log. A feed that has expired is no longer offered: the page keeps the last release it saw, shows `feed_expired` and hides the commands until a fresh feed arrives. Every address in the feed must be https and every hash must have the exact shape of a SHA-256.

That signature is the only thing the server verifies. It does not download the image or the program and does not check them. The page shows the image digest or the SHA-256 that the signed feed names, and the commands compare against it, so you can see whether what you pulled or downloaded matches what the feed said. A signature from the Sesame key shows the feed came from whoever holds that key. It does not show that a release is free of faults, so read the release notes first.

If the server cannot use a feed it keeps the last good result and shows a short code on the Updates page:

| Code | Meaning |
| --- | --- |
| `feed_unreachable` | The request failed, the answer was not 200, or it redirected to another address. |
| `feed_invalid` | The signature, the key, the shape of the document or its size was wrong. |
| `feed_rollback` | The feed is older than one the server already accepted. |
| `feed_expired` | The feed's expiry time has passed. |
| `not_configured` | This build has no public key, so the server makes no request. |

### The commands

The server detects how it is installed. It treats `SESAME_INSTALL_KIND=container` as a container, which the image and `deploy/selfhost/compose.yaml` both set, and falls back to the presence of `/.dockerenv`. Anything else counts as a binary install. Set `SESAME_INSTALL_KIND` to `container` or `binary` yourself if the detection is wrong for you.

For a container install the page shows the new `SESAME_IMAGE_TAG` value, `docker compose pull`, a command that prints the digest of the image you pulled, the image reference with its digest from the feed to compare it with, and `docker compose up -d`, to run in the directory that holds your compose file. Compare the two digests before you run the last command. Take a manual backup first, as the [upgrade](#upgrade) section says.

For a binary install the page shows a `curl` command that downloads the program for your operating system and architecture over https only, also after redirects, a command that compares its SHA-256 with the one in the feed, an `install` command that replaces the running program and `sudo systemctl restart sesame-server`. It shows nothing for a platform the feed does not list.

### Keys and the build

The public key comes from the `SESAME_UPDATE_PUBLIC_KEYS` build argument of `Dockerfile.selfhost`, or from the same variable at run time. The value is a comma separated list of `keyId:key` entries, where the key is an unpadded base64url Ed25519 public key. A variable set at run time replaces the keys compiled into the build. A build that has neither reports update checks as not configured and makes no request. The release workflow passes the repository variable `SESAME_UPDATE_PUBLIC_KEYS` to the image build, and an empty variable is allowed. So the official image checks for updates only once Sesame has published a signing key and put it in that variable for the release you run. Until then the Updates page says `not_configured`, whatever choice you made. To turn checks off for good, leave the choice unset or choose Off.

Maintainers who publish the feed read [deploy/selfhost/UPDATES.md](deploy/selfhost/UPDATES.md).

## What you give up without accounts

The hosted Sesame service has accounts with email addresses. A self-hosted server has owners and members instead, and the difference has consequences.

- Members cannot sign in. Only owners sign in, so a member cannot see their own devices, revoke a lost laptop or change anything. An owner does all of it for them.
- There is no new-device email. Nothing tells a member or an owner when a device is paired. The Audit log and the Devices page record it, but someone has to look.
- There is no per-person login history. The audit log records owner sign-ins and device events. A member has no sign-ins to record, and the Devices page shows when each device last connected and not where from.
- Whoever holds a signed-in owner session can create a device. Pairing a device for yourself asks for no password, so a stolen or unattended owner browser session lets someone attach a device to that owner for as long as the session lasts, up to 12 hours. Pairing for a member asks for the password and a code unless the owner signed in within the last 10 minutes.
- Whoever controls the host controls the server. Anyone who can run commands as the server's user or read the data directory can run `owner reset` and sign in as an owner, or copy the secrets and the database.
- Device tokens are not bound to hardware. A token copied from a desktop works from any machine until the owner revokes it or it expires after 90 days.

Keep the owner list short, and look at the Devices page now and then to check that every listed device is one you know.

## The audit log

Every action that changes the server is written to an audit log in the database. Each row holds a SHA-256 hash that covers the previous row, so a changed, removed or inserted row breaks the chain. The Audit log page and `sesame-server check` verify the chain and report the first row that fails.

The chain detects tampering and does not prevent it. The hash has no secret key. Anyone who can write the data directory can edit rows and then recompute every later hash, and the result passes the check. It also does not protect against the last rows being deleted, because a shorter chain is still a valid chain. To make tampering visible from the outside, copy the head hash from the System page or from an `export` to a place the server's host cannot write.

This is a different chain from the one in the hosted service. The two use different formats and different storage, so one cannot verify or continue the other.

## Secrets

The `secrets` directory in the data directory holds three files that the server creates on first start with mode 0600 inside a directory of mode 0700.

| File | What it protects |
| --- | --- |
| `admin-encryption.key` | The owners' authenticator secrets in the database |
| `signing.key` | The server's identity, shown as the fingerprint in pairing links |
| `ip-pepper.key` | Visitor addresses in the rate-limit records |

Losing `admin-encryption.key` makes the owners' authenticator secrets in the database unreadable, so no owner can sign in, and `owner reset` cannot help because it needs the original key. The server will not generate a replacement for any missing secret while `sesame.db` exists. It exits and asks you to restore the `secrets` directory from a backup. Without a copy, the only way out is a new data directory, and every device must be paired again.

Back the secrets up together with the database. The scheduled backups already include all three files, so the usual failure is a copy of `sesame.db` made by hand without the `secrets` directory. A restore refuses a database whose secrets do not match.

## Verify and build the image

Release images are published to `ghcr.io/usesesame/sesame-server-selfhost` for `linux/amd64` and `linux/arm64`, with a build provenance attestation on the image index and an SBOM attestation on each platform image. Each platform image also has its own tag, ending in `-linux-amd64` or `-linux-arm64`. Verify an image with the GitHub CLI before you run it:

```sh
gh attestation verify oci://ghcr.io/usesesame/sesame-server-selfhost:0.1.0 \
  --repo usesesame/sesame-server \
  --signer-workflow usesesame/sesame-server/.github/workflows/release.yml \
  --source-ref refs/tags/v0.1.0
```

To build it yourself from a checkout of the release tag:

```sh
docker build -f Dockerfile.selfhost -t sesame-selfhost:local .
```

The build compiles the console and then a static program with `CGO_ENABLED=0`, and it runs as the unprivileged user 65532 with a `/data` volume owned by that user. If you mount a host directory at `/data` instead of the named volume, run `chown 65532:65532` on it first.

## When something goes wrong

| What you see | What it means |
| --- | --- |
| 421 `host_mismatch` | The `Host` header differs from `SESAME_PUBLIC_URL`. Use the exact address from `SESAME_PUBLIC_URL` and make the proxy pass `Host` unchanged. |
| 403 `origin_not_allowed` | The browser sent an `Origin` other than the public address. Open the console at the address in `SESAME_PUBLIC_URL`. |
| 429 `too_many_attempts` | Sign-in allows 5 tries per minute for a name. Wait for the number of seconds in `Retry-After`. |
| The server exits at start with a message about `SESAME_PUBLIC_URL` | The address must start with `https://` unless the host is `localhost`, `127.0.0.1` or `::1`. |
| The log says another server is using the data directory | A second process or a running restore holds the lock. Stop it first. |
| The Updates page shows `feed_rollback` | The feed host served a feed older than one this server already accepted. The server keeps the last good result. If the host's own sequence numbers went backwards by mistake, it must publish a feed with a higher number. |
| The log says the database was written by a newer server | Run the newer release again, or restore a backup made by the older one. |

The HTTP routes are listed in [internal/selfhost/server/API.md](internal/selfhost/server/API.md). Report a security problem as [SECURITY.md](SECURITY.md) describes.
