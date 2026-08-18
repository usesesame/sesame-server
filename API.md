# Sesame account API

This document is the complete closed request and response schema contract for
the Go API. The generated [OpenAPI inventory](./openapi/openapi.json) is the
source of truth for route and method enumeration, authentication and CSRF
classes, availability, and Go handler ownership. Regenerate it with
`npm run openapi:generate` from this directory; backend CI compares it byte for
byte.

The API is vault-blind. These contracts must never carry a vault file,
encrypted vault blob, vault password, TOTP seed, backup code, recovery note, or
encryption key.

Every website mutation requires the configured `Origin` plus a matching
`X-Sesame-CSRF` value obtained from `GET /v1/auth/csrf`. Browser sessions travel
in an HttpOnly, SameSite=Lax cookie; the separate CSRF cookie uses
SameSite=Strict. Those origin and CSRF checks are what protect unsafe requests.
Only account auth routes accept passwords, and those values are never logged.
JSON decoders reject unknown fields. Every response carries an `X-Request-ID`,
which support may be given, but it must never be combined with secrets or
email-action links.

Credential routes are budgeted twice: first by the client address the request
arrives from, then by the identity it targets. An address-keyed budget on its
own bounds one network peer rather than one account, so a caller spread across
many addresses was not bounded at all. Sign-in, reauthentication, password
change, and account deletion are additionally budgeted per account;
registration, password recovery, email verification, and email change are
additionally budgeted per email address, including the destination address of
an email change. The identity is peppered and hashed before it becomes a
limiter key. Exceeding a budget returns `429 too_many_attempts` with
`Retry-After`, except for password recovery, which keeps answering `202` so the
status code cannot reveal whether an address holds an account or recent
activity.

## Signed capability contract

`GET /v1/capabilities` returns an ETag-cacheable envelope that holds a base64url
JSON payload, an Ed25519 signature, and a key ID. The payload is
`{schemaVersion,minimumDesktopVersion,latestDesktopVersion,features,serviceStatus,expiresAt}`.
Website and desktop builds pin the matching public key and verify the raw
payload before enabling a capability. Missing, expired, malformed, or
unverifiable configuration fails closed for desktop linking, downloads,
updater use, and Sync. The API also enforces the linking and download flags.

Unauthenticated requests return `not_authenticated`; an invalid or expired
session cookie returns `session_expired`. Both use `401`. The distinct codes let
the website clear only the stale signed-in state.

## Health

- `GET /livez` → `200 {status,service,version}`. A lightweight liveness probe
  that only confirms the process is running.
- `GET /readyz` → `200 {status,service,version,accounts}` when the database is
  reachable, otherwise `503`. Load balancers and deployment systems use it.
- `GET /healthz` is a deprecated alias for `/readyz`.

## Public metadata

Read-only. These reject mutation methods and every request body without parsing
it.

- `GET /v1/plans` → the free app and the planned Sesame Sync subscription,
  including its optional `annualPrice`.
- `GET /v1/product/status` → current phase, platform, account, sign-in, sync,
  and download availability.
- `GET /v1/releases/latest?platform=windows` → release availability. Stays
  unavailable until verified Tauri updater and exact-workflow Sigstore evidence
  exist. Production additionally requires verified Authenticode evidence.
- `GET /v1/security/boundaries` → machine-readable confirmation that the API
  accepts and stores no vault data or credentials.
- `GET /v1/support` → public support availability and a safe-submission
  warning.

## Registration and email

- `GET /v1/auth/registration` →
  `{mode:"closed"|"invite"|"public",enabled,requiresInvite,emailDeliveryAvailable}`.
- `POST /v1/auth/register` with `{email,password,inviteCode?}` → `201`
  `{user,verificationQueued}` and a browser-session cookie. The server enforces
  the registration mode and consumes eligibility/invites transactionally.
  `verificationQueued` is `true` when the verification email has been written
  to the durable outbox; it does not mean the message has been accepted by the
  upstream SMTP relay yet.
- `POST /v1/auth/email/verification/request` with no body → `202`.
- `POST /v1/auth/email/verification/confirm` with `{token}` → `200 {user}`.
- `POST /v1/auth/password/recovery/request` with `{email}` → `202`. Missing,
  malformed, and known emails receive the same response shape.
- `POST /v1/auth/password/recovery/confirm` with `{token,newPassword}` →
  `200 {user,otherSessionsRevoked:true}` and a replacement session.
- `POST /v1/account/email/change/request` with `{newEmail}` → `202`.
- `POST /v1/account/email/change/confirm` with `{token}` →
  `200 {user,otherSessionsRevoked:true}` and a replacement session.

Verification tokens live for 24 hours. Recovery and email-change tokens live
for 30 minutes. Only SHA-256 token hashes are stored. Tokens are single-use;
creating another token for the same purpose invalidates the previous one.

Action emails contain links such as `/verify-email#token={token}`. The token
is in the URL fragment so it is never sent to the server in the request line,
never appears in access logs, and is not included in `Referer` headers.
Client-side code must read the fragment and submit the token in the POST body
of the confirmation endpoint.

`user` is `{id,email,emailVerified,betaAccess}`. Confirming an email change or
password recovery revokes every older browser session in the same transaction
that applies the account change.

## Recent authentication and browser sessions

Password login, passkey login, registration, recovery completion, and email
change completion mark a browser session as recently authenticated. The default
recent-auth window is ten minutes.

- `POST /v1/account/reauthenticate` with `{password}` → `204`.
- `GET /v1/account/sessions` → `{sessions:[Session]}`.
- `DELETE /v1/account/sessions` → `204` and revokes every website session.
- `DELETE /v1/account/sessions/{id}` → `204`.

`Session` is
`{id,label,createdAt,lastSeenAt,authenticatedAt,expiresAt,current}`. Session
labels are coarse browser/OS names; raw User-Agent strings and IP addresses are
not stored.

Recent authentication is required before password changes, passkey add/remove,
desktop-link creation/cancellation, connected-device revocation, email changes,
and browser-session revocation. A stale request receives
`403 recent_auth_required`; the website should reauthenticate and retry the
original operation once.

`POST /v1/account/password` accepts `{currentPassword,newPassword}`. The new
password, revocation of every old session, and creation of the replacement
current session are one database transaction.

## Passkeys

- `POST /v1/account/passkey/register/begin` and `.../finish` register a
  WebAuthn passkey for the signed-in account. Registration requires recent
  authentication.
- `POST /v1/auth/passkey/login/begin` and `.../finish` sign in with a
  discoverable passkey and mark the session recently authenticated.
- `GET /v1/account/passkeys` lists them; `DELETE /v1/account/passkeys?id=<hex>`
  removes one.

A passkey authenticates the website account only. It never unlocks,
identifies, or touches a local vault, and no vault material is part of any
ceremony.

## Account state and deletion

- `GET /v1/auth/me` → the signed-in account id and email only.
- `GET /v1/account/bootstrap` → the combined first-paint state the website
  needs, so a signed-in page load does not fan out into several requests.
- `GET /v1/account/activity` → `{events:[...]}`, the account's own 50 most
  recent security events.
- `GET /v1/account/notifications` → `{securityMandatory:true,preferences}`.
  `PATCH` the same path with `{betaReleases,supportReplies,productAnnouncements}`
  → `204`. Security mail cannot be switched off.
- `POST /v1/account/delete` with `{password}` → deletes the account. Requires
  recent authentication and re-verifies the password.

## Beta access, licences, and verified private-beta downloads

- `GET /v1/account/access` →
  `{betaAccess,emailVerified,downloadsAllowed,licences:[Licence]}`.
- `GET /v1/account/downloads` → `{releases:[Release]}`.
- `POST /v1/account/download-tickets` with
  `{releaseId,platform}` and a random `Idempotency-Key` header →
  `{downloadUrl,expiresAt,releaseId,platform}`. A retry with the same key and
  payload refreshes the unredeemed ticket without creating another audit event.
- `GET /v1/downloads/{ticket}` requires the same signed-in account, accepts a
  ticket once, then redirects to the selected artifact. It returns `410` for a
  used or expired ticket.

`Licence` is `{id,product,status,issuedAt,expiresAt?,graceEndsAt?}`. Its state
is constrained in PostgreSQL: `pending` can become `active` or `revoked`;
`active` can become `grace_period`, `expired`, or `revoked`; and
`grace_period` can recover to `active` or become `expired` or `revoked`.
Every state transition writes an internal entitlement event. A verified account
may download when it has beta access, a live active licence, or an unexpired
grace period. Provider receipt references are stored internally; Sesame never
accepts card data.

`Release` is
`{id,channel,platform,version,sha256,updaterVerified,distributionClass,sigstoreVerified,sigstoreIdentity?,authenticodeVerified,signature,signingKeyId,supportedWindows,releaseNotesUrl,rollbackNotice?,publishedAt}`.
Only `published` rows with hashes, updater signatures, and verified Sigstore
evidence from the exact protected release workflow are returned. An
`early_access` row must remain Authenticode-unsigned; `production` also requires
verified Authenticode evidence. Release records are inserted by the
signing/release pipeline, not by a public API. A withdrawn build or one without
a verified updater signature must never be marked `published`. A row without
Sigstore evidence may exist only as a lab record and is never returned as a
download or updater release.

Each canonical release manifest also records its architecture, monotonic
revision, rollout percentage, update-enabled state, and kill switch. The
operations control plane changes these fields transactionally and audits the
change. A kill switch excludes the release from account download eligibility
immediately; it does not require a website deployment.

Release candidates are accepted only through `POST /v1/release-candidates`.
The release pipeline authenticates with its dedicated bearer credential, never
an admin browser session or CSRF token, and submits an Ed25519-signed descriptor containing the exact
artifact hash, byte length, Tauri updater signature, signer key ID, supported
Windows versions, release-notes URL, distribution class, exact Sigstore issuer
and workflow identity, bundle hash, normalized Sigstore evidence digest, and
optional Authenticode evidence. The API verifies that receipt against its
configured release-candidate public key before writing append-only artifact
evidence. Sigstore and Authenticode are separate evidence and neither
substitutes for the updater signature.

The exact candidate signing payload, signing-key ID, and signature are retained
with the immutable artifact. Older artifact rows without that receipt are not
eligible for desktop update delivery.

Artifact locations are never included in an account response. Tickets expire
after five minutes, are one-time, bound to the issuing account, release and
platform, and stored only as SHA-256 hashes. Ticket issue and redemption are
recorded in the account activity log without the raw ticket or artifact object key.

## Desktop linking and devices

- `GET /v1/account/desktop-link` → latest `{state,linkId?,createdAt?,expiresAt?,deviceId?,device?}`.
- `POST /v1/account/desktop-link` with no body →
  `201 {state:"pending",linkId,code,createdAt,expiresAt}`. Creating another
  request cancels the previous unused code.
- `DELETE /v1/account/desktop-link` → `204` and cancels the pending request.
- `POST /v1/desktop/link` with `{code,deviceName}` is called by the desktop app
  and returns its opaque device token.
- `GET /v1/desktop/status` reports the calling device's connection.
- `DELETE /v1/desktop/connection` revokes the connection from the desktop.
- `GET /v1/account/devices` → `{devices:[...]}`.
- `DELETE /v1/account/devices/{deviceId}` → `204`.

Link states are `none`, `pending`, `connected`, or `expired`. The raw code is
returned once on creation and is never stored in recoverable form. A connected
state remains briefly so the website can show a clear success result.

## Desktop updates

- `GET /v1/desktop/updates?format=tauri&currentVersion=<SemVer>` requires a
  linked desktop token. The Tauri dynamic-updater variant returns top-level
  `{version,notes,pub_date,url,signature,candidateReceipt}` for a newer release
  and `204 No Content` when no update is available. `candidateReceipt` is
  `{payload,signingKeyId,signature}` and is verified by the desktop before it
  trusts the version label. `url` is an opaque, account- and
  device-bound ticket URL, never an artifact location. Release selection uses
  the server-side owner ring when the account is enrolled; clients cannot
  self-select that ring. The non-Tauri account response retains the
  `{available:false}` shape. This route is retained for compatibility with
  earlier account-bound desktop builds. Current desktop builds discover
  updates from the release workflow's static signed manifest and do not call
  this route or send a linked-desktop token for update discovery.
- `GET /v1/desktop/update-tickets/{ticket}` requires the same linked desktop
  token. It redirects to the verified artifact and can be redeemed repeatedly
  by that device until the 30-minute expiry, allowing HTTP range resumption.
  It returns `410 update_ticket_expired` after expiry. The API stores an opaque
  artifact object key and returns only a short-lived gateway URL after a ticket
  has been redeemed by the linked desktop.

## Support intake

- `GET /v1/support` describes the policy.
- `POST /v1/support/requests` accepts
  `{email,subject,message,category?,appVersion?,diagnosticCode?,browserIntegration?,requestId?}` and
  returns `202 {requestId,status:"open"}`. `category` is one of `general`
  (default), `account`, `import`, `sync`, `browser_helper`, `billing`, or
  `bug`; an unrecognized value is rejected with `400 invalid_ticket_category`.
  Admin ticket list and detail responses, and the signed-in account's own
  ticket list and detail responses, all include the stored `category`. The
  admin ticket list additionally accepts a `category` query filter using the
  same allowlist.
- `GET /v1/account/support` lists requests owned by the signed-in account,
  including unread staff-reply counts and close/reopen eligibility.
- `GET /v1/account/support/{id}` returns that account's user/staff conversation
  and marks only that thread read.
- `POST /v1/account/support/{id}/reply` adds a text-only user follow-up.
- `POST /v1/account/support/{id}/close` closes the user's open request.
- `POST /v1/account/support/{id}/reopen` reopens a request closed by the user
  within 30 days.

The intake accepts JSON only, rejects attachment fields and multipart bodies,
and refuses secret-shaped content: `key: value` assignments, the prose form of
the same thing where the value looks like a secret rather than like the rest of
a sentence, OTPAuth URLs, PEM private-key headers, long token-like strings,
unpadded base32 runs long enough to be a TOTP secret, and Sesame's own
recovery-kit shape. It does not echo submitted text. This is a guard rail, not
a guarantee: no filter recognises every secret, so the intake copy still asks
people not to send one. It is tuned to let ordinary problem reports through,
including sentences that merely mention a password.
Users should send a diagnostic code or safe request ID generated by the app,
not logs that could contain secrets. Browser-integration state is a short
allowlisted label, never an extension-installation claim.

Account support reads are scoped by both ticket ID and account ID. Public
reference numbers do not grant access. Internal notes, assignment, priority,
admin identities, email-delivery state, and audit data are never returned by
account endpoints.

## Sync (registered, disabled)

`/v1/sync/*` exists and refuses. Every route returns
`403 sync_unavailable` while the `cloud_sync_available` runtime flag is false,
which it is, and `Config.Sync` is left unset in `cmd/api/main.go`, so enabling
Sync requires a code change as well as a flag change.

The routes are listed here so the contract is reviewable, not because they are
usable:

- `POST /v1/sync/enroll/begin` → issues a one-time, vault-bound, expiring
  enrollment challenge and creates the vault on first use.
- `POST /v1/sync/enroll/finish` → registers a device's Ed25519 signing key and
  X25519 encryption key with a signed proof. The device is `pending` and can do
  nothing until approved.
- `GET /v1/sync/devices` → lists the vault's devices and states.
- `POST /v1/sync/devices/{id}/approve` → carries an encrypted key package from
  an already-approved device. The service cannot produce this package, which is
  what makes "Sesame cannot add a device to your vault" a property rather than a
  promise.
- `DELETE /v1/sync/devices/{id}` → revokes a device and advances the vault
  epoch, invalidating envelopes signed under the previous epoch.
- `GET /v1/sync/key-package?deviceId=` → the wrapped vault key addressed to one
  device.
- `GET /v1/sync/envelope` → current revision and opaque ciphertext.
- `POST /v1/sync/envelope` → compare-and-swap upload. Returns
  `409 sync_conflict` with the current revision when another device got there
  first. A client must resolve that with the user and must never retry by
  overwriting.

Authentication is the desktop device token (`Authorization: Sesame <token>`),
never a website session. Sync moves vault bytes and a browser must not be able
to. There is no cookie path into any Sync handler.

The service stores opaque ciphertext and routing metadata. It does not decrypt,
parse, or log `ciphertext`.

## Administration

`/v1/admin/*` is called only by the separate admin origin. It uses a distinct
cookie, session table, CSRF token and eight-hour TTL. Password plus TOTP is
required. There is no public admin registration; `cmd/adminctl bootstrap`
creates the first one-time setup link.

The API exposes role-checked routes for account support, feature flags,
release metadata, product plans, administrators, aggregated system status and
the append-only audit log. Every mutation writes its audit entry in the same
database transaction as the change. Requests are decoded with a small body
limit and unknown-field rejection, and vault-shaped fields are rejected before
route decoding.
