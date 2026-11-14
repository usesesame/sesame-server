# Sesame server

This repository holds the server side of Sesame. The servers here never receive a vault password, an encryption key, a TOTP seed, a recovery kit or an unencrypted vault. When someone turns on Sync, the server stores their vault encrypted, together with the signed version history and device list that their devices use to check it.

It contains two separate servers that share no data:

- The hosted service at usesesame.app, with website accounts, beta invitations, download and release information, support requests and desktop linking. It is a Go API with an account portal in `web/account` and an administration portal in `web/admin`, and it runs on PostgreSQL. [DEPLOYMENT.md](DEPLOYMENT.md) covers production.
- The self-hosted server, `sesame-server`, which you run yourself to pair your desktops and optionally sync encrypted vaults. It is one program with SQLite and a browser console in `web/console`. To run one, follow [SELF-HOSTING.md](SELF-HOSTING.md).

## Run the hosted service locally

You need Node and Docker with the Compose plugin, and Go for the tests. From a fresh clone:

```bash
npm ci
npm run setup
npm run compose:up
```

`npm run setup` writes local secrets to `deploy/compose/.env`, which git ignores, and keeps existing values when you run it again. `npm run compose:up` starts PostgreSQL, applies the migrations and runs the API, both portals and a local mail inbox:

| Service | Address |
| --- | --- |
| Account portal | `http://localhost:4175` |
| Administration portal | `http://localhost:4174` |
| API | `http://localhost:8787` |
| Mail inbox | `http://localhost:8025` |

Create the first administrator, which prints a one-time setup link:

```bash
npm run admin:bootstrap -- bootstrap you@example.com
```

Registration is invite-only by default. You can change the registration mode or send invitations from the administration portal.

This stack is for development and runs over plain HTTP. To run the hosted service publicly, follow [DEPLOYMENT.md](DEPLOYMENT.md) instead. If you put the local stack behind TLS anyway, give `SESAME_API_ORIGIN` and each portal origin its HTTPS address in `deploy/compose/.env`, set `SESAME_SESSION_SECURE` and `SESAME_ADMIN_SESSION_SECURE` to `true`, and run `npm run compose:up` again so the portals are rebuilt with the new API address. The API refuses to start when an HTTPS origin has its flag off. Every API setting is listed in [.env.example](.env.example).

## Database migrations

The API does not migrate the database itself, and it exits when the schema is out of date, so several replicas never race to migrate. Compose runs the `migrate` service before it starts the API, and in production the release deploy tool uses the same service, as [DEPLOYMENT.md](DEPLOYMENT.md) describes.

## Tests

```bash
npm ci
npm run ci
```

This runs the contract checks, lint, the builds and the Go tests. The PostgreSQL tests run only when `SESAME_TEST_DATABASE_URL` points at a test database, and CI provides one. Each app under `web` has its own `npm ci`, `release:check` and `test`.

`npm run openapi:generate` rebuilds [openapi/openapi.json](openapi/openapi.json) from the API's routes, and `npm run openapi:check` fails when that file is stale. The request and response rules for each route are in [API.md](API.md).

## More

- [SECURITY.md](SECURITY.md) explains how to report a security problem.
- [SUPPORT.md](SUPPORT.md) describes the support service.
