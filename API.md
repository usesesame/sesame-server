# Sesame account API

This document is the complete closed request and response schema contract for
the Go API. The generated OpenAPI inventories are the source of truth for route
and method enumeration, authentication and CSRF classes, availability, and Go
handler ownership: [project](./openapi/openapi.json) for Sesame's own publishing
deployment and [operator](./openapi/openapi.operator.json) for a self-hosted
operator deployment. Regenerate both with `npm run openapi:generate` from this
directory; server CI compares them byte for byte.

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

## Deployment profiles

`SESAME_DEPLOYMENT_PROFILE` selects what the API serves. Unset or `operator` is
the default self-hosted deployment. `project` is only for Sesame's own
publishing deployments. Any other value stops startup instead of guessing.

Under `operator`:

- The release, extension-publication, and plan administration routes, the
  owner-ring user action, and `POST /v1/release-candidates` are not registered.
  Every role, including `super`, receives `404 not_found`.
- `GET /v1/plans` returns `{plans:[]}`.
- `GET /v1/product/status` returns
  `{webSignInAvailable,desktopConnectionAvailable,registrationMode,cloudSyncAvailable,updated}`.
  It carries no product phase, platform list, account purpose, or download
  state.
- `GET /v1/releases/latest`, `GET /v1/desktop/updates`,
  `GET /v1/desktop/update-tickets/{ticket}`, `GET /v1/account/downloads`,
  `POST /v1/account/download-tickets`, and `GET /v1/downloads/{ticket}` return
  `503 release_artifacts_unavailable`. An operator artifact path is not
  defined yet.
- `updater_enabled` and `public_download` are absent from
  `GET /v1/admin/flags` and the feature flags of
  `GET /v1/admin/system/config`, and `PATCH /v1/admin/flags/{key}` rejects
  them. The signed capability document reports `downloads` and `updater` false.
- `GET /v1/admin/system/config` reports `deploymentProfile`.

Under `project`, the full release, publication, plan, and download surface is
registered and behaves as described below. The admin console hides Releases,
Product plans, and the owner-ring action outside `project`, and the account
portal hides its download surfaces.

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

- `GET /livez` returns `200 {status,service,version,commit}`. A lightweight liveness probe
  that only confirms the process is running.
- `GET /readyz` returns `200 {status,service,version,commit,accounts}` when the database is
  reachable, otherwise `503`. Load balancers and deployment systems use it.
- `GET /healthz` is a deprecated alias for `/readyz`.

## Public metadata

These endpoints are read-only. They reject mutation methods and parse no request
body.

- `GET /v1/plans` returns the free app and the planned Sesame Sync subscription,
  including its optional `annualPrice`.
- `GET /v1/product/status` returns current phase, platform, account, sign-in, sync,
  and download availability.
- `GET /v1/releases/latest?platform=windows|linux` returns release availability for
  the platform. Stays unavailable until the full release set clears the gate:
  exact-workflow Sigstore evidence for every package, and a verified Tauri
  updater signature on the Windows NSIS artifact alone. `signed` reports only
  the updater signature. `artifacts` lists every distributable package with
  its file name, format, URL, SHA-256, and updater-signature state. Production
  additionally requires verified Authenticode evidence.
- `GET /v1/security/boundaries` returns machine-readable confirmation that the API
  accepts and stores no vault data or credentials.
- `GET /v1/support` returns public support availability, a safe-submission
  warning, and `receiptEmail`, a boolean stating whether intake receipts are
  emailed on this deployment. It never returns a staff address.

## Registration and email

- `GET /v1/auth/registration` returns
  `{mode:"closed"|"invite"|"public",enabled,requiresInvite,emailDeliveryAvailable}`.
- `POST /v1/auth/register` with `{email,password,inviteCode?}` returns `202`
  with an empty body. The server enforces the registration mode and consumes
  eligibility/invites transactionally. An address that already has an account,
  an invitation that was already used, and an eligible new address all receive
  the same response. Only a new account receives a verification email, and the
  outbox result does not change the response; a queued message does not mean the
  upstream SMTP relay accepted it. Registration does not create a browser
  session.
- `POST /v1/auth/email/verification/request` with no body returns `202`.
- `POST /v1/auth/email/verification/confirm` with `{token}` returns
  `200 {user}` and a replacement browser session. Verification revokes every
  browser session, passkey, and desktop connection created while the account was
  unverified, and cancels its pending desktop-link codes, in the same transaction
  that marks the address verified.
- `POST /v1/auth/password/recovery/request` with `{email}` returns `202`. Missing,
  malformed, and known emails receive the same response shape.
- `POST /v1/auth/password/recovery/confirm` with `{token,newPassword}` returns
  `200 {user,otherSessionsRevoked:true}` and a replacement session. Every
  linked desktop token is revoked with the old browser sessions.
- `POST /v1/account/email/change/request` with `{newEmail}` returns `202`.
- `POST /v1/account/email/change/confirm` with `{token}` returns
  `200 {user,otherSessionsRevoked:true}` and a replacement session.

Verification tokens live for 24 hours. Recovery and email-change tokens live
for 30 minutes. Only SHA-256 token hashes are stored. Tokens are single-use;
creating another token for the same purpose invalidates the previous one. A
password change or password reset invalidates every pending email-change
token, and a completed email change invalidates every pending recovery token.
The queued action email stores its link sealed with the deployment's admin
encryption key, never in plaintext; a delivered or failed outbox row is
purged seven days after its last update.

Action emails contain links such as `/verify-email#token={token}`. The token
is in the URL fragment so it is never sent to the server in the request line,
never appears in access logs, and is not included in `Referer` headers.
Client-side code must read the fragment and submit the token in the POST body
of the confirmation endpoint.

`user` is `{id,email,emailVerified,betaAccess}`. Confirming an email change or
password recovery revokes every older browser session in the same transaction
that applies the account change. A password reset also revokes every linked
desktop token in that transaction. A completed email change also queues a
security notice to the previous address. Recovery also revokes every passkey and
desktop connection created while the account was unverified, cancels its
pending desktop-link codes, and revokes every desktop connection regardless of
when it was created. Credentials an attacker attached to an unverified account
do not survive verification or recovery. Verification clears the password set
at registration, so the address owner who did not start that registration sets
a new password through password recovery.

## Recent authentication and browser sessions

Password login, passkey login, verification completion, recovery completion,
and email change completion mark a browser session as recently authenticated.
The default recent-auth window is ten minutes.

- `POST /v1/account/reauthenticate` with `{password}` returns `204`.
- `GET /v1/account/sessions` returns `{sessions:[Session]}`.
- `DELETE /v1/account/sessions` returns `204` and revokes every website session
  and every linked desktop token.
- `DELETE /v1/account/sessions/{id}` returns `204`.

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
password, revocation of every old session and every linked desktop token, and
creation of the replacement current session are one database transaction.

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
ceremony. If an authenticator reports a possible clone after a successful
assertion, the API records a `passkey_clone_warning` security event and
refuses the sign-in instead of creating a session.

## Account state and deletion

- `GET /v1/auth/me` returns the signed-in account id and email only.
- `GET /v1/account/bootstrap` returns the combined first-paint state the website
  needs, so a signed-in page load does not fan out into several requests.
- `GET /v1/account/activity` returns `{events:[...]}`, the account's own 50 most
  recent security events.
- `GET /v1/account/notifications` returns `{securityMandatory:true,preferences}`.
  `PATCH` the same path with `{betaReleases,supportReplies,productAnnouncements}`
  returns `204`. `supportReplies` defaults to on for every account and can be
  switched off. Security mail cannot be switched off.
- `POST /v1/account/delete` with `{password}` deletes the account. Requires
  recent authentication and re-verifies the password.

## Beta access, licences, and verified private-beta downloads

- `GET /v1/account/access` returns
  `{betaAccess,emailVerified,downloadsAllowed,licences:[Licence]}`.
- `GET /v1/account/downloads` returns `{releases:[Release]}`.
- `POST /v1/account/download-tickets` with
  `{releaseId,platform}` and a random `Idempotency-Key` header returns
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

`Release` contains one immutable artifact set for a channel, platform,
architecture, and version. Each artifact records its package format,
architecture, SHA-256 digest, byte count, download location, Sigstore evidence,
and updater capability. Windows sets contain NSIS. Linux sets contain AppImage,
DEB, and RPM records. Linux records do not claim updater capability. Only
`published` sets whose complete artifact set has eligible evidence can be
returned. An `early_access` artifact remains Authenticode-unsigned;
`production` also requires verified Authenticode evidence.

Each canonical release manifest also records its architecture, monotonic
revision, rollout percentage, update-enabled state, and kill switch. The
operations control plane changes these fields transactionally and audits the
change. A kill switch excludes the release from account download eligibility
immediately; it does not require a website deployment.

Release controls use `POST` commands for publish, rollout, emergency stop, and
withdrawal. Every command includes the current manifest revision. Stale
commands return `409`; artifact fields are never accepted by these routes.

Release candidates are accepted only through `POST /v1/release-candidates`.
The release pipeline authenticates with its dedicated bearer credential, never
an admin browser session or CSRF token, and submits a schema version 3,
Ed25519-signed release set. Its digest binds every package format,
architecture, artifact hash, byte length, download URL, updater capability,
updater signature when applicable, distribution class, exact Sigstore issuer
and workflow identity, bundle hash, normalized Sigstore evidence digest, and
optional Authenticode evidence. The API recomputes the set digest and verifies
the receipt before writing all artifact records in one transaction. Sigstore
and Authenticode remain separate evidence. Neither substitutes for an updater
signature on an updater-capable package.

The exact candidate signing payload, signing-key ID, and signature are retained
with every immutable artifact in the set. Older artifact rows without that
receipt are not eligible for desktop update delivery.

Replaying the same signed candidate returns the existing release and does not
add another audit row. A request for the same channel, platform, architecture,
and version with different signed evidence returns `409`.

Artifact locations are never included in an account response. Tickets expire
after five minutes, are one-time, bound to the issuing account, release and
platform, and stored only as SHA-256 hashes. Ticket issue and redemption are
recorded in the account activity log without the raw ticket or artifact object key.

## Desktop linking and devices

- `GET /v1/account/desktop-link` returns the latest `{state,linkId?,createdAt?,expiresAt?,deviceId?,device?}`.
- `POST /v1/account/desktop-link` with no body returns
  `201 {state:"pending",linkId,code,createdAt,expiresAt}`. Creating another
  request cancels the previous unused code.
- `DELETE /v1/account/desktop-link` returns `204` and cancels the pending request.
- `POST /v1/desktop/link` with `{code,deviceName}` is called by the desktop app
  and returns its opaque device token.
- `GET /v1/desktop/status` reports the calling device's connection.
- `POST /v1/desktop/heartbeat` with
  `{appVersion,platform,architecture,updateChannel,protocolVersion,browserHelperCapable,browserHelperObserved}`
  records runtime details for the calling device and returns `{device}`.
- `GET /v1/desktop/config` returns
  `{minimumProtocolVersion,syncAvailable,browserHelper:{capable,lastObservedAt}}`.
- `DELETE /v1/desktop/connection` revokes the connection from the desktop.
- `GET /v1/account/devices` returns `{devices:[...]}`.
- `PATCH /v1/account/devices/{deviceId}` with `{deviceName}` returns `204` and
  renames a connected desktop.
- `DELETE /v1/account/devices/{deviceId}` returns `204`.

Link states are `none`, `pending`, `connected`, or `expired`. The raw code is
returned once on creation and is never stored in recoverable form. A connected
state remains briefly so the website can show a clear success result. A
password change, a password reset, and revoking every website session delete
every linked desktop token in the same transaction. The desktop receives the
normal authorization failure and must link again.

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
- `POST /v1/account/support/{id}/attach` attaches a guest request to the
  signed-in account when the account is verified and its address matches the
  request address, then returns the ticket. A closed guest request may still be
  attached. Attaching revokes every live guest link in the same transaction.
- `POST /v1/support/access` with `{token}` returns `200 {ticket}` for a live
  guest link and `POST /v1/support/access/reply` with `{token,message}` adds a
  text-only follow-up and returns `201 {ticket}`. Neither route reads or sets a
  session cookie, and both are origin- and CSRF-checked like the intake route.

A staff reply to a guest request queues one email that carries only a link and
a short instruction, never the reply body, subject, or reference. The link
secret is 32 random bytes stored only as a SHA-256 hash; the raw value travels
in the URL fragment and in JSON request bodies. A link is repeatable for 7 days,
issuing a newer link revokes the older ones, and closing or attaching the
request revokes every live link. Redemption resolves only the one request the
token was issued for and requires the request to stay unattached, open, and at
the same address. Unknown, expired, revoked, closed, attached, and
address-mismatched links all return the same `400 support_link_invalid` body.
Redemption is limited to 60 requests per client per minute; replies are limited
to 12 per client per hour and 20 per link per hour.

Account ticket list and detail responses carry `autoClosed`. It is `true` when
the system closed the request after 14 days without activity; the 30-day reopen
window is unchanged, and no email is sent for that close. Scheduled maintenance
deletes a closed request and its linked email outbox rows 90 days after
closure. Open, in-progress, and waiting requests are never deleted by
retention, and a waiting request with recent activity is never closed
automatically.

Guest and signed-in intake returns the reference and, when SMTP is configured,
queues a receipt to the requester that carries only the reference and the
portal link. Intake is capped per client and per recipient address. When
`SESAME_SUPPORT_NOTIFY_EMAIL` is set, a new request and a signed-in follow-up
queue one notice to that address with the reference, category, and admin
console link. Staff replies queue a notice only when the owning account's
support-reply preference is on; that enqueue shares the reply transaction, so a
failed enqueue fails the reply. No notification contains the subject or
message.

The admin ticket detail and reply responses carry a server-computed
`emailDeliveryReason` on each staff message: `delivered`, `pending`, `failed`,
`guest` (the request has no account), `mail-off` (no SMTP sender),
`opted-out` (the account turned reply email off), or `not-queued` (no outbox
row, including a preference lookup failure). A lookup failure never reports
`opted-out`. User messages carry no reason. The ticket detail response also
carries `mail: {deliveryConfigured, staffNotifyConfigured}` so support staff can
see the deployment's mail state without `system:read`. The list response and
account responses are unchanged, and neither field exposes an address or a
secret.

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
account endpoints. A guest link is a capability for one request only: it is
bound to the requester address stored at issue time and stops working on close,
attach, or a newer link.

## Sync (registered, disabled)

`/v1/sync/*` exists and refuses. Every route returns
`403 sync_unavailable` while the `cloud_sync_available` runtime flag is false,
which it is, and `Config.Sync` is left unset in `cmd/api/main.go`, so enabling
Sync requires a code change as well as a flag change.

The routes are listed here so the contract is reviewable, not because they are
usable:

- `POST /v1/sync/enroll/begin` issues a one-time, vault-bound, expiring
  enrollment challenge and creates the vault on first use.
- `POST /v1/sync/enroll/finish` registers a device's Ed25519 signing key and
  X25519 encryption key with a signed proof. The device is `pending` and can do
  nothing until approved.
- `GET /v1/sync/devices` lists the vault's devices and states.
- `POST /v1/sync/devices/{id}/approve` carries an encrypted key package from
  an already-approved device. The service cannot produce this package, which is
  what makes "Sesame cannot add a device to your vault" a property rather than a
  promise.
- `POST /v1/sync/devices/{id}/deny` removes a pending device, which never had
  the vault key and therefore needs no key rotation.
- `POST /v1/sync/devices/{id}/rekey` removes another approved device while
  atomically advancing the vault epoch, replacing the encrypted envelope, and
  supplying new encrypted key packages for every survivor.
- `DELETE /v1/sync/devices/{id}` lets only the calling device leave the
  vault. Removing another approved device requires the signed rekey ceremony.
- `GET /v1/sync/key-package` returns the wrapped vault key addressed to the
  authenticated device.
- `POST /v1/sync/activate` proves that an approved device received its key
  package before making it active.
- `POST /v1/sync/reset` deletes an abandoned synced vault only when no
  approved device remains.
- `GET /v1/sync/envelope` returns the current revision and opaque ciphertext.
- `POST /v1/sync/envelope` performs a compare-and-swap upload. Returns
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

Destructive actions also require a fresh credential check: user deletion,
owner release and beta changes, suspension, session and device revocation,
feature-flag changes, release publication, rollout, emergency stop and
withdrawal, extension publication acceptance and transition, plan changes, and
administrator creation, update and deletion. The admin session must have
re-authenticated within the last five minutes. Sign-in and setup count as a
fresh re-authentication. A missing or expired check returns
`403 admin_step_up_required` and commits no change.

- `POST /v1/admin/auth/step-up` renews the re-authentication of the caller's
  own admin session. Send exactly one of `password` or `code`; the code is a
  current six-digit TOTP value and is rejected if its time step was already
  used. Sign-in and step-up share the same counter, so a code used to sign in
  cannot be reused for step-up. The pending action still enforces its role
  permission separately. A wrong password or code returns
  `401 invalid_admin_credentials`. Success
  returns the `200` receipt `{"stepUpExpiresAt": "...", "windowSeconds": 300}`.
  A successful check writes one `admin.step_up` audit entry naming the method,
  and the route is rate limited per administrator and per peer.

The API exposes role-checked routes for account support, feature flags,
release metadata, product plans, administrators, aggregated system status and
the append-only audit log. Every mutation writes its audit entry in the same
database transaction as the change. Requests are decoded with a small body
limit and unknown-field rejection, and vault-shaped fields are rejected before
route decoding.

- `GET /v1/admin/system/health` returns a typed operational snapshot for
  `system:read`. It reports deployed version and commit, schema and database
  readiness, release pipeline and artifact delivery state, capped email outbox
  pending and failed totals, and the last maintenance result. Dependency waits
	stop after two seconds and return a safe status rather than config values
  or credentials. Each outbox total is capped at 100.
- `GET /v1/admin/system/rate-limits` returns the current limiter counters for
  `system:read`.
- `GET /v1/admin/system/config` returns the feature-flag document for
  `system:read`.
- `GET /v1/admin/saved-replies` lists the saved replies for `support:read`.
  `POST /v1/admin/saved-replies` creates one, and
  `PATCH`/`DELETE /v1/admin/saved-replies/{id}` updates or deletes one, for
  `support:manage`. A title is capped at 120 characters and a body at 8,000,
  both must be non-empty, and both pass the secret-shaped-content guard before
  storage. Every mutation writes its audit entry in the same transaction.
  Updates and deletes return `404 admin_record_not_found` for an unknown id.
