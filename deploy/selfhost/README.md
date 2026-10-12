# Self-hosted server recipes

These files run `sesame-server`. [SELF-HOSTING.md](../../SELF-HOSTING.md) explains each one.

- `compose.yaml` runs the released image with one volume and the port published on `127.0.0.1` only.
- `.env.example` lists the values the compose files read.
- `compose.traefik.yaml` is an override that puts the server behind a Traefik already running in Docker.
- `Caddyfile` is the Caddy site block for a real domain.
- `nginx.conf` is the nginx server block for a real domain with a Let's Encrypt certificate.
- `sesame-server.service` and `sesame-server.env.example` run the static binary under systemd as a dedicated user.
- Tailscale needs no file. The commands are in SELF-HOSTING.md.
