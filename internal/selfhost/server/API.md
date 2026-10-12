# Self-host server API

This file describes the HTTP routes of `sesame-server` in the self-host profile. The console is served from the same origin as the API, so there is no CORS and the console calls relative paths. Every request and response body is JSON unless a route says otherwise.

## Conventions

Errors use one envelope on every route: `{"error":{"code":"...","message":"..."}}`. The code is stable and meant for programs. The message is plain text for people.

Times are RFC 3339 strings in UTC with whole seconds. A time that is not set is `null`.

Request bodies must have `Content-Type: application/json`, must be at most 16 KiB, and must not contain fields a route does not list. Anything else gets 415 `json_required`, 413 `request_too_large` or 400 with the route's invalid request code.

Unknown API paths under `/v1/` return 404 `not_found`. A known path with the wrong method returns 405 `method_not_allowed` with an `Allow` header. There is no CORS support and no OPTIONS handling.

Every API response carries `X-Request-ID`. The server keeps a client supplied `X-Request-ID` when it is 16 to 64 characters of letters, digits, `-` and `_`, and otherwise makes a new one. API responses also send `Cache-Control: no-store` and `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'`.

### Host check

Every `/v1/owner/*` and `/v1/desktop/*` route answers 421 `host_mismatch` unless the `Host` header equals the host of `SESAME_PUBLIC_URL`. Case, a trailing dot and the default port are ignored. The server never reads `X-Forwarded-Host`, so a reverse proxy must pass the original `Host` header. The routes `/livez`, `/readyz`, `/config.json`, `/v1/instance`, `/v1/capabilities`, `/metrics` and the console files are not checked, so health checks can use any address.

### Owner session, CSRF and origin

Owner routes use a session cookie. Its name is `__Host-sesame_owner` when the public URL is https and `sesame_owner` when it is plain http on a loopback address. The cookie has `Path=/`, `HttpOnly`, `SameSite=Strict`, no `Domain`, and `Secure` when the public URL is https. Its lifetime follows the session, which lasts 12 hours. The console never reads the cookie.

Each session has a CSRF token. The console gets it from `csrfToken` in the setup, login and session responses and sends it back in the `X-Sesame-CSRF` header on every POST, PATCH and DELETE to an authenticated owner route. A missing or different value gets 403 `invalid_csrf`. The token is tied to the session, so a token from another session does not work.

Every POST, PATCH and DELETE to an owner route, including login and setup, must carry an `Origin` header equal to the public origin. Browsers add it on their own. A different or missing value gets 403 `origin_not_allowed`. A GET with a different `Origin` also gets 403.

Order of checks on an authenticated route: host, origin, session, CSRF, then step-up when the route needs it. A missing cookie gives 401 `not_authenticated`. An unknown or expired session gives 401 `session_expired` and clears the cookie.

### Step-up

Routes marked step-up need a sign-in or step-up within the last 10 minutes. Otherwise they return 403 `step_up_required`. The console should then ask for the password and a current code, call `POST /v1/owner/step-up` and repeat the request. `recentAuthUntil` in the session response says when the window ends.

### Rate limits

Limits are counted per peppered client address, and login and step-up are also counted per name or owner. A client over its limit gets 429 `too_many_attempts` with a `Retry-After` header in seconds. The client address comes from the socket, or from `X-Forwarded-For` only when the socket address is inside `SESAME_TRUSTED_PROXIES`. Current limits per minute unless stated: login 5 per name and 20 per address, step-up 10 per five minutes per owner and 20 per address, setup details 10, setup 5, update check 3 per hour per owner, desktop link 8, desktop status 60, heartbeat 120, desktop config 60, disconnect 20.

## Public routes

`GET /livez` returns `{status:"ok", service:"sesame-server", version, commit}`.

`GET /readyz` returns 200 with `{status:"ok", ..., database:"ready"}` or 503 with `{status:"not_ready", ...}` when the database does not answer.

`GET /config.json` returns `{version, setupRequired, apiBase:""}`. The console reads it first. When `setupRequired` is true the console shows the setup page, which reads the token from the URL fragment.

`GET /v1/instance` returns `{instanceId, name, version, commit, apiVersion:1, minimumClientVersion, maximumClientVersion, profile:"selfhost", modules:[], capabilityKeyId, capabilityPublicKey, fingerprint, setupRequired}`. `maximumClientVersion` is left out when none is set. `capabilityPublicKey` is the raw 32 byte Ed25519 key in unpadded base64url. `fingerprint` is the lowercase hex SHA-256 of those raw bytes.

`GET /v1/capabilities` returns `{payload, signature, keyId}` in the same format as the hosted service. `payload` is unpadded base64url JSON with `schemaVersion`, `minimumDesktopVersion`, `latestDesktopVersion`, `features` and `serviceStatus`. `signature` is the Ed25519 signature over the payload bytes. `features.desktopLinking` follows the `desktop_linking_enabled` flag. `downloads`, `updater` and `sync` are always false. The route supports `If-None-Match` and answers 304.

`GET /metrics` exists only when metrics are switched on. It returns Prometheus text and is limited to direct requests from loopback or private addresses without forwarding headers. Others get 403 `metrics_forbidden`.

## Console files

Every other GET and HEAD path is served from the embedded console. A path that is not a file and has no extension returns `index.html` with 200, so client side routes work. A missing path with an extension returns 404 `not_found`. Other methods return 405.

Console responses use `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`. That means no inline scripts or styles, fonts only from the same origin and never from data: URLs, and no images from other origins. Files under `/assets/` are cached for a year, so the build must put a content hash in their names. Everything else is sent with `Cache-Control: no-cache`.

## Setup and sign-in

`POST /v1/owner/setup/details` with `{token}` returns `{totpSecret, totpUri, ownerName, firstOwner, expiresAt}`. `ownerName` is empty for the first owner, who chooses a name. An invited owner already has one. A bad, used or expired token returns 400 `setup_token_invalid`.

`POST /v1/owner/setup` with `{token, name, password, code}` and an optional `updateChecks` creates the owner, signs them in and returns 201 with the session object. The password must be 12 to 1024 bytes and the code is the current six digit code for the secret from the details call. `updateChecks` must be true or false when present. On the first owner's setup it turns update checks on or off, and when it is true the server starts a check in the background. When it is absent the choice stays unset. For an invited owner or a reset the field is accepted and ignored, so a setup link cannot change the instance's update choice. Errors: 400 `setup_token_invalid`, 400 `invalid_setup` for a bad field or wrong code, 409 `name_taken`. A token works once.

`POST /v1/owner/login` with `{name, password, code}` returns 200 with the session object. Every failure returns the same 401 `invalid_credentials` with the same message, whether the name is unknown, the password is wrong, the code is wrong or the code was already used.

`POST /v1/owner/logout` returns 204 and clears the cookie.

`GET /v1/owner/session` returns the session object.

`POST /v1/owner/step-up` with `{password, code}` returns the session object with a new `recentAuthUntil`. A code that was already used for a sign-in or step-up is refused with 401 `invalid_credentials`, so the console should tell the person to wait for the next code.

The session object is `{owner:{id,name}, csrfToken, recentAuthUntil, expiresAt}`.

## Owners

`GET /v1/owner/owners` returns `{owners:[{id, name, createdAt, lastLoginAt, setupPending, current}]}`. `current` marks the signed in owner.

`POST /v1/owner/owners` with `{name}` needs step-up and returns 201 `{owner, setupToken, link, expiresAt}`. The link is `<publicOrigin>/setup#token=<token>`. The token is shown once and expires after 24 hours. A used name returns 409 `conflict`.

`DELETE /v1/owner/owners/{id}` needs step-up and returns 204. The last active owner returns 409 `last_owner`. Removing yourself clears the cookie.

## Members

`GET /v1/owner/members` returns `{members:[{id, name, createdAt, deviceCount}]}`.

`POST /v1/owner/members` with `{name}` returns 201 `{member}`. A used name returns 409 `conflict`.

`PATCH /v1/owner/members/{id}` with `{name}` returns `{member}`.

`DELETE /v1/owner/members/{id}` needs step-up and returns `{revokedDevices}`. It revokes every device the member holds.

## Pairings and devices

`POST /v1/owner/pairings` takes either `{self:true}` or `{memberId}`, plus an optional `deviceName` hint of printable ASCII up to 64 characters. It returns 201 `{pairingId, code, link, holder, expiresAt}`. The code is shown once, works once and expires after 10 minutes. The link is `<publicOrigin>/pair#code=<code>&fp=<fingerprint>`. A pairing for a member needs step-up, a pairing for yourself does not. Sending both fields or neither returns 400 `invalid_pairing`. An unknown member returns 404 `not_found`. Creating a pairing cancels any earlier pending pairing for the same holder, so the older code then fails with 401 `invalid_desktop_link`.

`GET /v1/owner/pairings` returns `{pairings:[{id, holder:{kind,id,name}, deviceName, createdBy, createdAt, expiresAt}]}` for pending pairings. It never includes codes.

`DELETE /v1/owner/pairings/{id}` cancels a pending pairing and returns 204.

`GET /v1/owner/devices` returns `{devices:[...]}` for active devices. Add `?includeInactive=true` to include revoked and expired ones. A device has `id, name, holder:{kind,id,name}, appVersion, platform, architecture, updateChannel, protocolVersion, browserHelperCapable, browserHelperLastObservedAt, createdAt, expiresAt, lastSeenAt, revokedAt`. `holder.kind` is `owner` or `member`.

`DELETE /v1/owner/devices/{id}` needs step-up and returns 204. The device's token stops working at once.

## Audit, settings, flags, system and export

`GET /v1/owner/audit?cursor=&limit=` returns `{entries, nextCursor, chain}` with newest entries first. `limit` is 1 to 200 and defaults to 50. Pass `nextCursor` as `cursor` for the next page. A `nextCursor` of 0 means there are no more. An entry is `{seq, actor, action, target, detail, at, hash}`. `chain` is `{ok, rows, headSeq, headHash, firstBreak}` where `firstBreak` is `{seq, reason}` or null. The chain detects changes made to the database file and does not prevent them. By default the server checks only the rows added since its last successful check, and it confirms that the last checked row is unchanged and that the first new row links to it. That keeps the call fast on a large log. Add `full=true` to read every row again, which is the only way to notice an edit to an old row. Once a check has found a break, later default calls keep reporting it until a full check runs. `full` accepts `true`, `false`, `1` and `0`, and anything else returns 400 `invalid_full`. The first default call after the server starts runs a full check.

`GET /v1/owner/settings` returns `{instanceId, name, publicUrl, publicUrlSet, createdAt, fingerprint}`. `publicUrl` is the address the server was started with.

`PATCH /v1/owner/settings` needs step-up. It takes `name`, `publicUrl` or both. The public URL can only be cleared or set to the address the server already runs on, because changing it requires a new `SESAME_PUBLIC_URL` and a restart. Errors: 400 `invalid_name`, 400 `invalid_public_url` for non https addresses other than loopback, 400 `public_url_mismatch`.

`GET /v1/owner/flags` returns `{flags:[{key, description, enabled, updatedAt}]}`.

`PATCH /v1/owner/flags/{key}` with `{enabled}` returns the flag. An unknown key returns 404 `not_found`.

`GET /v1/owner/system` takes the same `full` parameter for `auditChainOk` and returns `{version, commit, schemaVersion, databaseBytes, lastBackupAt, warnings, auditChainOk, activeOwners, members, activeDevices, pendingPairings, metricsEnabled, updateAvailable}`. `warnings` is a list of sentences for the owner to read. `updateAvailable` is true when update checks are on and the last good feed offers a newer version on the owner's channel, so the console can show a banner without a second call. `GET /v1/instance` does not change.

`POST /v1/owner/export` needs step-up and takes no body. It returns a JSON download with `Content-Disposition: attachment`. The document has `format` set to `sesame-selfhost-export-v1`, `exportedAt`, `instance`, `members`, `devices` including inactive ones, and `audit` with `chain`, `entries` oldest first and `truncated`. It holds no tokens, secrets or password data.

## Updates

The server can check a feed for newer releases and show the owner what to run. It does not download, install or restart anything. The owner chooses whether it checks. A check is one plain GET request to a fixed address, with no query string, no instance id, no cookie and no version, so the feed host sees only the server's IP address. The feed is a signed JSON document, and the server accepts it only when a pinned Ed25519 public key verifies it. SELF-HOSTING.md describes the privacy side and the settings.

`GET /v1/owner/updates` returns the update document and never makes a request to the feed. Its fields are:

- `configured` is false when the build has no pinned public key and `SESAME_UPDATE_PUBLIC_KEYS` is unset. The server then never makes a request and `error` is `not_configured`.
- `enabled` is true, false or null. Null means the owner has not chosen yet. Null and false both mean the server makes no request.
- `channel` is `stable` or `beta` and defaults to `stable`. A prerelease version is never offered on `stable`.
- `installKind` is `container` or `binary`. It comes from `SESAME_INSTALL_KIND`, or from the presence of `/.dockerenv` when that is unset.
- `current` is `{product, version}` for the running server.
- `latest` is null or `{version, publishedAt, notesUrl, security, minimumFrom, images, binaries}`. `images` is a list of `{ref}` where each ref ends in `@sha256:` and 64 hex digits. `binaries` is a list of `{os, arch, url, sha256}`. `minimumFrom` is empty or the oldest version that can update directly to this one, and the server does not enforce it. `latest` is null while `enabled` is not true or when the feed has no release on the owner's channel.
- `available` is true when `latest` is newer than `current` and the stored feed has not expired. When the stored feed has expired, `latest` stays as the last good result, `available` is false, `commands` is empty and `error` is `feed_expired` until a fresh feed arrives.
- `checkedAt` is the time of the last successful check, or null.
- `error` is empty or the code of the most recent failed check. The codes are `feed_unreachable` for a network failure, a bad status or a refused redirect, `feed_invalid` for a bad signature, an unpinned key, a malformed document or a body over 256 KiB, `feed_rollback` for a sequence lower than one already accepted, `feed_expired` for an expired document, and `not_configured`. A failed check keeps `latest` and `checkedAt` from the last good check.
- `commands` is a list of `{label, text}` and is empty unless `available` is true. For a container install it holds the line `SESAME_IMAGE_TAG=<version>`, then `docker compose pull`, a `docker image inspect` command that prints the digests of the image that was pulled, the first image ref from the feed to compare it with, and `docker compose up -d`, all to run in the directory that holds the compose file. For a binary install it holds a `curl` download of the program for this operating system and architecture that allows only https, including after redirects, a SHA-256 comparison against the hash in the feed, an `install` that replaces the running program and a `systemctl restart sesame-server`. It is empty when the feed lists no program for this platform. The only thing verified is the feed's signature. The commands compare the digest or hash from the feed with what the owner downloaded, and the server does not check the image or the program itself.

`PATCH /v1/owner/updates` with `{enabled, channel}` needs step-up. Both fields are optional but at least one must be present. `enabled` must be true or false and `channel` must be `stable` or `beta`. Anything else, an unknown field or an empty object returns 400 `invalid_updates`. The change is written to the audit log as `updates.updated` with the changed fields. When the call turns checks on, the server fetches the feed before it answers, and a failed fetch shows up as `error` in the returned document and not as an HTTP error. Changing only the channel does not fetch. The response is the update document.

`POST /v1/owner/updates/check` takes no body and needs a session and CSRF token but no step-up. It is limited to 3 calls per hour per owner and returns 429 `too_many_attempts` with `Retry-After` after that. It fetches the feed once and returns the update document. When `configured` is false it makes no request, whatever `enabled` says, and returns 200 with the document and `error` set to `not_configured`. Otherwise, when `enabled` is not true, it makes no request and returns 409 `updates_off`.

A check also runs about once a day with a random delay of up to one hour, and about an hour later after a failed fetch. When the server starts with checks on, it skips the check if the last good result is less than a day old, and otherwise waits a random time of up to 10 minutes before it checks.

The server accepts a feed when all of these hold:

- The envelope is `{payload, signature, keyId}`. `payload` is unpadded base64url JSON and `signature` is an unpadded base64url Ed25519 signature over the bytes `sesame-update-feed-v1`, a line feed and the decoded payload. `keyId` must name a pinned key.
- The payload has `schemaVersion` 1, an integer `sequence` from 1 to 1,000,000,000, `issuedAt` no more than 10 minutes in the future, `expiresAt` in the future and at most 45 days after `issuedAt`, and a product list that includes `sesame-server`.
- Every URL is https without user info or a fragment, every SHA-256 is 64 lowercase hex digits and every image ref is a lowercase name followed by `@sha256:` and 64 lowercase hex digits.
- The `sequence` is at least the highest one accepted before for the same `keyId`. Each key id has its own highest sequence, so a newly pinned key starts fresh and a compromised key cannot block its successor. The same sequence is accepted as the same document. A lower one is a rollback, is refused, is recorded as `feed_rollback` and is written to the audit log once as `updates.rollback_rejected`.

The highest accepted sequence per key id and the last good feed are kept in the database. The host command `sesame-server updates reset-sequence` forgets every key's highest sequence, clears a `feed_rollback` error and writes `updates.sequences_reset` to the audit log. It is for a maintainer who published a number that was too high by mistake. The payload and signature may not contain line breaks, and URLs may not contain `[`, `]`, `{` or `}`. The server checks the stored feed's signature again each time it builds the document.

## Desktop routes

These routes keep the wire shapes of the hosted service so the existing desktop works unchanged. They need no cookie. They send no CORS headers and refuse any request with an `Origin` header.

A device token goes in `Authorization: Sesame <token>`. `Authorization: Bearer <token>` is accepted as well. A missing, malformed, unknown, expired or revoked token returns 401 `not_authenticated`.

`POST /v1/desktop/link` with `{code, deviceName}` returns 201 `{accessToken, device, expiresAt, syncAvailable:false}`. The code is 32 to 128 characters. The device name is printable ASCII up to 64 characters. A bad shape returns 400 `invalid_desktop_link`. An unknown, used, cancelled or expired code returns 401 `invalid_desktop_link`. Switching off the `desktop_linking_enabled` flag returns 503 `desktop_linking_disabled`. The token lasts 90 days.

`GET /v1/desktop/status` returns `{connected:true, device, syncAvailable:false, browserHelperAvailable}`.

`POST /v1/desktop/heartbeat` takes `{appVersion, platform, architecture, updateChannel, protocolVersion, browserHelperCapable, browserHelperObserved}` and returns `{device}`. `protocolVersion` is 1 to 100 and the text fields are at most 64 characters with no line breaks or tabs. A bad body returns 400 `invalid_desktop_heartbeat`.

`GET /v1/desktop/config` returns `{minimumProtocolVersion:1, syncAvailable:false, browserHelper:{capable, lastObservedAt}}`.

`DELETE /v1/desktop/connection` revokes the calling device and returns 204. It also returns 204 when the token was already revoked, so a desktop can always finish unlinking.

The `device` object is `{deviceId, deviceName, connectedAt, expiresAt, appVersion, platform, architecture, updateChannel, lastSeenAt, protocolVersion, browserHelperCapable, browserHelperLastObservedAt}`. Empty text fields and an unset `browserHelperLastObservedAt` are left out.

## Error codes

Common: `not_found`, `method_not_allowed`, `json_required`, `request_too_large`, `host_mismatch`, `origin_not_allowed`, `invalid_csrf`, `not_authenticated`, `session_expired`, `step_up_required`, `too_many_attempts`, `rate_limit_unavailable`, `unavailable`, `internal_error`.

Owner: `invalid_request`, `invalid_name`, `invalid_credentials`, `setup_token_invalid`, `invalid_setup`, `name_taken`, `conflict`, `last_owner`, `invalid_pairing`, `invalid_cursor`, `invalid_limit`, `invalid_public_url`, `public_url_mismatch`, `invalid_updates`, `updates_off`.

Desktop: `invalid_desktop_link`, `invalid_desktop_heartbeat`, `desktop_linking_disabled`, `desktop_link_unavailable`, `desktop_heartbeat_unavailable`, `capabilities_unavailable`.
