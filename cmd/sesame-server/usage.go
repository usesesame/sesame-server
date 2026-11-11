package main

const usage = `Usage: sesame-server [command] [arguments]

Commands:
  serve                 Run the server. This is the default.
  install --profile local|caddy|tailscale --version <tag> --dir <path> [--host <name>] [--json]
                        Generate Compose files and a plan in a new private directory. Caddy and Tailscale need --host. Nothing is started.
  backup [--stdout] [path]
                        Write a backup of the database and secrets. With --stdout, write the archive to standard output.
  restore <file|->      Restore a backup into the data directory. Use - to read it from standard input.
  check [--json] [file]
                        Verify the database, or a backup file, opens and is intact.
  check --stdin [--json]
                        Verify a backup archive from standard input.
  recover [--status] [--json]
                        Show the recovery state after a restore.
  recover --confirm <generation> [--retain-owner <id>] [--retain-device <id>] [--json]
                        Approve a restored database and choose the owners and devices to keep. Keep at least one owner when the restore holds a complete owner.
  owner reset <name>    Print a one-time link that sets a new password and authenticator for an owner.
  updates reset-sequence  Forget the highest accepted update feed sequence for every signing key.
  export [path]         Write the members, devices and audit log as JSON.
  healthcheck           Exit 0 when the configured address answers /readyz.
  version               Print the version.

Settings come from environment variables, then the config file, then defaults.
SESAME_DATA_DIR defaults to /data and SESAME_CONFIG_FILE defaults to config.json inside it.
`
