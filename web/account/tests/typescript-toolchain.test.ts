import { spawnSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { describe, expect, test } from 'vitest'

const projectRoot = path.resolve(import.meta.dirname, '..')
const manifest = JSON.parse(readFileSync(path.join(projectRoot, 'package.json'), 'utf8')) as {
  scripts: Record<string, string>
  dependencies?: Record<string, string>
  devDependencies: Record<string, string>
}
const requireFromProject = createRequire(path.join(projectRoot, 'package.json'))
const nativeTsc = path.join(projectRoot, 'node_modules', '@typescript', 'native', 'bin', 'tsc')

const runNativeTsc = (args: string[]) => spawnSync(process.execPath, [nativeTsc, ...args], { cwd: projectRoot, encoding: 'utf8' })

describe('TypeScript toolchain', () => {
  test('declares the side-by-side compiler aliases', () => {
    expect(manifest.devDependencies.typescript).toBe('npm:@typescript/typescript6@~6.0.2')
    expect(manifest.devDependencies['@typescript/native']).toBe('npm:typescript@^7.0.2')
    expect(manifest.dependencies?.typescript).toBeUndefined()
    expect(manifest.dependencies?.['@typescript/native']).toBeUndefined()
    const lock = JSON.parse(readFileSync(path.join(projectRoot, 'package-lock.json'), 'utf8'))
    expect(lock.packages[''].devDependencies.typescript).toBe(manifest.devDependencies.typescript)
    expect(lock.packages[''].devDependencies['@typescript/native']).toBe(manifest.devDependencies['@typescript/native'])
    expect(manifest.scripts.check).toBe('svelte-check --tsconfig ./tsconfig.json && node ./node_modules/@typescript/native/bin/tsc --noEmit')
  })

  test('runs the native compiler from the check command', () => {
    const nativeManifest = JSON.parse(readFileSync(path.join(projectRoot, 'node_modules', '@typescript', 'native', 'package.json'), 'utf8')) as { version: string }
    expect(nativeManifest.version).toMatch(/^7\./)
    const version = runNativeTsc(['--version'])
    expect(version.status).toBe(0)
    expect(version.stdout.trim()).toMatch(/^Version 7\.\d+\.\d+$/)
  })

  test('keeps every published native platform in the lockfile', () => {
    const installed = JSON.parse(readFileSync(path.join(projectRoot, 'node_modules', '@typescript', 'native', 'package.json'), 'utf8')) as { optionalDependencies: Record<string, string> }
    const lock = JSON.parse(readFileSync(path.join(projectRoot, 'package-lock.json'), 'utf8'))
    expect(Object.keys(installed.optionalDependencies).length).toBeGreaterThan(0)
    expect(lock.packages['node_modules/@typescript/native'].optionalDependencies).toEqual(installed.optionalDependencies)
    for (const [name, version] of Object.entries(installed.optionalDependencies)) {
      const entry = lock.packages[`node_modules/${name}`]
      expect(entry, name).toBeDefined()
      expect(entry.version, name).toBe(version)
      expect(entry.optional, name).toBe(true)
      const [, os, cpu] = name.split('-')
      expect(entry.os, name).toEqual([os])
      expect(entry.cpu, name).toEqual([cpu])
    }
  })

  test('keeps the TypeScript 6 compiler API for Svelte and ESLint', () => {
    const ts = requireFromProject('typescript') as typeof import('typescript')
    expect(ts.versionMajorMinor).toBe('6.0')
    expect(typeof ts.createProgram).toBe('function')
    expect(typeof ts.createSourceFile).toBe('function')
    expect(typeof ts.createLanguageService).toBe('function')
    expect(typeof ts.sys).toBe('object')
    for (const consumer of ['svelte-check', '@typescript-eslint/parser']) {
      const api = createRequire(path.join(projectRoot, 'node_modules', consumer, 'package.json'))('typescript') as typeof import('typescript')
      expect(api.versionMajorMinor, consumer).toBe('6.0')
      expect(typeof api.createProgram, consumer).toBe('function')
    }
  })

  test('propagates native compiler errors', () => {
    const directory = mkdtempSync(path.join(tmpdir(), 'sesame-account-typescript-'))
    try {
      const fixture = path.join(directory, 'type-error.ts')
      writeFileSync(path.join(directory, 'tsconfig.json'), JSON.stringify({ compilerOptions: { noEmit: true, strict: true, target: 'ES2022', lib: ['ES2022'], types: [] }, files: ['type-error.ts'] }))
      writeFileSync(fixture, "export const count: number = 'not a number'\n")
      const result = runNativeTsc(['--project', directory])
      expect(result.status).not.toBe(0)
      expect(`${result.stdout}${result.stderr}`).toContain('error TS2322')
      writeFileSync(fixture, 'export const count: number = 7\n')
      expect(runNativeTsc(['--project', directory]).status).toBe(0)
    } finally {
      rmSync(directory, { recursive: true, force: true })
    }
  })
})
