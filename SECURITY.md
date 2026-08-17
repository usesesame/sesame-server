# Security policy

This repository holds the Sesame server: the vault-blind Go API, the account
portal, and the administration portal. It is part of a password manager in
private beta and has not had an independent security audit.

The rule this service exists to keep: it stores account records and opaque
bytes. It has no vault endpoint and no vault-shaped type.

## Reporting a vulnerability

**Do not open a public issue, discussion, or pull request for a security
problem.** Use GitHub private vulnerability reporting: open the repository's
**Security** tab and choose **Report a vulnerability**.

If you cannot use GitHub, ask for a security contact through the account
portal's support form. Do not put vulnerability details in that first message:
support intake is read by a wider group than an advisory.

### What to include

- What an attacker gains, in one sentence.
- Whether it needs an account, an admin session, or neither.
- The steps to reproduce it, in order, against your own deployment.
- Whether you ran the self-hosted Compose deployment or something else.

### What not to include

Never send a real password, vault, export, recovery kit, TOTP seed, session
cookie, or account token. Use fictional data in every reproduction.

### What happens next

- We acknowledge within 5 working days.
- We give an assessment and a rough timeline within 10 working days.
- We tell you when a fix ships and credit you unless you prefer otherwise.

## Scope

In scope:

- Any endpoint that accepts, stores, or returns something a vault-blind
  service must never hold: a vault, a master password, a PIN, a recovery kit,
  a wrapping key, or a TOTP seed. This is the highest-severity class here.
- Authentication, session handling, CSRF, and the origin rules: reaching an
  account route from an origin that is not the account portal, or an admin
  route from anything but the admin portal.
- Cross-account access: reading, changing, or deleting another account's data.
- Privilege escalation between admin roles, audit-log tampering, or crossing
  from an admin session to account data.
- Desktop link and device tokens, download tickets, passkey ceremonies, and
  the signed capability envelope.
- The self-hosted deployment: a key that is predictable, shared between
  installations, or logged; an admin bootstrap that can be replayed.
- Support intake accepting secret-shaped content it should refuse.

Out of scope:

- Sync. It is built-disabled, reachable from no route, and has its own open
  findings recorded in the desktop repository. Report Sync issues there.
- Missing hardening with no demonstrated impact: header audits, version
  disclosure, rate limits without a working amplification, scanner output.
- Denial of service through traffic volume, and social engineering.

## Safe harbour

We will not pursue or support legal action against research that stays within
the scope above, uses only your own deployment and your own accounts, avoids
accessing or retaining anyone else's data, avoids degrading the service for
others, and gives us reasonable time to ship a fix before public disclosure.

## Known limits

- Sesame has not had an independent security audit.
- The self-hosted deployment runs over loopback HTTP by default. A deployment
  behind TLS must set the two session-secure settings itself.
- Container base images are pinned by tag, not digest.
