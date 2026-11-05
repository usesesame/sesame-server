import { spawnSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const appRoot = process.cwd()
const manifest = JSON.parse(readFileSync(path.join(appRoot, 'package.json'), 'utf8')) as {
  scripts: Record<string, string>
  dependencies?: Record<string, string>
  devDependencies: Record<string, string>
}
const requireFromApp = createRequire(path.join(appRoot, 'package.json'))
const nativeTsc = path.join(appRoot, 'node_modules', '@typescript', 'native', 'bin', 'tsc')

const readPackage = (name: string) =>
  JSON.parse(readFileSync(path.join(appRoot, 'node_modules', ...name.split('/'), 'package.json'), 'utf8')) as {
    name: string
    version: string
    optionalDependencies: Record<string, string>
  }

const runTsc = (args: string[], cwd = appRoot) => spawnSync(process.execPath, [nativeTsc, ...args], { cwd, encoding: 'utf8' })

describe('TypeScript toolchain', () => {
  it('pins the native compiler and the TypeScript 6 API behind aliases', () => {
    expect(manifest.dependencies?.typescript).toBeUndefined()
    expect(manifest.dependencies?.['@typescript/native']).toBeUndefined()
    expect(manifest.devDependencies['@typescript/native']).toBe('npm:typescript@^7.0.2')
    expect(manifest.devDependencies.typescript).toBe('npm:@typescript/typescript6@~6.0.2')
    const lock = JSON.parse(readFileSync(path.join(appRoot, 'package-lock.json'), 'utf8'))
    expect(lock.packages[''].devDependencies.typescript).toBe(manifest.devDependencies.typescript)
    expect(lock.packages[''].devDependencies['@typescript/native']).toBe(manifest.devDependencies['@typescript/native'])
  })

  it('runs the TypeScript 7 compiler from check', () => {
    expect(manifest.scripts.check).toBe(
      'svelte-check --tsconfig ./tsconfig.json && node ./node_modules/@typescript/native/bin/tsc --noEmit',
    )
    const version = runTsc(['--version'])
    expect(version.status).toBe(0)
    expect(version.stdout.trim()).toMatch(/^Version 7\./)
  })

  it('keeps the TypeScript 6 compiler API for svelte-check and typescript-eslint', () => {
    const native = readPackage('@typescript/native')
    expect(native.name).toBe('typescript')
    expect(native.version).toMatch(/^7\./)

    const apiPackage = readPackage('typescript')
    expect(apiPackage.name).toBe('@typescript/typescript6')
    expect(apiPackage.version).toMatch(/^6\.0\./)

    const api = requireFromApp('typescript') as Record<string, unknown>
    expect(api.version).toMatch(/^6\.0\./)
    for (const name of ['createProgram', 'createSourceFile', 'getDefaultLibFilePath', 'sys']) {
      expect(api[name]).toBeDefined()
    }
    for (const consumer of ['svelte-check', '@typescript-eslint/parser']) {
      const resolved = createRequire(path.join(appRoot, 'node_modules', consumer, 'package.json'))('typescript') as typeof import('typescript')
      expect(resolved.versionMajorMinor, consumer).toBe('6.0')
      expect(typeof resolved.createProgram, consumer).toBe('function')
    }
  })

  it('keeps every native platform package in the lockfile', () => {
    const lock = JSON.parse(readFileSync(path.join(appRoot, 'package-lock.json'), 'utf8')) as {
      packages: Record<string, { version?: string; optional?: boolean; os?: string[]; cpu?: string[]; optionalDependencies?: Record<string, string> }>
    }
    const native = lock.packages['node_modules/@typescript/native']
    expect(native).toBeDefined()
    const published = readPackage('@typescript/native').optionalDependencies
    const platforms = Object.entries(published)
    expect(platforms.length).toBeGreaterThan(0)
    expect(native.optionalDependencies).toEqual(published)
    for (const [name, version] of platforms) {
      const entry = lock.packages[`node_modules/${name}`]
      expect(entry, name).toBeDefined()
      expect(entry.version, name).toBe(version)
      expect(entry.optional, name).toBe(true)
      const [, os, cpu] = name.split('-')
      expect(entry.os, name).toEqual([os])
      expect(entry.cpu, name).toEqual([cpu])
    }
  })

  it('reports type errors instead of falling back', () => {
    const dir = mkdtempSync(path.join(tmpdir(), 'sesame-ts7-'))
    try {
      const source = path.join(dir, 'fixture.ts')
      writeFileSync(path.join(dir, 'tsconfig.json'), JSON.stringify({ compilerOptions: { noEmit: true, strict: true, target: 'ES2022', lib: ['ES2022'], types: [] }, files: ['fixture.ts'] }))
      writeFileSync(source, 'const answer: number = 42\nexport default answer\n')

      const passed = runTsc(['--project', dir], dir)
      expect(passed.status).toBe(0)

      writeFileSync(source, 'const answer: number = "42"\nexport default answer\n')
      const failed = runTsc(['--project', dir], dir)
      expect(failed.status).not.toBe(0)
      expect(`${failed.stdout}${failed.stderr}`).toContain('TS2322')
    } finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })
})
