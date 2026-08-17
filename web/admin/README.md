# Sesame admin portal

This subtree is the operations interface for the vault-blind Sesame account
API, and it builds on its own. It belongs to the future `sesame-server`
release boundary, yet it stays inside the monorepo during Phase 1.

The portal owns its Node version, package lock, commands, lint and TypeScript
configuration, design-token snapshot, browser tests, and future-repository CI
entry point. Its build does not read the desktop, website, extension, root
design files, or root package metadata.

```powershell
npm ci
$env:VITE_SESAME_API_URL='https://api.test.invalid'
npm run ci
```

The tests use fictional data and intercept API requests. They do not start the
Go API or create an administrator. The deployed portal remains a separate
origin and must receive an HTTPS API origin at build time.
