import { readFile, writeFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const portalRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const source = await readFile(resolve(portalRoot, 'dist', '_headers'), 'utf8')

const directives = []
let inGlobalBlock = false
for (const line of source.split(/\r?\n/)) {
  if (line.trim() === '') continue
  if (!/^\s/.test(line)) {
    inGlobalBlock = line.trim() === '/*'
    continue
  }
  if (!inGlobalBlock) continue
  const header = line.match(/^\s+([A-Za-z][A-Za-z0-9-]*):\s*(.+?)\s*$/)
  if (!header) continue
  const [, name, value] = header
  if (value.includes('__')) throw new Error(`${name} still contains a build placeholder.`)
  directives.push(`add_header ${name} "${value.replaceAll('\\', '\\\\').replaceAll('"', '\\"')}" always;`)
}

if (directives.length === 0) throw new Error('No headers were found in dist/_headers.')

await writeFile(resolve(portalRoot, 'dist', 'security-headers.conf'), `${directives.join('\n')}\n`, 'utf8')
console.log(`Wrote dist/security-headers.conf with ${directives.length} headers.`)
