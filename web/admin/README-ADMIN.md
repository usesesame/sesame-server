# Sesame administration app

This document describes the current administration boundary. The Svelte portal
is in `web/admin`. Its handlers are in `internal/httpapi/admin_*.go`; its store
and authorization model are in `internal/admin`.

The administration app manages website accounts, support requests, runtime
controls, releases, product plans, and administrators. It cannot receive,
open, reset, identify, or decrypt a local vault.

## Run and validate

From `web/admin`:

```powershell
npm ci
$env:VITE_SESAME_API_URL='https://api.test.invalid'
npm run release:check
```

`npm run ci` runs design-token, lint, and type checks. `npm run release:check`
also builds the production bundle and security headers. There are no browser
specs yet, so `npm run test` prints a skip notice.

Start the complete server stack from the repository root:

```powershell
npm run setup
npm run compose:up
```

See [DEPLOYMENT.md](../../DEPLOYMENT.md) for deployment instructions.

## Boundary decisions

### Separate origin and session

The administration portal has its own configured origin. `/v1/admin/*`
accepts credentialed browser traffic only from that exact origin. Admin
sessions use a separate cookie and table from website-account sessions, a
separate CSRF token, and an eight-hour lifetime.

The normal deployment uses `admin.usesesame.app`. Serving it from the public
website origin removes the origin boundary enforced by the API.

### Password plus TOTP

Every administrator signs in with a password and TOTP. There is no public
administrator registration. `adminctl` creates the first super administrator
and prints a one-time setup link. The link reveals its enrollment secret once.
Setup must finish within 30 minutes. `adminctl reset` revokes sessions and
issues a new link.

`SESAME_ADMIN_ENCRYPTION_KEY` encrypts administrator TOTP secrets. Replacing
that key makes existing secrets unreadable, so deployments must back it up
separately from PostgreSQL.

### Authorization and audit

Authorization is enforced by `internal/admin/types.go`, not by hiding portal
navigation. The current roles are:

| Role | Additional authority beyond its own audit entries |
| --- | --- |
| `super` | Every administration permission, including account deletion and administrator management |
| `support` | Read and manage users and support requests |
| `ops` | Manage feature flags and release controls; read system state |
| `billing` | Read users and manage product plans |
| `readonly` | Read all admin data and the full audit log |

Every administrative mutation writes an allowlisted audit record in the same
database transaction. If the audit write fails, the change fails. Audit records
are append-only through the API. Only `super` and `readonly` can read or export
the full log. Other roles see their own entries.

Trusted proxy ranges are deployment configuration. The portal can read the
effective system configuration but exposes no route that changes which peers
may assert a client address.

### Vault-blind control plane

Admin request bodies use closed schemas and reject vault-shaped fields. User
views show website-account state, browser sessions, and desktop-link metadata.
Release artifacts enter through the authenticated candidate pipeline. The
portal controls rollout and publication; it does not create artifact evidence.

## Domain model

| Term | Meaning |
| --- | --- |
| Administrator | A staff identity with one role, password plus TOTP, and admin-only sessions |
| Website account | An optional service identity, not a vault identity |
| Desktop connection | A revocable device token linking one installed app to a website account |
| Feature flag | A runtime value, such as registration mode or an availability gate |
| Release candidate | A signed pipeline submission with immutable artifact evidence |
| Release record | An accepted candidate plus mutable publication, rollout, update, and kill-switch controls |
| Support request | A secret-filtered text conversation owned by a guest email or website account |
| Audit entry | An append-only record of an administrative mutation |

Support replies appear in the signed-in account portal. Email is queued only
for accounts that opted in. Delivery state is separate from portal visibility.
See [SUPPORT.md](../../SUPPORT.md) for the full support lifecycle.

## Route and schema references

The generated [OpenAPI inventory](../../openapi/openapi.json) lists routes,
authentication, availability, and handler ownership. [API.md](../../API.md)
documents the closed request and response contracts. From the repository root:

```powershell
npm run openapi:generate
npm run openapi:check
```

The portal has Overview, Support, Users, Feature flags, Releases, Product
plans, Administrators, Audit log, and System views. The API enforces every
permission independently of those role-filtered links.

## Explicit non-capabilities

The administration app cannot:

- receive or decrypt a vault, vault password, PIN, recovery kit, wrapping key,
  or saved TOTP seed;
- reset a local vault credential or impersonate a vault owner;
- add a device to a vault or enable Sync for a shipping client;
- manufacture updater, Sigstore, or Authenticode evidence;
- change trusted proxy ranges at runtime.
