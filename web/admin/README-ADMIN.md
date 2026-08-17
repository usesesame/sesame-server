# Sesame administration app

This document records shipped behaviour. All of the following are implemented:
the separate frontend, admin-only authentication and sessions, CLI bootstrap,
role checks, user actions, feature flags, releases, plans, administrator
management, system views, vault-shaped body rejection, and the transactional
audit log.

Two parts are deliberately left un-automated. Trusted proxy ranges stay
deployment-controlled, since changing which network peers may assert a client
address needs a controlled restart. Administrator invitations return a
one-time setup URL for a super administrator to hand over through a trusted
channel rather than being emailed.

A separate web application for staff to manage accounts, devices, releases, feature flags, and product configuration. It runs on the same Go API, yet uses a separate auth boundary, role system, and audit log. It never receives vault data.

During the Phase 1 repository boundary work, the `admin/` subtree is
independently buildable. It owns its package lock, Node version, commands, lint
and TypeScript configuration, design-token snapshot, browser tests, and future
CI entry point. Root commands are wrappers around those owned commands. Copying
the exact subtree into an empty directory and running `npm ci` followed by
`npm --prefix . run ci` needs no desktop, website, extension, root design file,
or root package metadata. This packaging change does not alter admin authentication,
authorization, audit, CSP, CORS, or separate-origin deployment.

## Architecture

### Separate app, shared API

```
admin.usesesame.app  (or usesesame.app/admin)
         |
         | HTTPS + admin session cookie + CSRF
         v
  Go API (/v1/admin/*)
  - Separate admin auth, not shared with user accounts
  - Separate session table, shorter TTL (8h)
  - MFA required (TOTP or WebAuthn)
  - All actions audited
  - Vault-blind: same prohibited-data rules as the public API
         |
         v
  PostgreSQL (shared with public API)
  - New tables: sesame_admin_accounts, sesame_admin_sessions,
    sesame_admin_audit_log, sesame_feature_flags
  - Existing tables: sesame_accounts (read + limited write),
    sesame_desktop_connections (read + revoke)
```

### Why a separate app, not a route in the marketing website

The website (`website/`) is a public marketing surface with no privileged access. Folding admin capabilities into it would enlarge the attack surface of a public-facing SPA. A separate `admin/` frontend keeps the blast radius small, and gives the admin app its own CSP, its own deploy cadence, and its own auth flow that never touches the website's cookie domain.

### Monorepo layout

```
admin/                    New Svelte SPA (same stack as website/)
  src/
  public/
  package.json
  vite.config.ts
backend/
  internal/
    admin/                New Go package: admin store, roles, audit
      admin.go
      audit.go
      roles.go
      store.go
      migrations/
        0006_admin_dashboard.sql
    httpapi/
      admin_routes.go     New file: all /v1/admin/* handlers
      admin_auth.go       New file: admin session middleware
```

The admin frontend builds to static files and deploys to its own origin (`admin.usesesame.app`) or a subpath (`usesesame.app/admin`). It calls the same Go API, though only the `/v1/admin/*` routes.

## Roles and permissions

Five roles exist. A super admin assigns roles. Every role can read its own audit log entries.

| Capability | Super Admin | Support | Operations | Billing | Read-only |
|---|---|---|---|---|---|
| View dashboard overview | yes | yes | yes | yes | yes |
| List/search users | yes | yes | limited | limited | yes |
| View user detail (email, devices, sessions, beta access) | yes | yes | limited | limited | yes |
| Grant/revoke beta access | yes | yes | no | no | no |
| Suspend/unsuspend account | yes | yes | no | no | no |
| Delete account | yes | no | no | no | no |
| Revoke user sessions | yes | yes | no | no | no |
| Revoke user devices | yes | yes | no | no | no |
| View audit log (all) | yes | own actions only | own actions only | own actions only | yes |
| Manage feature flags | yes | no | yes | no | no |
| Manage releases | yes | no | yes | no | no |
| Manage product plans/pricing | yes | no | no | yes | no |
| Manage trusted proxies / system config | yes | no | yes | no | no |
| Manage admin accounts (invite, assign roles, revoke) | yes | no | no | no | no |
| View rate-limit / health metrics | yes | yes | yes | no | yes |

### Role definitions

**Super Admin.** Full access. Only this role can invite other admins, assign roles, and delete user accounts. Should be limited to 2-3 people.

**Support.** Day-to-day account assistance. Can search users, view details, grant/revoke beta access, suspend accounts, and revoke sessions and devices. Cannot delete accounts or touch product config. The audit log shows only their own actions.

**Operations.** Manages the product surface: feature flags, release metadata, trusted proxies, rate-limit config. Cannot touch user accounts or billing.

**Billing.** Manages plans, pricing, and (when Stripe is live) refund/licence operations. Cannot touch users or system config.

**Read-only.** Auditor or observer. Can view everything but change nothing. Useful for stakeholders or external review.

## Database schema

New migration `0006_admin_dashboard.sql`:

```sql
-- Admin accounts. Separate from sesame_accounts (user accounts).
-- An admin account is created by a super admin; there is no public
-- registration.
CREATE TABLE IF NOT EXISTS sesame_admin_accounts (
    id            TEXT PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL CHECK (role IN ('super', 'support', 'ops', 'billing', 'readonly')),
    totp_secret   BYTEA,                    -- encrypted at rest, NULL until MFA is set up
    totp_verified BOOLEAN NOT NULL DEFAULT FALSE,
    suspended     BOOLEAN NOT NULL DEFAULT FALSE,
    created_by    TEXT REFERENCES sesame_admin_accounts(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_login_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS sesame_admin_accounts_role_idx
    ON sesame_admin_accounts(role);

-- Admin sessions. Short TTL (8h), HttpOnly + Secure + SameSite=Strict.
-- Token stored as SHA-256 hash only.
CREATE TABLE IF NOT EXISTS sesame_admin_sessions (
    token_hash   BYTEA PRIMARY KEY,
    admin_id     TEXT NOT NULL REFERENCES sesame_admin_accounts(id) ON DELETE CASCADE,
    ip_hash      TEXT,                       -- SHA-256 of IP for audit binding without retention
    user_agent   TEXT,                       -- truncated to 200 chars
    expires_at   TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sesame_admin_sessions_expiry_idx
    ON sesame_admin_sessions(expires_at);

-- Audit log. Append-only. Every admin action gets one row.
-- No user vault data is ever stored here.
CREATE TABLE IF NOT EXISTS sesame_admin_audit_log (
    id            BIGSERIAL PRIMARY KEY,
    admin_id      TEXT NOT NULL REFERENCES sesame_admin_accounts(id),
    admin_email   TEXT NOT NULL,             -- snapshot for readability after admin deletion
    action        TEXT NOT NULL,             -- e.g. "user.suspend", "flag.update", "release.publish"
    target_type   TEXT NOT NULL,             -- "account", "device", "flag", "release", "admin", "plan"
    target_id     TEXT,                      -- the affected record's ID
    detail        JSONB NOT NULL DEFAULT '{}',-- structured, allowlisted fields only
    ip_hash       TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sesame_admin_audit_log_admin_idx
    ON sesame_admin_audit_log(admin_id, created_at DESC);
CREATE INDEX IF NOT EXISTS sesame_admin_audit_log_action_idx
    ON sesame_admin_audit_log(action, created_at DESC);
CREATE INDEX IF NOT EXISTS sesame_admin_audit_log_target_idx
    ON sesame_admin_audit_log(target_type, target_id);

-- Feature flags. Runtime toggles read by the Go API on each request.
CREATE TABLE IF NOT EXISTS sesame_feature_flags (
    key         TEXT PRIMARY KEY,             -- e.g. "registration_mode", "sync_enabled", "public_download"
    value       TEXT NOT NULL,                -- stored as string; API parses per flag
    updated_by  TEXT REFERENCES sesame_admin_accounts(id),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed the flags that currently live as env vars or constants,
-- so they become runtime-configurable from the dashboard.
INSERT INTO sesame_feature_flags (key, value) VALUES
    ('registration_mode', 'invite'),
    ('cloud_sync_available', 'false'),
    ('public_download', 'false')
ON CONFLICT (key) DO NOTHING;
```

### Column additions to existing tables

```sql
-- Allow support to suspend user accounts without deleting them.
ALTER TABLE sesame_accounts
    ADD COLUMN IF NOT EXISTS suspended_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS suspended_reason TEXT;

-- Track which admin granted beta access, for audit.
ALTER TABLE sesame_accounts
    ADD COLUMN IF NOT EXISTS beta_granted_by TEXT REFERENCES sesame_admin_accounts(id),
    ADD COLUMN IF NOT EXISTS beta_granted_at TIMESTAMPTZ;
```

## API endpoints

All under `/v1/admin/*`. All require a valid admin session cookie + CSRF token. All mutations are audited. All reject vault-shaped request bodies (same `DisallowUnknownFields` + body-size limits as the public API).

### Admin auth

| Method | Path | Role | Description |
|---|---|---|---|
| POST | `/v1/admin/auth/login` | public | Email + password + TOTP code. Issues admin session cookie. |
| POST | `/v1/admin/auth/logout` | any | Revokes current session. |
| GET | `/v1/admin/auth/me` | any | Returns admin email, role, MFA status. |
| POST | `/v1/admin/auth/mfa/setup` | any | Generates TOTP secret, returns QR URI. |
| POST | `/v1/admin/auth/mfa/verify` | any | Verifies TOTP code, marks MFA active. |

### Admin management (super admin only)

| Method | Path | Role | Description |
|---|---|---|---|
| GET | `/v1/admin/admins` | super | List admin accounts. |
| POST | `/v1/admin/admins` | super | Invite a new admin (email + role). Sends setup link. |
| PATCH | `/v1/admin/admins/{id}` | super | Change role or suspend/unsuspend. |
| DELETE | `/v1/admin/admins/{id}` | super | Remove admin account. |

### User management

| Method | Path | Role | Description |
|---|---|---|---|
| GET | `/v1/admin/users?query=&page=` | super, support, billing, readonly | Paginated list. Search by email or ID. |
| GET | `/v1/admin/users/{id}` | super, support, billing, readonly | Full detail: email, verified, beta, suspended, sessions, devices, created. |
| POST | `/v1/admin/users/{id}/beta` | super, support | Grant beta access. |
| DELETE | `/v1/admin/users/{id}/beta` | super, support | Revoke beta access. |
| POST | `/v1/admin/users/{id}/suspend` | super, support | Suspend account (sets `suspended_at`, blocks login). |
| DELETE | `/v1/admin/users/{id}/suspend` | super, support | Unsuspend account. |
| DELETE | `/v1/admin/users/{id}` | super | Delete account (cascade). |
| DELETE | `/v1/admin/users/{id}/sessions` | super, support | Revoke all user sessions. |
| DELETE | `/v1/admin/users/{id}/devices/{deviceId}` | super, support | Revoke a connected device. |

### Feature flags

| Method | Path | Role | Description |
|---|---|---|---|
| GET | `/v1/admin/flags` | super, ops, readonly | List all flags. |
| PATCH | `/v1/admin/flags/{key}` | super, ops | Update a flag value. |

### Releases

| Method | Path | Role | Description |
|---|---|---|---|
| GET | `/v1/admin/releases` | super, ops, readonly | List all release records. |
| PUT | `/v1/admin/releases/{platform}` | super, ops | Update release metadata (version, URL, SHA256, available, signed, message). |

### Product plans

| Method | Path | Role | Description |
|---|---|---|---|
| GET | `/v1/admin/plans` | super, billing, readonly | List all plans. |
| PATCH | `/v1/admin/plans/{id}` | super, billing | Update plan fields (name, price, description, available, includes). |

### System

| Method | Path | Role | Description |
|---|---|---|---|
| GET | `/v1/admin/system/health` | super, ops, readonly | API version, DB status, uptime, active session count. |
| GET | `/v1/admin/system/rate-limits` | super, ops, readonly | Recent rate-limit activity (aggregated counts, no IPs). |
| GET | `/v1/admin/system/config` | super, ops, readonly | Trusted proxies, registration mode, session TTL, current flag overrides. |
| PATCH | `/v1/admin/system/config` | super, ops | Update trusted proxies, registration mode. |

### Audit log

| Method | Path | Role | Description |
|---|---|---|---|
| GET | `/v1/admin/audit?admin=&action=&from=&to=&page=` | super, readonly | Full audit log, filterable. |
| GET | `/v1/admin/audit/me` | any | Own actions only. |
| GET | `/v1/admin/audit/export` | super, readonly | CSV export of filtered log. |

## Audit log actions

Every mutation writes one audit row. Actions follow a `noun.verb` pattern:

| Action | Target type | Target ID | Example detail |
|---|---|---|---|
| `user.beta.grant` | account | user ID | `{"email": "...", "reason": "beta tester"}` |
| `user.beta.revoke` | account | user ID | `{"email": "..."}` |
| `user.suspend` | account | user ID | `{"reason": "abuse"}` |
| `user.unsuspend` | account | user ID | `{}` |
| `user.delete` | account | user ID | `{"email": "..."}` |
| `user.sessions.revoke` | account | user ID | `{"count": 3}` |
| `user.device.revoke` | device | device ID | `{"account": "...", "deviceName": "..."}` |
| `flag.update` | flag | flag key | `{"from": "invite", "to": "public"}` |
| `release.publish` | release | platform | `{"version": "0.2.0", "signed": true}` |
| `plan.update` | plan | plan ID | `{"field": "price", "from": "15", "to": "19"}` |
| `config.update` | system | config key | `{"field": "trusted_proxies", "from": [...], "to": [...]}` |
| `admin.create` | admin | admin ID | `{"email": "...", "role": "support"}` |
| `admin.role.change` | admin | admin ID | `{"from": "support", "to": "ops"}` |
| `admin.suspend` | admin | admin ID | `{}` |
| `admin.delete` | admin | admin ID | `{"email": "..."}` |

Detail fields are allowlisted. No free text beyond a short `reason` string (max 200 chars, ASCII only).

## Frontend structure

```
admin/src/
  main.ts
  App.svelte
  app.css                      (shared tokens from website/ or its own)
  lib/
    api.ts                     (fetch wrapper, CSRF, admin session)
    auth.ts                    (login, MFA, session store)
    types.ts
    stores/
      session.ts               (admin session state)
  routes/
    Login.svelte               (email + password + TOTP)
    MfaSetup.svelte            (QR + verification)
    Dashboard.svelte           (overview: user count, recent signups, flagged accounts, system health)
    Users.svelte               (searchable table)
    UserDetail.svelte          (profile, sessions, devices, beta, suspend/delete actions)
    Flags.svelte               (feature flag toggles)
    Releases.svelte            (release metadata editor)
    Plans.svelte               (product plan editor)
    AuditLog.svelte            (filterable table + CSV export)
    Admins.svelte              (admin list, invite, role assignment)
    System.svelte              (health, rate limits, config)
    Settings.svelte            (own admin account, MFA, password change)
```

### Routing

Simple client-side routing (same `window.location.pathname` approach as the website). A sidebar with role-filtered navigation: a billing admin never sees the Flags or Releases links.

### Dashboard overview

The landing page after login. It shows:
- Total user count, new signups this week
- Active beta testers, pending email verifications
- Suspended accounts count
- Active admin sessions
- System health (API up, DB up, version)
- Recent audit entries (last 10 actions)
- Current feature flag state (registration mode, sync, public download)

## Security model

### Auth boundary
- Admin auth is completely separate from user auth. Different cookie name (`sesame_admin_session`), different session table, different middleware.
- Admin sessions last 8 hours, versus 30 days for user sessions. No "remember me" option.
- MFA (TOTP) is required before any action. An admin without MFA is redirected to setup on first login and can only set up MFA, nothing else.
- Admin login is rate-limited at 5 attempts per minute per email + IP.
- Suspended admin accounts cannot log in.

### Vault-blind
- The admin API follows the same vault-blind rules as the public API. No endpoint accepts or returns vault data, credentials, TOTP seeds, backup codes, or vault identifiers.
- User detail returns only account-level fields: email, email verified, beta access, suspended status, session count, device list (device ID + name + connected date + expiry). It never returns vault contents.
- Audit log detail fields are allowlisted and cannot contain user secrets.

### Network
- The admin app is served from `admin.usesesame.app` (separate origin) or `usesesame.app/admin` (subpath). The Go API only accepts admin requests from the configured admin origin.
- CSRF protection: double-submit token, same pattern as the public API.
- All admin routes are behind the admin session middleware. No admin route is public.
- Trusted proxy rules apply the same as the public API.

### Audit
- Every mutation writes an audit row before returning success. If the audit write fails, the action fails (fail-closed).
- The audit log is append-only. No endpoint deletes or edits rows.
- Super admins and read-only auditors can export the full log. Other roles see only their own actions.

### First admin bootstrap
- The first super admin is created via a CLI command, rather than via the API:
  ```
  go run ./cmd/adminctl bootstrap email@usesesame.app
  ```
- This generates a one-time setup link printed to stdout. The link expires after 1 hour. Opening the link consumes it: the enrollment secret is returned once, and finishing the password and MFA setup must happen within 30 minutes of that first open. After that window, or after setup completes, the link is spent and `adminctl reset` issues a fresh one.

## Implementation phases

### Phase A: auth + user read (minimum viable)
1. Migration `0006_admin_dashboard.sql`.
2. Admin store (`backend/internal/admin/store.go`).
3. Admin auth middleware + login/logout/me endpoints.
4. CLI bootstrap command for the first admin.
5. Admin frontend: login, MFA setup, dashboard overview, user list, user detail (read-only).
6. Audit logging for all actions.

**Exit gate:** a super admin can log in with MFA, search users, view user details, and every action is in the audit log.

### Phase B: user management
1. Suspend/unsuspend, beta grant/revoke, session revoke, device revoke endpoints.
2. Account deletion (super only).
3. Frontend actions on the user detail page.
4. Suspended account check in the public login endpoint.

**Exit gate:** support can suspend a user, revoke their sessions and devices, and the user cannot log in until unsuspended.

### Phase C: flags + releases + plans
1. Feature flag store + endpoints. Go API reads flags from DB instead of env vars for the configurable values.
2. Release metadata CRUD.
3. Plan CRUD.
4. Frontend: flags page, releases page, plans page.

**Exit gate:** an ops admin can flip `registration_mode` from `invite` to `public` without a redeploy, and the public API reflects the change immediately.

### Phase D: system + admin management
1. System health, rate-limit metrics, config editor.
2. Admin CRUD (super only): invite, role change, suspend, delete.
3. Frontend: system page, admins page.

**Exit gate:** a super admin can invite a support agent, who can log in and help users, and every action by both is in the audit log.

### Phase E: polish
1. CSV export of audit log.
2. Pagination on all list endpoints.
3. Keyboard navigation and a11y pass.
4. Dark mode (reuse tokens from the desktop app).
5. Rate-limit charts on the system page.

## What the admin dashboard does NOT do

- It cannot open, read, edit, or decrypt a user's vault.
- It cannot see vault contents, passwords, TOTP seeds, backup codes, or recovery details.
- It cannot send a user's vault to another device.
- It cannot create or revoke a user's master password or recovery kit.
- It cannot impersonate a user on the website.
- It does not have access to the desktop app's local files or session.

These are hard boundaries enforced by the vault-blind API design, and not only by policy.
