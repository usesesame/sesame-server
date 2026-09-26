# Sesame support system

This describes shipped behaviour, not a proposal.

The support system is implemented across the public website, the vault-blind Go API, and the separate admin application. It accepts text only. It must never receive a vault, password-manager export, password, PIN, recovery kit, backup code, TOTP seed, encryption key, session token, or screenshot containing those values.

This file records the current boundary and the remaining release work.

## Implemented

### Public website and account portal

- The public website links to the account portal's support form when an account
  origin is configured. It does not submit support content itself.
- Guests and signed-in users can create a request through
  `POST /v1/support/requests` from the account portal.
- The API rejects attachment content types, unknown fields, oversized content, and common secret-shaped text before storing a request.
- Intake is rate-limited per client and per recipient address and returns a reference number without exposing ticket contents publicly.
- When SMTP is configured, intake queues a receipt to the requester carrying only the reference and the portal link.
- A signed-in user can list and read only requests owned by that account under `/v1/account/support/*`.
- Signed-in users can add a follow-up to an open request, close it, and reopen
  it for 30 days after closure.
- The Support links in the portal show the unread reply count, hidden at zero,
  and the sign-in page explains an expired session instead of showing an
  ordinary signed-out visit.
- The support form states the response expectation: "We aim to reply within
  3 business days."
- The support form and a signed-in request page state whether a receipt and
  reply email will be sent. Guests are told a receipt is emailed when the
  deployment's public support metadata reports mail is configured, or that
  email is unavailable on this deployment and the reference is still shown
  after submit. Signed-in users see the account's support-reply preference with
  a link to the toggle in Security settings.
- A request the system closed after 14 days without activity says it closed
  automatically. The 30-day reopen path is unchanged.
- A staff reply to a guest request queues an email that carries only a link and
  a short instruction. The link opens `/support/request` in the account portal,
  reads that one request, and can add a text-only follow-up. It is repeatable
  for 7 days, a newer link revokes the older ones, and closing or attaching the
  request revokes every live link. The page keeps the token out of the address
  bar and browser history, and no website session is required or created.
- A guest can attach the request to a signed-in account whose verified address
  matches the request address. Attaching moves the request to
  `/v1/account/support/*` and revokes every live guest link.
- Both sites repeat the no-secrets boundary before directing or submitting a
  request.

### Desktop app

- Settings has a "Sesame support" entry that opens the website support form in the system browser, prefilled with only short, user-reviewed fields (app version, a safe diagnostic code, browser-integration status, a safe request ID). No diagnostic file, raw error, vault record, or account token is sent.
- The entry reflects the desktop's own account-link state (Settings > Connections > Sesame account, a separate feature from Sync): linked desktops are told to sign in to the website in the same browser to keep replies with that account; unlinked desktops are told to sign in first if they want a history. The desktop does not know the linked account's email, so it cannot prefill or claim a sign-in on the account's behalf.

### Admin workspace

- `super` and `support` administrators can list, filter, and inspect support requests.
- The users and ticket lists paginate (100 per page, Prev/Next) and search as you type; the backend already returned `page`/`size`/`total`.
- A ticket from a signed-in account shows any desktop currently linked to that account, so staff can see the link state directly. This reads the existing desktop-link table; it does not add a new link mechanism.
- Staff can assign or unassign a request, set its priority and status, add an internal note, and add a staff reply. The ticket detail shows the current assignee and, for `super`/`support` roles, a dropdown to reassign or unassign. The dropdown is fed by `GET /v1/admin/support/assignees`, which returns only the `id` and `email` of `super` and `support` administrators (the only targets `AssignTicket` accepts) and requires `support:read`, so a read-only admin can see who a ticket is assigned to without being able to change it.
- Assigning an open ticket moves it to `in_progress`, matching the store's existing workflow transition.
- Internal notes are never exposed through the account portal.
- Admin mutations use the same fail-closed audit transaction as the rest of the control plane. If the audit write fails, the support mutation fails.
- Replies and notes pass the secret-shaped-content guard before storage.
- Administrators with support management permission maintain saved replies:
  each has a title of at most 120 characters and a body of at most 8,000, and
  each passes the secret-shaped-content guard when it is saved. The reply form
  can insert a saved reply into the reply text, and the reply still passes the
  guard when it is sent.
- The queue shows each request's age, derived in the browser from the request's
  created timestamp.
- A staff reply queues a short notification email when the owning account's
  support-reply preference is on and SMTP is configured. The preference is on
  by default for new and existing accounts, and the account can turn it off
  with the toggle in Security settings. The enqueue runs in the same
  transaction as the reply, so a failed enqueue fails the reply. A preference
  lookup failure fails the reply instead of silently skipping the email. The
  email links to the portal and never contains the reply body.
- A staff reply to a guest request queues the one-request link email in the
  same transaction as the reply and the link row when SMTP is configured. The
  email carries only the link and a short instruction. With mail delivery off,
  the reply is still stored and marked undelivered, and no link is issued.
- When `SESAME_SUPPORT_NOTIFY_EMAIL` is set, a new request and a signed-in
  follow-up queue one notice to that address. The notice carries the reference,
  category, and admin console link, never the subject or message. With no
  address set, nothing is queued and the System workspace reports staff
  notification off.
- Each staff message shows why reply email was or was not sent: delivered,
  waiting to send, failed, guest request, reply email turned off by the
  account, or mail not configured on the deployment. The raw outbox status is
  not shown.
- The ticket header reports whether requester receipts and staff notification
  email are configured, using the mail state returned with the ticket detail
  and without requiring `system:read`.
- Ticket rows behave as keyboard buttons: Enter or Space opens the ticket,
  focus is visible on the row, and the workspace announces loading, notices,
  and errors through an `aria-live` region.
- Read-only and unrelated admin roles cannot mutate support data.

### Database

- Migration `0008_support_workspace.sql` adds conversation messages, internal notes, assignment, priority, timestamps, and the `open | in_progress | waiting | closed` workflow.
- Migration `0009_support_portal.sql` aligns new intake with the workspace and account portal.
- Migration `0019_support_lifecycle.sql` adds unread state, the bounded reopen
  window, and durable email-delivery linkage. Migration
  `0028_support_ticket_category.sql` adds the triage category. Migration
  `0039_support_email_delivery.sql` turns support-reply email on by default,
  applies that to existing rows, and adds the `support-receipt` and
  `support-staff-notify` outbox kinds. Migration
  `0040_support_operations.sql` adds the system-close marker and the saved-reply
  table. Migration
  `0041_support_access_links.sql` adds the one-request link table: only the
  SHA-256 token hash is stored, with the request id, the requester address, the
  expiry, and revocation timestamps. Rows cascade with the request, and the
  hourly purge removes expired and revoked links.
- Ticket ownership is tied to the website account when the requester is signed in. A guest reference number is not an authentication credential.
- Hourly maintenance deletes a closed request and its linked email outbox rows
  90 days after closure. Open, in-progress, and waiting requests are not deleted
  by retention. A waiting request with no activity for 14 days is closed by the
  system: it records no administrator, sends no email, and opens the same
  30-day reopen window. The requester sees the automatic close in the portal.

## Delivery status

Staff replies are visible in the signed-in account portal. Intake queues a
receipt to the requester and, when a staff address is configured, a notice to
that address. A staff reply queues a durable notification when the account's
support-reply preference is on, and the preference defaults on for new and
existing accounts. For a guest request it queues the one-request link email
instead. The worker records pending, delivered, and failed states with bounded
retries. The admin workspace receives a server-computed reason per staff message
instead of the raw outbox status, so a guest request, a turned-off account, and
an unconfigured sender are distinguishable. Portal visibility does not depend
on email delivery, and no notification contains the subject or support message.

The public support flow is suitable for controlled beta testing, not a promise of continuous support. The response expectation shown to requesters is "We aim to reply within 3 business days." There is no attachment handling, live chat, phone support, automatic desktop-log upload, or vault recovery service.

## Release checks still required

- Run every checked-in migration against a fresh PostgreSQL database and an
  upgrade fixture.
- Add end-to-end tests with the real website, API, admin app, and PostgreSQL for guest intake, account ownership, follow-up, assignment, notes, status changes, and audit failure.
- Verify rate limits and secret-shape rejection without logging rejected content.
- Define abuse handling and incident escalation before public launch.
- Exercise keyboard navigation, focus restoration, Narrator, 200% zoom, and narrow layouts in both support interfaces.

## Vault-blind rule

Support is not a recovery path for a local vault. Staff cannot open, reset, inspect, or decrypt one. If a report needs a vault, export, credential, recovery kit, or other secret to reproduce, the report must be redesigned around fictional test data or a redacted diagnostic code.
