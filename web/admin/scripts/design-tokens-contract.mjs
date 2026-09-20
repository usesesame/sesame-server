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

assert.deepEqual(missing, [], `undefined admin design tokens: ${missing.join(', ')}`)
assert.match(main, /import ['"]\.\.\/design\/tokens\.css['"]/)
assert.doesNotMatch(main, /\.\.\/\.\.\/design/)

const app = readFileSync(join(root, 'src', 'App.svelte'), 'utf8')
assert.doesNotMatch(app, />\s*sesame\s*</, 'App.svelte renders the wordmark lowercase; design/tokens.css says Sesame')

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

const selectorParts = (selector) =>
  selector
    .split(',')
    .map((part) => part.trim().replace(/\s+/g, ' '))
    .filter(Boolean)

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

const appCss = readFileSync(join(root, 'src', 'app.css'), 'utf8')
const appBlocks = cssBlocks(appCss)
const fieldFocus = []
for (const block of appBlocks) {
  if (!/:focus/.test(block.selector)) continue
  if (!/\b(input|textarea|select|search-box)\b/.test(block.selector)) continue
  for (const [pattern, name] of [
    [/border-color:\s*var\(--border-input-focus\)/, 'border-color: var(--border-input-focus)'],
    [/border-color:\s*var\(--accent-link\)/, 'border-color: var(--accent-link)'],
    [/box-shadow:\s*var\(--focus-glow\)/, 'box-shadow: var(--focus-glow)'],
    [/outline:\s*\d+px solid/, 'a solid outline'],
  ]) {
    if (pattern.test(block.body)) fieldFocus.push(`${name} on ${block.selector.trim().slice(0, 90)}`)
  }
}
assert.deepEqual(fieldFocus, [], `these field focus rules bypass the shared treatment:\n  ${fieldFocus.join('\n  ')}`)

const silencers = new Map()
for (const block of appBlocks) {
  if (!/box-shadow:\s*none/.test(block.body)) continue
  for (const part of selectorParts(block.selector)) {
    if (!silencers.has(part)) silencers.set(part, block)
  }
}
const unsilenced = []
for (const block of appBlocks) {
  if (!/box-shadow:[^;{}]*var\(--field-ring(-danger)?\)/.test(block.body)) continue
  for (const part of selectorParts(block.selector)) {
    const wrapper = part.match(/^(.+?)(:focus-within|:has\(input:focus\))$/)
    if (!wrapper) continue
    const base = wrapper[1].trim()
    const silencing = [`${base} input:focus`, `${base} input:focus-visible`]
      .map((selector) => silencers.get(selector))
      .find((found) => found !== undefined)
    if (!silencing) unsilenced.push(`app.css rings ${part} without silencing the input inside it`)
  }
}
assert.deepEqual(unsilenced, [], `a field would draw two concentric halos:\n  ${unsilenced.join('\n  ')}`)

console.log(`Admin design contract: ${used.size} used tokens resolve inside the repository.`)
