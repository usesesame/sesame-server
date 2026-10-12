package main

const usage = `Usage: sesame-server [command] [arguments]

Commands:
  serve                 Run the server. This is the default.
  backup [path]         Write a backup of the database and secrets.
  restore <file>        Restore a backup into the data directory.
  check [file]          Verify the database, or a backup file, opens and is intact.
  owner reset <name>    Print a one-time link that sets a new password and authenticator for an owner.
  updates reset-sequence  Forget the highest accepted update feed sequence for every signing key.
  export [path]         Write the members, devices and audit log as JSON.
  healthcheck           Exit 0 when the configured address answers /readyz.
  version               Print the version.

Settings come from environment variables, then the config file, then defaults.
SESAME_DATA_DIR defaults to /data and SESAME_CONFIG_FILE defaults to config.json inside it.
`
