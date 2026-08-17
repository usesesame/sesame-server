import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const tokens = readFileSync(join(root, 'design', 'tokens.css'), 'utf8')
const sources = readdirSync(join(root, 'src'), { recursive: true, withFileTypes: true })
  .filter((entry) => entry.isFile() && /\.(?:css|svelte|ts)$/.test(entry.name))
  .map((entry) => readFileSync(join(entry.parentPath, entry.name), 'utf8'))
  .join('\n')
const main = readFileSync(join(root, 'src', 'main.ts'), 'utf8')

const defined = new Set(
  [...`${tokens}\n${sources}`.matchAll(/--([a-z0-9-]+)\s*:/g)].map((match) => match[1]),
)
const usages = [...sources.matchAll(/var\(--([a-z0-9-]+)([^)]*)\)/g)]
const used = new Set(usages.map((match) => match[1]))
const required = new Set(usages.filter((match) => !match[2].includes(',')).map((match) => match[1]))
const missing = [...required].filter((name) => !defined.has(name)).sort()

assert.deepEqual(missing, [], `undefined account portal design tokens: ${missing.join(', ')}`)
assert.match(main, /import ['"]\.\.\/design\/tokens\.css['"]/)
assert.doesNotMatch(main, /\.\.\/\.\.\/design/)
console.log(`Account portal design contract: ${used.size} used tokens resolve inside the repository.`)
