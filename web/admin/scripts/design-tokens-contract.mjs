import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const tokens = readFileSync(join(root, 'design', 'tokens.css'), 'utf8')
const app = readFileSync(join(root, 'src', 'app.css'), 'utf8')
const main = readFileSync(join(root, 'src', 'main.ts'), 'utf8')

const defined = new Set(
  [...`${tokens}\n${app}`.matchAll(/--([a-z0-9-]+)\s*:/g)].map((match) => match[1]),
)
const used = new Set([...app.matchAll(/var\(--([a-z0-9-]+)/g)].map((match) => match[1]))
const missing = [...used].filter((name) => !defined.has(name)).sort()

assert.deepEqual(missing, [], `undefined admin design tokens: ${missing.join(', ')}`)
assert.match(main, /import ['"]\.\.\/design\/tokens\.css['"]/)
assert.doesNotMatch(main, /\.\.\/\.\.\/design/)
console.log(`Admin design contract: ${used.size} used tokens resolve inside the repository.`)
