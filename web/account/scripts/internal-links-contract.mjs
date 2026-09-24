// Every internal link in the account portal must resolve to a route the portal
// actually serves. The portal is an nginx static host with no rewrite rules
// beyond the SPA fallback, so a link to a marketing-site path is a dead end.
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const knownRoutes = new Set([
  '/',
  '/account',
  '/login',
  '/register',
  '/support',
  '/forgot-password',
  '/reset-password',
  '/verify-email',
  '/confirm-email-change',
])

function sourceFiles(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name)
    if (entry.isDirectory()) return sourceFiles(path)
    return entry.name.endsWith('.svelte') || entry.name.endsWith('.ts') ? [path] : []
  })
}

const failures = []
let checked = 0
for (const file of sourceFiles(join(root, 'src'))) {
  const source = readFileSync(file, 'utf8')
  for (const match of source.matchAll(/href="(\/[^"#?]*)"/g)) {
    checked += 1
    if (!knownRoutes.has(match[1])) failures.push(`${relative(root, file)}: ${match[1]}`)
  }
}

if (failures.length > 0) {
  console.error('Account portal links must point at a route this portal serves:')
  for (const failure of failures) console.error(`  ${failure}`)
  process.exit(1)
}

console.log(`Account portal link contract: ${checked} internal links resolve to a served route.`)
