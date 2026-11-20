import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import path from 'node:path'

const project = path.resolve(import.meta.dirname, '..')
const recordDirectory = path.join(project, 'node_modules', '.cache', 'shipped-packages')
const nodeModules = '/node_modules/'

export const buildConfigFiles = ['package.json', 'package-lock.json', 'svelte.config.js', 'tsconfig.json']

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

export const inputPathOf = (id, root = project, base = project) => {
  const file = String(id).split('?')[0]
  if (file.startsWith('\0') || installPathOf(file, root)) return null
  const absolute = path.resolve(root, file)
  if (!existsSync(absolute) || !statSync(absolute).isFile()) return null
  return path.relative(base, absolute).split(path.sep).join('/')
}

const digest = (file) => createHash('sha256').update(readFileSync(file)).digest('hex')

export const buildInputs = (ids, root = project, base = project, configFiles = []) => {
  const inputs = new Set(buildConfigFiles.filter((file) => existsSync(path.join(base, file))))
  for (const id of [...configFiles, ...ids]) {
    const input = inputPathOf(id, root, base)
    if (input) inputs.add(input)
  }
  return [...inputs].sort().map((file) => ({ path: file, sha256: digest(path.join(base, file)) }))
}

export const changedInput = (inputs, loaded) => inputs.find((input) => loaded.has(input.path) && loaded.get(input.path) !== input.sha256)

export const shippedPackages = (build) => {
  const record = path.join(recordDirectory, `${build}.json`)
  const loaded = new Map()
  let root = project
  let configFiles = []
  return {
    name: 'shipped-packages',
    apply: 'build',
    enforce: 'pre',
    configResolved(config) {
      root = config.root
      configFiles = [config.configFile, ...(config.configFileDependencies ?? [])].filter(Boolean)
    },
    buildStart() {
      rmSync(record, { force: true })
      loaded.clear()
    },
    load(id) {
      const input = inputPathOf(id, root, project)
      if (input && !loaded.has(input)) loaded.set(input, digest(path.join(project, input)))
      return null
    },
    generateBundle(_options, bundle) {
      const ids = [...this.getModuleIds()]
      for (const item of Object.values(bundle)) {
        if (item.type === 'chunk') ids.push(...item.moduleIds)
        else ids.push(...(item.originalFileNames ?? []))
      }
      const packages = [...new Set(ids.map((id) => installPathOf(id, root)).filter(Boolean))].sort()
      const inputs = buildInputs(ids, root, project, configFiles)
      const changed = changedInput(inputs, loaded)
      if (changed) this.error(`${changed.path} changed while the ${build} build was running, so its record of bundled packages would not match the files. Run the build again.`)
      mkdirSync(recordDirectory, { recursive: true })
      writeFileSync(record, `${JSON.stringify({ inputs, packages }, null, 2)}\n`)
    },
  }
}
