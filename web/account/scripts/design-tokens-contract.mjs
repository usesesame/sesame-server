import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const tokens = readFileSync(join(root, 'design', 'tokens.css'), 'utf8')
const files = readdirSync(join(root, 'src'), { recursive: true, withFileTypes: true })
  .filter((entry) => entry.isFile() && /\.(?:css|svelte|ts)$/.test(entry.name))
  .map((entry) => ({ path: join(entry.parentPath, entry.name), text: readFileSync(join(entry.parentPath, entry.name), 'utf8') }))
const sources = files.map((file) => file.text).join('\n')
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

for (const retired of ['--border-input-focus', '--focus-glow', '--field-border-focus']) {
  assert.ok(
    !tokens.includes(`${retired}:`),
    `${retired} is declared again. Focus is --field-ring alone; hover is --field-border-hover.`,
  )
}
assert.match(tokens, /--field-ring:/)
assert.match(tokens, /--field-border-hover:/)

const whiteOnTheme = []
for (const file of files) {
  for (const line of file.text.split('\n')) {
    if (!/color:\s*(#fff\b|#ffffff\b|white\b)/i.test(line)) continue
    if (!/background(-color)?:\s*var\(--/.test(line)) continue
    whiteOnTheme.push(`${file.path.slice(root.length + 1)}: ${line.trim().slice(0, 90)}`)
  }
}
assert.deepEqual(whiteOnTheme, [], `hardcoded white over a themed background:\n  ${whiteOnTheme.join('\n  ')}`)

console.log(`Account portal design contract: ${used.size} used tokens resolve inside the repository.`)
