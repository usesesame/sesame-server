import { spawnSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createRequire } from 'node:module'
import { expect, test } from 'vitest'

interface Manifest {
  scripts: Record<string, string>
  devDependencies: Record<string, string>
  dependencies?: Record<string, string>
}

interface LockedPackage {
  name?: string
  version?: string
  optional?: boolean
  optionalDependencies?: Record<string, string>
  devDependencies?: Record<string, string>
  os?: string[]
  cpu?: string[]
}

interface Lockfile {
  packages: Record<string, LockedPackage>
}

const root = process.cwd()
const manifest = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8')) as Manifest
const lockfile = JSON.parse(readFileSync(join(root, 'package-lock.json'), 'utf8')) as Lockfile
const nativeRoot = join(root, 'node_modules', '@typescript', 'native')
const nativeTsc = join(nativeRoot, 'bin', 'tsc')

const installedPackage = (path: string) => JSON.parse(readFileSync(join(root, 'node_modules', path, 'package.json'), 'utf8')) as { name: string; version: string; optionalDependencies: Record<string, string> }

test('the manifest aliases TypeScript 7 for tsc and TypeScript 6 for the compiler API', () => {
  expect(manifest.devDependencies['@typescript/native']).toBe('npm:typescript@^7.0.2')
  expect(manifest.devDependencies.typescript).toBe('npm:@typescript/typescript6@~6.0.2')
  expect(manifest.dependencies?.typescript).toBeUndefined()
  expect(manifest.dependencies?.['@typescript/native']).toBeUndefined()
  expect(lockfile.packages[''].devDependencies?.['@typescript/native']).toBe('npm:typescript@^7.0.2')
  expect(lockfile.packages[''].devDependencies?.typescript).toBe('npm:@typescript/typescript6@~6.0.2')
})

test('the lockfile keeps the native compiler for every supported OS and CPU', () => {
  const native = lockfile.packages['node_modules/@typescript/native']
  expect(native?.name).toBe('typescript')
  expect(native?.version).toMatch(/^7\./)
  const published = installedPackage('@typescript/native').optionalDependencies
  expect(Object.keys(published).length).toBeGreaterThan(0)
  expect(native.optionalDependencies).toEqual(published)
  for (const [name, version] of Object.entries(published)) {
    const entry = lockfile.packages[`node_modules/${name}`]
    expect(entry, name).toBeDefined()
    expect(entry.optional).toBe(true)
    expect(entry.version).toBe(version)
    const [, os, cpu] = name.split('-')
    expect(entry.os, name).toEqual([os])
    expect(entry.cpu, name).toEqual([cpu])
  }
})

test('the installed tsc runs TypeScript 7 while svelte-check and typescript-eslint keep the TypeScript 6 API', () => {
  const native = installedPackage('@typescript/native')
  expect(native.name).toBe('typescript')
  expect(native.version).toMatch(/^7\./)

  const version = spawnSync(process.execPath, [nativeTsc, '--version'], { cwd: root, encoding: 'utf8' })
  expect(version.error).toBeUndefined()
  expect(version.status).toBe(0)
  expect(version.stdout.trim()).toMatch(/^Version 7\./)

  const apiPackage = installedPackage('typescript')
  expect(apiPackage.name).toBe('@typescript/typescript6')
  expect(apiPackage.version).toMatch(/^6\./)
  for (const consumer of ['svelte-check', '@typescript-eslint/parser']) {
    const api = createRequire(join(root, 'node_modules', consumer, 'package.json'))('typescript') as { version: string; createProgram?: unknown }
    expect(api.version, consumer).toMatch(/^6\.0\./)
    expect(typeof api.createProgram, consumer).toBe('function')
  }
})

test('the check command keeps both diagnostics and the native compiler fails on type errors', () => {
  expect(manifest.scripts.check).toContain('svelte-check --tsconfig ./tsconfig.json')
  expect(manifest.scripts.check).toContain('node ./node_modules/@typescript/native/bin/tsc --noEmit')
  expect(manifest.scripts['release:check']).toContain('npm run check')

  const directory = mkdtempSync(join(tmpdir(), 'sesame-console-ts7-'))
  try {
    const source = join(directory, 'broken.ts')
    writeFileSync(join(directory, 'tsconfig.json'), JSON.stringify({ compilerOptions: { noEmit: true, strict: true, target: 'ES2022', lib: ['ES2022'], types: [] }, files: ['broken.ts'] }))
    writeFileSync(source, "const retries: number = 'three'\n")
    const result = spawnSync(process.execPath, [nativeTsc, '--project', directory], { cwd: root, encoding: 'utf8' })
    expect(result.status).not.toBe(0)
    expect(`${result.stdout}${result.stderr}`).toMatch(/TS2322/)
    writeFileSync(source, 'const retries: number = 3\n')
    const valid = spawnSync(process.execPath, [nativeTsc, '--project', directory], { cwd: root, encoding: 'utf8' })
    expect(valid.status).toBe(0)
  } finally {
    rmSync(directory, { recursive: true, force: true })
  }
})
