import { createHash } from 'node:crypto'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import path from 'node:path'

const project = path.resolve(import.meta.dirname, '..')
const recordDirectory = path.join(project, 'node_modules', '.cache', 'shipped-packages')
const nodeModules = '/node_modules/'

export const installPathOf = (id, root = project) => {
  const file = String(id).split('?')[0].split('\\').join('/')
  if (file.startsWith('\0')) return file.slice(1).match(/^(@[^/]+\/[^/]+|[^/@.][^/]*)\//)?.[1] ?? null
  const absolute = path.resolve(root, file).split('\\').join('/')
  const marker = absolute.lastIndexOf(nodeModules)
  if (marker === -1) return null
  const segments = absolute.slice(marker + nodeModules.length).split('/')
  const name = segments[0].startsWith('@') ? segments.slice(0, 2).join('/') : segments[0]
  return path.relative(project, absolute.slice(0, marker + nodeModules.length) + name).split(path.sep).join('/')
}

export const shippedPackages = (build) => {
  const record = path.join(recordDirectory, `${build}.json`)
  let root = project
  return {
    name: 'shipped-packages',
    apply: 'build',
    configResolved(config) {
      root = config.root
    },
    buildStart() {
      rmSync(record, { force: true })
    },
    generateBundle(_options, bundle) {
      const ids = [...this.getModuleIds()]
      for (const item of Object.values(bundle)) {
        if (item.type === 'chunk') ids.push(...item.moduleIds)
        else ids.push(...(item.originalFileNames ?? []))
      }
      const packages = [...new Set(ids.map((id) => installPathOf(id, root)).filter(Boolean))].sort()
      const lockfile = createHash('sha256').update(readFileSync(path.join(project, 'package-lock.json'))).digest('hex')
      mkdirSync(recordDirectory, { recursive: true })
      writeFileSync(record, `${JSON.stringify({ lockfile, packages }, null, 2)}\n`)
    },
  }
}
