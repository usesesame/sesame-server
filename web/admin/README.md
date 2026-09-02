# Sesame admin portal

This is the operations interface for the vault-blind Sesame account API. It
builds independently of the Go service.

It owns its Node version, lockfile, commands, lint and TypeScript settings,
and design-token snapshot. Its build does not read the desktop, public website,
browser extension, or repository-root package metadata.

```powershell
npm ci
$env:VITE_SESAME_API_URL='https://api.test.invalid'
npm run release:check
```

`npm run ci` runs design-token, lint, and type checks. `npm run release:check`
also creates the production bundle. `npm run test` runs the portal interaction
tests. The deployed portal needs an HTTPS API origin and must use a separate
origin.

See [README-ADMIN.md](README-ADMIN.md) for the current domain and security
boundaries. The generated route inventory is
[openapi/openapi.json](../../openapi/openapi.json).
