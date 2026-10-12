import assert from 'node:assert/strict'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, join, normalize } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const dist = join(root, 'dist')
const html = readFileSync(join(dist, 'index.html'), 'utf8')

const inlineScripts = [...html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/gi)].filter((match) => !/\bsrc=/.test(match[1]) || match[2].trim() !== '')
assert.deepEqual(inlineScripts.map((match) => match[0].slice(0, 80)), [], 'index.html has an inline script, which script-src self refuses')
assert.doesNotMatch(html, /<style\b/i, 'index.html has an inline style element, which style-src self refuses')
assert.doesNotMatch(html, /\sstyle\s*=/i, 'index.html has a style attribute, which style-src self refuses')
assert.doesNotMatch(html, /\son[a-z]+\s*=/i, 'index.html has an inline event handler')

const references = [...html.matchAll(/\b(?:src|href)="([^"]+)"/g)].map((match) => match[1])
assert.ok(references.length >= 2, 'index.html references no script and no stylesheet')
for (const reference of references) {
  assert.match(reference, /^\.\//, `index.html reference ${reference} is not relative`)
  assert.ok(existsSync(join(dist, normalize(reference))), `index.html references ${reference}, which is not in dist`)
}

const assets = readdirSync(join(dist, 'assets'))
for (const file of assets) {
  const text = readFileSync(join(dist, 'assets', file), 'utf8')
  if (file.endsWith('.css')) {
    assert.doesNotMatch(text, /url\(\s*["']?(?:\/|https?:|data:)/, `${file} has a non-relative url()`)
    for (const match of text.matchAll(/url\(\s*["']?([^)"']+)/g)) {
      assert.ok(existsSync(join(dist, 'assets', match[1])), `${file} references ${match[1]}, which is not in dist`)
    }
  }
  if (file.endsWith('.js')) {
    assert.doesNotMatch(text, /\beval\s*\(|new Function\s*\(/, `${file} builds code at run time`)
  }
}
assert.ok(assets.some((file) => file.endsWith('.woff2')), 'dist has no font files')
assert.ok(existsSync(join(dist, 'favicon.svg')), 'dist has no favicon.svg')
assert.ok(!existsSync(join(dist, '_headers')), 'dist must not serve a _headers file')

console.log(`Console build check: ${references.length} relative references, ${assets.length} assets, no inline script or style.`)
