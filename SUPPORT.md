# Sesame support system

This describes shipped behaviour, not a proposal.

The support system is implemented across the public website, the vault-blind Go API, and the separate admin application. It accepts text only. It must never receive a vault, password-manager export, password, PIN, recovery kit, backup code, TOTP seed, encryption key, session token, or screenshot containing those values.

This file records the current boundary and the remaining release work. It is not a proposal for an unbuilt admin page.

## Implemented

### Website and account portal

- Guests and signed-in users can create a request through `POST /v1/support/requests`.
- The API rejects attachment content types, unknown fields, oversized content, and common secret-shaped text before storing a request.
- Intake is rate-limited and returns a reference number without exposing ticket contents publicly.
- A signed-in user can list and read only requests owned by that account under `/v1/account/support/*`.
- Signed-in users can add a follow-up to an open request. Closed requests cannot be reopened by the requester.
- The website repeats the no-secrets boundary before intake and follow-up submission.

### Desktop app

- Settings has a "Sesame support" entry that opens the website support form in the system browser, prefilled with only short, user-reviewed fields (app version, a safe diagnostic code, browser-integration status, a safe request ID). No diagnostic file, raw error, vault record, or account token is sent.
- The entry reflects the desktop's own account-link state (Settings > Connections > Sesame account, a separate feature from Sync): linked desktops are told to sign in to the website in the same browser to keep replies with that account; unlinked desktops are told to sign in first if they want a history. The desktop does not know the linked account's email, so it cannot prefill or claim a sign-in on the account's behalf.

### Admin workspace

- `super` and `support` administrators can list, filter, and inspect support requests.
- The users and ticket lists paginate (100 per page, Prev/Next) and search as you type; the backend already returned `page`/`size`/`total`.
- A ticket from a signed-in account shows any desktop currently linked to that account, so staff can tell a linked user from a bare guest email at a glance. This reads the existing desktop-link table; it does not add a new link mechanism.
- Staff can assign or unassign a request, set its priority and status, add an internal note, and add a staff reply. The ticket detail shows the current assignee and, for `super`/`support` roles, a dropdown to reassign or unassign. The dropdown is fed by `GET /v1/admin/support/assignees`, which returns only the `id` and `email` of `super` and `support` administrators (the only targets `AssignTicket` accepts) and requires `support:read`, so a read-only admin can see who a ticket is assigned to without being able to change it.
- Assigning an open ticket moves it to `in_progress`, matching the store's existing workflow transition.
- Internal notes are never exposed through the account portal.
- Admin mutations use the same fail-closed audit transaction as the rest of the control plane. If the audit write fails, the support mutation fails.
- Replies and notes pass the secret-shaped-content guard before storage.
- Read-only and unrelated admin roles cannot mutate support data.

### Database

- Migration `0008_support_workspace.sql` adds conversation messages, internal notes, assignment, priority, timestamps, and the `open | in_progress | waiting | closed` workflow.
- Migration `0009_support_portal.sql` aligns new intake with the workspace and account portal.
- Ticket ownership is tied to the website account when the requester is signed in. A guest reference number is not an authentication credential.

## Delivery status

Staff replies are visible in the signed-in website portal. Outbound email delivery for support replies is **not implemented** and must not be described as sent. Account-action mail for verification and recovery is a separate SMTP-backed system.

The public support flow is suitable for controlled beta testing, not a promise of continuous support. There is no attachment handling, live chat, phone support, automatic desktop-log upload, or vault recovery service.

## Release checks still required

- Run migrations `0001` through `0009` against a fresh PostgreSQL database and an upgrade fixture.
- Add end-to-end tests with the real website, API, admin app, and PostgreSQL for guest intake, account ownership, follow-up, assignment, notes, status changes, and audit failure.
- Verify rate limits and secret-shape rejection without logging rejected content.
- Remove or disable any UI that claims a support reply was emailed until a real delivery result is recorded.
- Define retention, deletion, abuse handling, response expectations, and incident escalation before public launch.
- Exercise keyboard navigation, focus restoration, Narrator, 200% zoom, and narrow layouts in both support interfaces.

## Vault-blind rule

Support is not a recovery path for a local vault. Staff cannot open, reset, inspect, or decrypt one. If a report needs a vault, export, credential, recovery kit, or other secret to reproduce, the report must be redesigned around fictional test data or a redacted diagnostic code.
