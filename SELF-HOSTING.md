# Self-hosting Sesame

You can run your own Sesame server and pair your Sesame desktop apps to it instead of the hosted service. You manage it from a console in your browser, where you add the people who use it, create one-time pairing links and see or revoke every paired desktop. Sync of encrypted vaults is available too, and it stays off until you turn it on.

The server is one program with a SQLite database in a single data directory. It needs no database server, no mail server and no other service. It shares no data with the hosted service, which is described in [DEPLOYMENT.md](DEPLOYMENT.md).

## Before you start

You need a machine with Docker and the Compose plugin. Pairing needs a Sesame desktop newer than 0.3.0, which shows Your own server in Settings.

A self-hosted server has no accounts for the people you add. Only owners sign in, and they do everything for the members. Read [What you give up without accounts](#what-you-give-up-without-accounts) before you decide who gets a device.

## Quick start

This starts the server on one machine, reachable only from that machine, which is enough to try it. [Network and HTTPS](#network-and-https) explains how to reach it from other computers afterwards.

1. Download the compose file for the latest release and store its version in a `.env` file next to it.

   ```sh
   mkdir sesame && cd sesame
   SESAME_IMAGE_TAG=$(curl -fsSL https://api.github.com/repos/usesesame/sesame-server/releases/latest | sed -n 's/.*"tag_name": *"v\([^"]*\)".*/\1/p')
   echo "SESAME_IMAGE_TAG=${SESAME_IMAGE_TAG}" > .env
   curl -fsSLo compose.yaml "https://raw.githubusercontent.com/usesesame/sesame-server/v${SESAME_IMAGE_TAG}/deploy/selfhost/compose.yaml"
   cat .env
   ```

   `cat .env` should print a line such as `SESAME_IMAGE_TAG=0.5.0`. To run another release, put its version there and follow that release's own `SELF-HOSTING.md`, because commands can differ between releases.

2. Start the server, wait until it answers and read the setup link from its log.

   ```sh
   docker compose up -d
   curl -fsS --max-time 5 --retry 30 --retry-delay 1 --retry-all-errors --retry-max-time 60 http://localhost:8787/livez
   docker compose logs sesame | grep 'setup#token'
   ```

   The `curl` line retries for up to a minute and then prints `{"status":"ok", ...}`. If it fails, run `docker compose logs sesame` and look in [When something goes wrong](#when-something-goes-wrong).

3. Open the link, which looks like `http://localhost:8787/setup#token=...`, in a browser on the same machine. Use `localhost` and not `127.0.0.1`, because the server only answers at the address in `SESAME_PUBLIC_URL`. If the server runs on another machine, forward the port with `ssh -L 8787:127.0.0.1:8787 your-server` and open the link on your own computer.

4. Choose an owner name and a password of at least 12 characters, add the shown key to an authenticator app and enter its current six digit code. You are now signed in.

The compose file publishes the port on `127.0.0.1` only and keeps the data in a Docker volume named `sesame-selfhost_sesame-data`. [deploy/selfhost/.env.example](deploy/selfhost/.env.example) lists the other settings you can put in `.env`.

## First run and pairing

The setup link from the first start works once and expires after 24 hours. If it expires, restart the server and it prints a new one. Once an owner exists, the server stops printing setup links.

Signing in always takes the password and a current code from the authenticator app. Each code works once, so wait for the next code before you sign in again.

Members are the people who use the server, such as family or colleagues. A member has a name and devices but cannot sign in. Owners sign in to the console, and you can invite more of them on the Owners page.

To connect a desktop:

1. Open Pairing, choose yourself or a member and optionally name the device.
2. Copy the link. It is shown once, works once and expires after 10 minutes.
3. In the desktop app, open Settings, choose to connect to your own server and paste the whole link.

A pairing link looks like `https://your-server/pair#code=...&fp=...`. The `fp` part is the fingerprint of the server's signing key, and the Settings page in the console shows the same fingerprint so you can compare them.

Pairing a device, revoking one or removing a member asks for your password and a code again unless you signed in within the last 10 minutes. A revoked device is refused at its next request. A device token lasts 90 days from pairing and is not extended by use, so after 90 days the desktop has to be paired again.

## Network and HTTPS

Passwords, codes and device tokens travel between the browser or desktop and the server, so anything reachable from another computer needs HTTPS. The server refuses to start with an `http://` address in `SESAME_PUBLIC_URL` unless the host is `localhost`, `127.0.0.1` or `::1`.

The server does not handle TLS itself. It listens on a local port, and a reverse proxy or Tailscale provides the HTTPS address. Three settings have to match that setup.

`SESAME_PUBLIC_URL` is the exact address people open, such as `https://sesame.example.net`. Pairing links are built from it. Every request must arrive with a `Host` header for that host, or it gets 421 `host_mismatch`, so the proxy has to pass the original `Host` header. The server never reads `X-Forwarded-Host`. All the recipes below pass `Host` correctly.

`SESAME_TRUSTED_PROXIES` lists the proxy's addresses as comma separated CIDR ranges in loopback, private or link-local space. The server takes the visitor address from `X-Forwarded-For` only on connections from these ranges and uses it for rate limits. Leave it empty when nothing sits in front of the server. If it is missing behind a proxy, the log warns that every visitor shares one rate limit.

`SESAME_ADDR` is where the server listens. The default is `127.0.0.1:8787`. The image listens on `0.0.0.0:8787` inside the container, and the compose file publishes that on the host loopback only, so only a proxy on the same host can reach it. Keep it that way.

Each recipe below has its configuration file in [deploy/selfhost](deploy/selfhost).

### Tailscale

Tailscale is the simplest choice when every device that pairs is on your tailnet. It gives the server an HTTPS address with no domain and no open port. Turn on MagicDNS and HTTPS certificates in the Tailscale admin console first, then run:

```sh
tailscale serve --bg --https=443 http://127.0.0.1:8787
tailscale serve status
```

Set `SESAME_PUBLIC_URL` in `.env` to the address that `tailscale serve status` prints, such as `https://sesame.tailnet-name.ts.net`, set `SESAME_TRUSTED_PROXIES=172.31.88.0/24` and run `docker compose up -d` again. Use `tailscale serve` and not `tailscale funnel`, because funnel opens the server to the whole internet.

### Caddy on a domain

Point the domain at the machine and set `SESAME_PUBLIC_URL=https://sesame.example.net` in `.env`. Use the [Caddyfile](deploy/selfhost/Caddyfile) and set `SESAME_HOST=sesame.example.net` in the environment of the Caddy service, for example with `systemctl edit caddy`. Caddy gets the certificate and passes the `Host` header by itself.

When Caddy runs on the host and the server runs from the compose file, also set `SESAME_TRUSTED_PROXIES=172.31.88.0/24`. The compose file fixes its network to that range, and Docker normally shows traffic from the host as coming from that network's gateway. If the log still warns about `X-Forwarded-For`, run `docker network inspect sesame-selfhost_default` to see which range the proxy really uses.

### nginx on a domain

Copy [nginx.conf](deploy/selfhost/nginx.conf), replace `sesame.example.net` with your name and point the two certificate paths at your certificate. The file passes `Host` with `$http_host` so a port in the address survives, adds the visitor to `X-Forwarded-For` and limits request bodies to 64 KiB. Set `SESAME_TRUSTED_PROXIES` the same way as for Caddy.

### Traefik on a domain

[compose.traefik.yaml](deploy/selfhost/compose.traefik.yaml) is an override for a Traefik that already runs in Docker with a `websecure` entry point and a certificate resolver. It removes the published port, joins the Traefik network and adds the router labels.

```sh
export SESAME_HOST=sesame.example.net
export SESAME_PUBLIC_URL=https://sesame.example.net
export SESAME_TRUSTED_PROXIES=172.18.0.0/16
docker compose -f compose.yaml -f compose.traefik.yaml up -d
```

Replace the trusted range with your Traefik network's subnet, which `docker network inspect traefik` prints. Set `TRAEFIK_NETWORK` and `TRAEFIK_CERT_RESOLVER` if yours are not called `traefik` and `letsencrypt`.

### Binary under systemd

To run without Docker, copy the program out of the release image. It is the same static file the image runs. Run this in the directory that holds your `.env`, or set `SESAME_IMAGE_TAG` to the release you want:

```sh
SESAME_IMAGE_TAG=$(sed -n 's/^SESAME_IMAGE_TAG=//p' .env)
docker create --name sesame-extract "ghcr.io/usesesame/sesame-server-selfhost:${SESAME_IMAGE_TAG}"
docker cp sesame-extract:/sesame-server ./sesame-server
docker rm --volumes sesame-extract
sudo install -m 0755 sesame-server /usr/local/bin/sesame-server
```

Then create the service user and the settings file and install the unit:

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin sesame
sudo install -d -m 0750 -o root -g sesame /etc/sesame
sudo install -m 0640 -o root -g sesame deploy/selfhost/sesame-server.env.example /etc/sesame/sesame-server.env
sudo install -m 0644 deploy/selfhost/sesame-server.service /etc/systemd/system/sesame-server.service
sudoedit /etc/sesame/sesame-server.env
sudo systemctl enable --now sesame-server
sudo journalctl -u sesame-server | grep 'setup#token'
```

The unit runs as the `sesame` user with its data in `/var/lib/sesame`, no capabilities, a read-only view of the rest of the system and a system call filter. The example settings expect a proxy on the same machine, so they trust `127.0.0.1/32`.

To run a host command such as a backup against this install, load the same settings:

```sh
sudo -u sesame sh -c 'set -a; . /etc/sesame/sesame-server.env; exec /usr/local/bin/sesame-server backup'
```

The commands on the rest of this page are written for the compose install. For a binary install, run `/usr/local/bin/sesame-server` with the same arguments in the way shown above.

## Guided installation

Release 0.5.0 and later have an `install` command that writes a complete compose setup for one of three profiles, as an alternative to the quick start and the recipes above. An older binary, such as the 0.4.1 release, does not gain the command even if you give it a newer tag. Get the program as described in [Binary under systemd](#binary-under-systemd).

The command only generates files. It does not pull an image, start a container, configure Caddy or Tailscale, open the data directory, contact the network or test the host. `--version` is the image tag it writes into the files, and it does not look up the latest release, so set it to the release you want.

```sh
SESAME_IMAGE_TAG=0.5.0
sesame-server install --profile local --version "$SESAME_IMAGE_TAG" --dir ./sesame-local
sesame-server install --profile caddy --host vault.example.com --version "$SESAME_IMAGE_TAG" --dir ./sesame-caddy
sesame-server install --profile tailscale --host sesame.example-tailnet.ts.net --version "$SESAME_IMAGE_TAG" --dir ./sesame-tailscale
```

Run one of the three. The `local` profile serves `http://localhost:8787` and trusts no proxy. The `caddy` and `tailscale` profiles serve the HTTPS address you pass with `--host` and trust only `172.31.88.0/24`, the fixed range of the compose network. The `caddy` profile also writes the [Caddyfile](deploy/selfhost/Caddyfile) and a `caddy.env` that holds only `SESAME_HOST`. It accepts lowercase ASCII names whose final suffix contains only letters, so a name with an internationalized suffix beginning `xn--` needs the [manual Caddy recipe](#caddy-on-a-domain).

The target must be a new path whose parent directory already exists. The command refuses an existing file, directory or symbolic link, refuses a parent path that contains a symbolic link and does not create missing parent directories. On Linux and macOS it creates the directory with mode 0700 and each file with mode 0600. On Windows it does not set an access control list, so the directory inherits its parent's permissions. The plan records the intended modes, but Sesame does not enforce them on Windows, so restrict the parent directory first. Linux is the only runtime tested for this command, and Windows and macOS runtime support remains unvalidated.

The file `plan.json` is written last, so a directory without it is incomplete. A hard kill can leave an incomplete private directory behind that the command cannot remove, so remove it yourself before you use that path again.

Read the generated files and check the compose configuration with `docker compose config` before you start anything:

```sh
cd sesame-local
sed -n '1,200p' compose.yaml .env plan.json
docker compose config
```

`plan.json` lists the profile, image tag, public address, trusted ranges, file hashes and modes, and the prerequisites the command could not check. Add `--json` to the install command to print the same plan. The plan does not prove that the image exists, that port 8787 or the subnet is free, that DNS resolves or that a certificate can be issued.

For the `caddy` profile, start the server and run Caddy on the host with the generated environment file:

```sh
cd sesame-caddy
docker compose up -d
sudo caddy run --config ./Caddyfile --envfile ./caddy.env
```

If Caddy already runs as a service, install the generated Caddyfile where that service reads its configuration and add `EnvironmentFile=/absolute/path/to/sesame-caddy/caddy.env` to its unit. With systemd, run `sudo systemctl daemon-reload` before you restart it.

For the `tailscale` profile, turn on MagicDNS and HTTPS certificates in the Tailscale admin console first. These prerequisites remain operator work, because the command cannot check them. Then start the server and serve it inside your tailnet:

```sh
cd sesame-tailscale
docker compose up -d
sudo tailscale serve --bg --https=443 http://127.0.0.1:8787
sudo tailscale serve status
```

Use `tailscale serve` and not `tailscale funnel`, and make sure the `--host` you passed matches the address that `tailscale serve status` prints.

The install command does not run the doctor below and does not validate the network.

## Diagnose an installation

Release 0.5.0 and later include `doctor`, which inspects an installation offline and reports invalid settings, unexpected file types, missing instance files and permissions that are too broad. It does not start or change the service. To check an older release, use a 0.5.0 or later program as the candidate build and keep following your release's own guide for everything else.

Run it as the same account that runs the service, with the same settings:

```sh
SESAME_DATA_DIR=/var/lib/sesame \
SESAME_CONFIG_FILE=/var/lib/sesame/config.json \
/usr/local/bin/sesame-server doctor --json
```

Put any other settings your service uses, such as `SESAME_PUBLIC_URL`, `SESAME_ADDR` and `SESAME_TRUSTED_PROXIES`, in front of the command as `NAME=value`. Do not source or evaluate a systemd `EnvironmentFile` or a generated `.env` as a shell script, because their syntax is not shell syntax and a shell can read the wrong values. A new process also cannot prove that its environment matches the running service.

For a running container, run it inside the service:

```sh
docker compose exec -T sesame /sesame-server doctor --json
```

This shares the container environment and the mounted data with the running server. It rereads `config.json`, though, so an edited file can differ from the settings cached by the running server. When the service is stopped, use a one-off run instead. A one-off run can create Docker resources, so it is not a zero-mutation observation.

```sh
docker compose run --rm --no-deps -T sesame doctor --json
```

The doctor checks configuration loading, the public address, the listen address, trusted proxies, the config file, the data directory, the secrets files, the database file and free space. It reads file metadata only. It does not open SQLite or read the contents of the secrets files, it makes no network request and it creates nothing. It does not repair, migrate, seed, checkpoint or recover anything. The report names your public address and trusted proxy ranges, so keep it as private as your settings.

The result is one of three states. `ok` means every check that ran found no problem, `action` means something needs a change and `unknown` means evidence is missing. The exit is 0 for `ok`, 1 for `action`, 3 for `unknown` and 2 for a usage error. An `unknown` result is not a pass. The offline checks always leave the database unknown, so a report with nothing to fix still exits 3. `--network` is not available.

Even `ok` covers only those checks. The doctor does not prove the database schema, integrity, audit chain or recovery state, the secret contents or their match to the database, or network reachability, DNS, certificates, proxy mapping, ACLs, write access, clock accuracy or the settings a running service has cached. A report is not a statement that the service is healthy or secure.

Any group or other permission bit on `config.json` counts as an action, because the file can still hold an old mail password that the environment overrides. Without `--json` the doctor prints a short summary, and with it one JSON object that holds no absolute path, key or database content.

Linux is the only runtime tested. Windows and macOS compile, but checks that depend on POSIX permission bits stay unknown there, and the doctor does not inspect an access control list on any platform.

### Check the running server in the console

The System page in the console shows how the running server sees its public address, proxy trust, audit log, latest backup and forwarded requests, with a next step for anything that needs one. It uses the settings the server already loaded and makes no network probe, so a configured HTTPS address does not prove that the certificate works, and a recorded backup does not prove that an off-host copy exists or that a restore works.

## Settings

Each setting comes from an environment variable, then from the config file, then from the default. The config file is `config.json` in the data directory, or the file named by `SESAME_CONFIG_FILE`, and it accepts only the keys in the last column.

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

`SESAME_METRICS=true` adds a Prometheus endpoint at `/metrics` that answers only direct requests from loopback or private addresses. `SESAME_BACKUP_INTERVAL=0` turns off scheduled backups, and the shortest interval is one minute. The update settings are explained in [Update notices](#update-notices). The server logs every effective setting and where it came from when it starts, and it exits with a message when a setting is invalid.

The server also accepts and checks `SESAME_SMTP_*` settings, but it does not send any email yet.

## Backup, restore, upgrade and reset

All state is in the data directory: the database `sesame.db`, the `secrets` directory and the optional `config.json`.

Keep your backup files in `backups/` or outside the data directory. Files named `.restore-input-` followed only by digits and directories named `.restore-new-` followed by exactly 12 lowercase hexadecimal characters are reserved for temporary restore data, and the server removes abandoned ones.

### Back up

The server writes a backup to `backups/` in the data directory every 24 hours and keeps the newest seven. Each backup is one tar file with a consistent copy of the database, the three secret files and their checksums. You can also take one at any time while the server runs:

```sh
docker compose exec sesame /sesame-server backup
docker compose exec sesame /sesame-server backup /data/backups/before-trip.tar
```

Copy backups to another machine, because the scheduled ones sit on the same disk as the data. A backup holds the keys that unlock owner sign-in, so keep it as private as the server itself.

To check a backup without restoring it:

```sh
docker compose exec sesame /sesame-server check /data/backups/before-trip.tar
```

`check` without a file name checks the live database, including the audit chain. `export` writes the members, devices and audit log as JSON without tokens, secrets or password data.

### Encrypted backup export

An encrypted export makes a copy of a backup that only your recovery device can open, which is safer to store elsewhere. It uses [age 1.3.2](https://github.com/FiloSottile/age/releases/tag/v1.3.2). The server only needs your public recipient, and the private identity stays on the recovery device. Install age on the machine that runs the commands, and run `scripts/export-backup.mjs` from a source checkout of the release tag that matches your server. A server that predates the encrypted export flags stops with a usage error, so upgrade it first. The scheduled local backups still run unchanged next to the exports.

Create the identity on the recovery device and copy only its public recipient to the server:

```sh
age-keygen -o /media/recovery/identity.txt
age-keygen -y /media/recovery/identity.txt > /etc/sesame/recipients.txt
chmod 0600 /etc/sesame/recipients.txt
```

For a binary install, run the export against the program and its data directory:

```sh
node scripts/export-backup.mjs export \
  --native /usr/local/bin/sesame-server --data-dir /var/lib/sesame \
  --destination /mnt/sesame-exports --recipients-file /etc/sesame/recipients.txt
```

For the compose install, run it next to your compose file and name the service:

```sh
node /opt/sesame/scripts/export-backup.mjs export \
  --compose-service sesame --destination /mnt/sesame-exports \
  --recipients-file /etc/sesame/recipients.txt
```

The export reads only the recipient file and never accepts or asks for a private identity. It uses `docker compose exec`. If the service is stopped, add `--stopped` so it uses a one-off container, and add `--compose-file` when your file is not `compose.yaml` in the current directory.

Each export writes a new `sesame-...tar.age` file with mode 0600 and never overwrites an earlier one. The database can be up to 4 GiB and the config up to 1 MiB, and the export temporarily needs room for the database and the finished archive. It prints the sizes, digests and path of the file it wrote.

An export on the same machine or disk is not off-host protection, so copy the file somewhere else with the tools you already use. Sesame does not schedule exports and promises no recovery point objective or restoration time.

Check the copy on the recovery device, which holds the identity:

```sh
node scripts/export-backup.mjs verify \
  --archive /mnt/sesame-exports/sesame-example.tar.age \
  --identity-file /media/recovery/identity.txt --native /usr/local/bin/sesame-server
```

Verification passes only when age decrypts the whole file and the archive check succeeds. To use a release image instead of a local program:

```sh
node scripts/export-backup.mjs verify \
  --archive /mnt/sesame-exports/sesame-example.tar.age \
  --identity-file /media/recovery/identity.txt \
  --image "ghcr.io/usesesame/sesame-server-selfhost:${SESAME_IMAGE_TAG:?set a compatible server version}"
```

To practise a recovery without touching the live data, drill the archive into a throwaway target:

```sh
node scripts/export-backup.mjs drill \
  --archive /mnt/sesame-exports/sesame-example.tar.age \
  --identity-file /media/recovery/identity.txt --native /usr/local/bin/sesame-server
```

The drill decrypts, restores into a new target, checks the database and reads the recovery status, then deletes the target. The restored state is left pending, because the drill does not approve recovery, retain a device, start a network service or ask for a device token. With `--image` it uses a fresh volume and never touches the live one. To keep a restored copy, use the restore steps below.

Never put the identity in the data directory, the export destination or the server image. Use `--state-dir` to choose where the tool keeps its private history, `--age` to name the age program and `--work-dir` to choose where verification and drills work. If the tool reports `cleanup_failed`, a child process may still hold temporary files, so make sure it has exited before you remove them.

### Restore

Stop the server before you restore. The restore moves the current data aside into a `restore-previous-` directory and installs the backup, and it refuses an archive whose checksums fail or whose secrets do not match its database.

```sh
docker compose stop sesame
docker compose run --rm --no-deps sesame restore /data/backups/before-trip.tar
```

After a restore, the server refuses to start until you approve the restore on the host and choose which owners and devices keep access. This stops a device revoked after the backup, or an owner removed after it, from coming back. Continue with [Approve a restore](#approve-a-restore). A restore interrupted by a crash finishes by itself the next time the server or a restore starts.

### Approve a restore

First look at what the backup contains. Status lists the pending generation, the active owners and every device in the archive, including devices that were already revoked:

```sh
docker compose run --rm --no-deps sesame recover --status
```

The JSON form gives exact `id` values for each owner and device, and it holds no tokens, codes, passwords or authenticator secrets:

```sh
docker compose run --rm --no-deps sesame recover --status --json
```

Set `SESAME_RECOVERY_GENERATION`, `SESAME_RETAIN_OWNER_ID` and `SESAME_RETAIN_DEVICE_ID` from the status output and approve. Repeat `--retain-owner` and `--retain-device` to keep more than one, and leave out `--retain-device` when no device should keep access.

```sh
docker compose run --rm --no-deps sesame recover --json \
  --confirm "$SESAME_RECOVERY_GENERATION" \
  --retain-owner "$SESAME_RETAIN_OWNER_ID" \
  --retain-device "$SESAME_RETAIN_DEVICE_ID"
```

Every owner you do not name is removed and every device you do not name is revoked, and there is no option that keeps everything. You must keep at least one complete owner when the backup has one. A kept device must belong to a kept owner or to a member, and an expired or already revoked device cannot be kept.

Keep only devices you still hold, because a kept device goes on using its token from the backup. A kept owner signs in with the password and authenticator from the backup. If those may be compromised, reset that owner with `docker compose run --rm --no-deps sesame owner reset "owner name"` before you start the server, and pair that owner's devices again afterwards.

Then start the server:

```sh
docker compose up -d
```

Every owner session, setup link and unused pairing from before the restore is gone. If you restore again, status shows a new generation and the old one is refused. A release that predates recovery approval installs a backup directly and has no `recover` command, so follow that release's own `SELF-HOSTING.md`.

### Restore a backup outside the data volume

For a backup that is not in the data volume, such as one copied from another machine, pipe it into the restore on standard input. Keep the file at mode 0600. The `-` tells `restore` to read standard input and `-T` makes sure the archive arrives unchanged. The server needs room in its data volume for a temporary copy as well as the extracted database.

```sh
chmod 0600 before-trip.tar
docker compose stop sesame
docker compose run --rm --no-deps -T sesame restore - < before-trip.tar
```

Then approve it as described in [Approve a restore](#approve-a-restore) before you start the server.

### Upgrade

Take a manual backup and read the release notes. Then change `SESAME_IMAGE_TAG` in `.env` and run:

```sh
docker compose pull
docker compose up -d
```

When a release changes the database schema, the server first writes a backup named `sesame-premigration-` in `backups/` and then migrates. An older release refuses a database that a newer one has migrated, so to go back, restore the pre-migration backup with the older image. An image that predates recovery approval does not ask for approval after that restore.

### Reset an owner

If an owner loses their password or authenticator, run this on the host:

```sh
docker compose exec sesame /sesame-server owner reset "owner name"
```

It prints a new setup link for that owner, which works once for 24 hours. It also ends their sessions, revokes the devices paired to them and cancels their unused pairing links, so they have to pair those devices again. Members' devices stay linked. This only works on the host, because it needs the data directory and its secrets.

## Sync

Sync has not had an independent security review, so keep backups while you use it.

To turn it on, open Settings in the console, confirm your password and code and switch on Sync. Each person then decides on their own desktop whether to use it. While Sync is off, the server refuses every Sync request.

The server learns how many devices a person has, when they sync and how large their encrypted vault is. It never sees a vault key, a password, a recovery kit or a device name, because device names are encrypted with the vault key.

The server can refuse or delay service, or serve an older copy. A desktop notices an older copy because it remembers the newest version it applied, and it notices a split history because every version names the one before it and every change to the device list is signed by a device already on it.

A new device joins with a one-time code shown on a device that already syncs. The two devices run a password-based key exchange through the server, which never learns the code. So an owner who creates pairing links for a member still cannot add a device to that member's vault.

The console shows, for each person, whether a synced vault exists, how many devices it has, its latest version and its size. An owner can reset a person's synced vault, which deletes the stored ciphertext and history and is written to the audit log. The console cannot approve a device, read a name or open a vault.

Backups include the encrypted Sync data. If a kept desktop has already applied a version newer than the restored backup, it refuses the older history and shows a warning. In that case reset that person's vault and start Sync again from the desktop with the newest copy.

## Update notices

If you allow it, the server checks a signed list of releases about once a day and the Updates page tells you when a newer version exists, what changed and which commands to run. It never downloads or installs anything itself.

You choose at setup whether it checks, and you can change that on the Updates page at any time. Until you choose, it makes no request.

A check is one plain `GET` to `https://api.usesesame.app/v1/updates/selfhost.json`, or to `SESAME_UPDATE_FEED_URL` if you set it. It sends no instance ID, cookie or version, and its `User-Agent` is the same for every server. Like any web request, it shows the feed host your server's IP address and the time, and if the server's environment sets `HTTPS_PROXY`, that proxy sees the request too. The code that builds the request is in `internal/selfhost/updates/fetch.go`.

The server accepts the list only when its Ed25519 signature matches a pinned public key, it has not expired and its sequence number is not lower than one it has accepted before. That signature is the only thing it verifies. It does not download or check the image itself, but the Updates page shows the digest from the signed list so you can compare it with what you pulled. If the list cannot be used, the page keeps the last good result and shows one of these codes:

| Code | Meaning |
| --- | --- |
| `feed_unreachable` | The request failed, did not answer 200 or redirected to another address. |
| `feed_invalid` | The signature, the key, the document's shape or its size was wrong. |
| `feed_rollback` | The list is older than one the server already accepted. |
| `feed_expired` | The list has expired, so the page hides the upgrade commands until a fresh one arrives. |
| `not_configured` | This build has no public key, so the server makes no request. |

If a maintainer published a sequence number that was too high by mistake, `docker compose exec sesame /sesame-server updates reset-sequence` makes the server forget the numbers it has seen and writes that to the audit log.

The server detects whether it runs in a container or as a binary and shows the matching upgrade commands. Set `SESAME_INSTALL_KIND` to `container` or `binary` if it guesses wrong. The official image checks for updates only once Sesame has published a signing key for the release you run, and until then the page shows `not_configured`. Maintainers who publish the list should read [deploy/selfhost/UPDATES.md](deploy/selfhost/UPDATES.md).

## What you give up without accounts

The hosted service has accounts with email addresses. A self-hosted server has owners and members instead, and that has consequences.

- Members cannot sign in, so they cannot see their own devices or revoke a lost laptop. An owner does that for them.
- Nobody gets an email when a device is paired. The audit log and the Devices page record it, but someone has to look.
- A signed-in owner session can read the console, and for the first 10 minutes it can also pair a device without asking again. After that, pairing, revoking and removing members ask for the password and a code. A session lasts up to 12 hours.
- Whoever controls the host controls the server. Anyone who can run commands as the server's user or read the data directory can reset an owner, approve a restore that keeps any owner or device, or copy the secrets and the database.
- Device tokens are not bound to hardware. A token copied from a desktop works from any machine until it is revoked or expires after 90 days.

Keep the owner list short, and check the Devices page now and then for devices you do not recognise.

## The audit log

Every change to the server is written to an audit log in the database. Each row includes a SHA-256 hash of the previous row, so a changed, removed or inserted row breaks the chain. The Audit log page and `sesame-server check` verify the chain and report the first row that fails.

The chain shows tampering but cannot prevent it. The hash uses no secret key, so anyone who can write the data directory can edit rows and recompute every later hash, and deleting the newest rows leaves a shorter chain that still checks out. To catch that, copy the head hash from the System page or from an `export` to a place the server's host cannot write.

## Secrets

The `secrets` directory holds three files that the server creates on first start.

| File | What it protects |
| --- | --- |
| `admin-encryption.key` | The owners' authenticator secrets in the database |
| `signing.key` | The server's identity, shown as the fingerprint in pairing links |
| `ip-pepper.key` | Visitor addresses in the rate-limit records |

Without `admin-encryption.key` no owner can sign in, and `owner reset` cannot help because it needs that key. The server will not create a new secret while `sesame.db` exists. It exits and asks you to restore the `secrets` directory from a backup. Without a copy, the only way out is a new data directory, and every device must be paired again.

Scheduled backups already include all three files. The usual mistake is copying `sesame.db` by hand without the `secrets` directory, and a restore refuses a database whose secrets do not match.

## Verify and build the image

Release images are published to `ghcr.io/usesesame/sesame-server-selfhost` for `linux/amd64` and `linux/arm64`, with build provenance and SBOM attestations. To verify an image with the GitHub CLI before you run it, go to the directory that holds your `.env`:

```sh
SESAME_IMAGE_TAG=$(sed -n 's/^SESAME_IMAGE_TAG=//p' .env)
gh attestation verify "oci://ghcr.io/usesesame/sesame-server-selfhost:${SESAME_IMAGE_TAG}" \
  --repo usesesame/sesame-server \
  --signer-workflow usesesame/sesame-server/.github/workflows/release.yml \
  --source-ref "refs/tags/v${SESAME_IMAGE_TAG}"
```

To build the image yourself from a checkout of the release tag:

```sh
docker build -f Dockerfile.selfhost -t sesame-selfhost:local .
```

The image runs as the unprivileged user 65532 with a `/data` volume owned by that user. If you mount a host directory at `/data` instead of the named volume, run `chown 65532:65532` on it first.

## When something goes wrong

| What you see | What to do |
| --- | --- |
| 421 `host_mismatch` | Open the exact address in `SESAME_PUBLIC_URL` and make the proxy pass the `Host` header unchanged. |
| 403 `origin_not_allowed` | Open the console at the address in `SESAME_PUBLIC_URL`. |
| 429 `too_many_attempts` | Sign-in allows 5 tries per minute for a name. Wait the number of seconds in `Retry-After`. |
| The server exits at start with a message about `SESAME_PUBLIC_URL` | The address must start with `https://` unless the host is `localhost`, `127.0.0.1` or `::1`. |
| `manifest unknown` or `not found` when pulling the image | `SESAME_IMAGE_TAG` names a version with no published image. Compare `cat .env` with the releases page. |
| No `setup#token` line in the log | An owner already exists. If you never created one, the link expired, so run `docker compose restart sesame` and read the log again. |
| `port is already allocated` | Another program uses port 8787. Change the left side of the `ports` line in the compose file and use the same port in `SESAME_PUBLIC_URL`. |
| The log says another server is using the data directory | Another process or a running restore holds the lock. Stop it first. |
| The log says the database was written by a newer server | Run the newer release again, or restore a backup made by the older one. |
| The log says the restored database is waiting for local recovery approval | Run `docker compose run --rm --no-deps sesame recover --status` and follow [Approve a restore](#approve-a-restore). |
| A usage error for `--stdout` or `--stdin` | The server predates encrypted export. Use `export-backup.mjs` from the same release tag, or upgrade. |
| The export cannot find age | Install age 1.3.2 on the machine that runs the command, or name it with `--age`. |

The HTTP routes are listed in [internal/selfhost/server/API.md](internal/selfhost/server/API.md). To report a security problem, follow [SECURITY.md](SECURITY.md).
