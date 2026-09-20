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

const cssChunks = (path, text) => {
  if (path.endsWith('.css')) return [text]
  if (path.endsWith('.svelte')) return [...text.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)].map((match) => match[1])
  return []
}

const cssBlocks = (text) => {
  const blocks = []
  const open = []
  let selector = ''
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index]
    if (char === '{') {
      open.push({ selector: selector.trim(), bodyStart: index + 1 })
      selector = ''
    } else if (char === '}') {
      const block = open.pop()
      if (block) blocks.push({ selector: block.selector, body: text.slice(block.bodyStart, index) })
      selector = ''
    } else {
      selector += char
    }
  }
  return blocks
}

const whiteOnTheme = []
for (const file of files) {
  for (const chunk of cssChunks(file.path, file.text)) {
    for (const block of cssBlocks(chunk)) {
      if (!/(?:^|;)\s*color\s*:\s*(#fff\b|#ffffff\b|white\b)/im.test(block.body)) continue
      if (!/(?:^|;)\s*background(?:-color)?\s*:\s*var\(--/im.test(block.body)) continue
      whiteOnTheme.push(`${file.path.slice(root.length + 1)}: ${block.selector.slice(0, 90)}`)
    }
  }
}
assert.deepEqual(whiteOnTheme, [], `hardcoded white over a themed background:\n  ${whiteOnTheme.join('\n  ')}`)

console.log(`Account portal design contract: ${used.size} used tokens resolve inside the repository.`)
