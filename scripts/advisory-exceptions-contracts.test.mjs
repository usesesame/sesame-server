import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'

import {
  applyBuildToolRule,
  bundledPackages,
  evaluateFindings,
  loadExceptions,
  npmFindings,
  shippedState,
  validateExceptions,
} from './audit-dependencies.mjs'

const today = '2026-10-24'

const exception = (overrides = {}) => ({
  id: 'GHSA-xq3m-2v4x-88gg',
  package: 'example-parser',
  reason: 'The parser only reads files the build writes itself.',
  reviewed: '2026-10-20',
  expires: '2026-11-19',
  ...overrides,
})

const npmReport = (via) => ({ auditReportVersion: 2, vulnerabilities: { 'example-parser': { name: 'example-parser', via } } })

const advisory = (overrides = {}) => ({
  source: 1100001,
  name: 'example-parser',
  title: 'Example parser reads past its buffer',
  url: 'https://github.com/advisories/GHSA-xq3m-2v4x-88gg',
  severity: 'high',
  ...overrides,
})

test('the committed exceptions file is valid today', () => {
  assert.doesNotThrow(() => loadExceptions(new Date().toISOString().slice(0, 10)))
})

test('an exception must be well formed', () => {
  assert.throws(() => validateExceptions({}, today), /"entries" array/)
  assert.throws(() => validateExceptions({ entries: [exception({ id: 'CVE-2026-1' })] }, today), /GHSA advisory/)
  assert.throws(() => validateExceptions({ entries: [exception({ reason: ' ' })] }, today), /needs a reason/)
  assert.throws(() => validateExceptions({ entries: [exception({ expires: '2026-02-30' })] }, today), /YYYY-MM-DD/)
  assert.throws(() => validateExceptions({ entries: [{ ...exception(), note: 'extra' }] }, today), /exactly an id/)
  assert.throws(() => validateExceptions({ entries: [exception(), exception()] }, today), /listed twice/)
})

test('an exception lasts at most 30 days from its review and fails once it expires', () => {
  assert.doesNotThrow(() => validateExceptions({ entries: [exception({ reviewed: '2026-10-24', expires: '2026-11-23' })] }, today))
  assert.throws(() => validateExceptions({ entries: [exception({ reviewed: '2026-10-24', expires: '2026-11-24' })] }, today), /within 30 days/)
  assert.throws(() => validateExceptions({ entries: [exception({ reviewed: '2026-10-25', expires: '2026-11-01' })] }, today), /after today/)
  assert.throws(() => validateExceptions({ entries: [exception({ reviewed: '2026-09-30', expires: '2026-10-23' })] }, today), /expired on 2026-10-23/)
  assert.doesNotThrow(() => validateExceptions({ entries: [exception({ reviewed: '2026-09-30', expires: '2026-10-24' })] }, today))
})

test('npm advisories at high and critical fail and lower ones do not', () => {
  const findings = npmFindings(npmReport([advisory(), advisory({ severity: 'moderate', url: 'https://github.com/advisories/GHSA-2222-3333-4444' }), 'example-parent']))
  assert.deepEqual(findings.map(({ id, severity }) => [id, severity]), [['GHSA-xq3m-2v4x-88gg', 'high']])
  assert.equal(npmFindings(npmReport([advisory({ severity: 'critical' })]))[0].severity, 'critical')
})

test('a failed or empty audit fails closed', () => {
  assert.throws(() => npmFindings({ error: { code: 'ENOAUDIT', summary: 'the registry is unreachable' } }), /the registry is unreachable/)
  assert.throws(() => npmFindings({}), /did not return a report/)
})

test('an exception accepts only the advisory and package it names', () => {
  const findings = npmFindings({
    auditReportVersion: 2,
    vulnerabilities: {
      'example-parser': { name: 'example-parser', via: [advisory()] },
      'example-server': { name: 'example-server', via: [advisory({ name: 'example-server', url: 'https://github.com/advisories/GHSA-4444-5555-6666' })] },
    },
  })
  const result = evaluateFindings(findings, [exception(), exception({ id: 'GHSA-4444-5555-6666', package: 'another-server' })])
  assert.deepEqual(result.accepted.map(({ id }) => id), ['GHSA-xq3m-2v4x-88gg'])
  assert.deepEqual(result.blocking.map(({ id, package: name }) => [id, name]), [['GHSA-4444-5555-6666', 'example-server']])
  assert.deepEqual(result.unused.map(({ package: name }) => name), ['another-server'])
})

test('an npm advisory without a GHSA identifier can never be accepted', () => {
  const findings = npmFindings(npmReport([advisory({ url: 'https://example.invalid/advisory' })]))
  assert.equal(findings[0].id, 'npm advisory 1100001')
  assert.equal(evaluateFindings(findings, [exception()]).blocking.length, 1)
})

const globAdvisory = (overrides = {}) => ({
  source: 1100002,
  name: 'example-glob',
  dependency: 'example-glob',
  title: 'Example glob backtracks on a crafted pattern',
  url: 'https://github.com/advisories/GHSA-2222-3333-4444',
  severity: 'high',
  cwe: ['CWE-1333'],
  ...overrides,
})

const globFinding = (overrides = {}) =>
  npmFindings({
    auditReportVersion: 2,
    vulnerabilities: { 'example-glob': { name: 'example-glob', nodes: ['node_modules/example-glob'], via: [globAdvisory(overrides)] } },
  })[0]

const devLockfile = {
  packages: {
    '': { name: 'example-project' },
    'node_modules/example-bundler': { version: '1.0.0', dev: true },
    'node_modules/example-glob': { version: '4.0.0', dev: true },
  },
}

const bundled = (...packages) => ({ packages: new Set(packages) })

test('npm findings keep the weaknesses and installed paths the report names', () => {
  const finding = globFinding({ cwe: ['CWE-1333', 'CWE-400'] })
  assert.deepEqual(finding.cwe, ['CWE-1333', 'CWE-400'])
  assert.deepEqual(finding.nodes, ['node_modules/example-glob'])
  assert.deepEqual(npmFindings(npmReport([advisory()]))[0].cwe, [])
})

test('a resource exhaustion advisory in a build tool that never ships does not fail', () => {
  for (const cwe of [['CWE-1333'], ['CWE-400', 'CWE-405', 'CWE-407', 'CWE-770', 'CWE-834']]) {
    const { buildOnly, remaining } = applyBuildToolRule([globFinding({ cwe })], devLockfile, bundled('node_modules/svelte'))
    assert.deepEqual(buildOnly.map(({ id, package: name }) => [id, name]), [['GHSA-2222-3333-4444', 'example-glob']])
    assert.match(buildOnly[0].reason, /no shipped build bundles it/)
    assert.equal(remaining.length, 0)
  }
})

test('an advisory without a weakness list still fails', () => {
  for (const cwe of [[], undefined, 'CWE-1333']) {
    const { buildOnly, remaining } = applyBuildToolRule([globFinding({ cwe })], devLockfile, bundled())
    assert.equal(buildOnly.length, 0)
    assert.equal(remaining.length, 1)
  }
})

test('an advisory that lists any other weakness still fails', () => {
  for (const other of ['CWE-94', 'CWE-22', 'CWE-1321', 'CWE-506']) {
    const { buildOnly, remaining } = applyBuildToolRule([globFinding({ cwe: ['CWE-1333', other] })], devLockfile, bundled())
    assert.equal(buildOnly.length, 0, `${other} must keep failing`)
    assert.equal(remaining.length, 1)
  }
})

test('an advisory in a package that ships still fails', () => {
  const lockfile = { packages: { ...devLockfile.packages, 'node_modules/example-glob': { version: '4.0.0' } } }
  assert.equal(shippedState(globFinding(), lockfile, bundled()).state, 'shipped')
  assert.equal(applyBuildToolRule([globFinding()], lockfile, bundled()).remaining.length, 1)
  const optional = { packages: { ...devLockfile.packages, 'node_modules/example-glob': { version: '4.0.0', devOptional: true } } }
  assert.equal(shippedState(globFinding(), optional, bundled()).state, 'shipped')
})

test('a development package that a shipped build bundles still fails', () => {
  assert.equal(shippedState(globFinding(), devLockfile, bundled('node_modules/example-glob')).state, 'shipped')
  assert.equal(shippedState(globFinding(), devLockfile, bundled('../node_modules/example-glob')).state, 'shipped')
  assert.equal(shippedState(globFinding(), devLockfile, bundled('example-glob')).state, 'shipped')
  const { buildOnly, remaining } = applyBuildToolRule([globFinding()], devLockfile, bundled('node_modules/example-glob'))
  assert.equal(buildOnly.length, 0)
  assert.match(remaining[0].reason, /a shipped build bundles node_modules\/example-glob/)
})

test('an advisory whose shipped state is unknown still fails', () => {
  const cases = [
    [devLockfile, { unknown: 'no build has recorded what the app build bundles' }],
    [devLockfile, undefined],
    [null, bundled()],
    [{ packages: { '': {} } }, bundled()],
    [{ packages: { ...devLockfile.packages, 'node_modules/example-glob': undefined } }, bundled()],
  ]
  for (const [lockfile, record] of cases) {
    assert.equal(shippedState(globFinding(), lockfile, record).state, 'unknown')
    const { buildOnly, remaining } = applyBuildToolRule([globFinding()], lockfile, record)
    assert.equal(buildOnly.length, 0)
    assert.equal(remaining.length, 1)
  }
  const notInstalled = { ...globFinding(), nodes: [] }
  assert.equal(shippedState(notInstalled, { packages: { '': {} } }, bundled()).state, 'unknown')
  assert.equal(applyBuildToolRule([notInstalled], { packages: { '': {} } }, bundled()).remaining.length, 1)
})

test('a package with one development copy and one shipped copy still fails', () => {
  const lockfile = {
    packages: {
      ...devLockfile.packages,
      'node_modules/example-runtime': { version: '2.0.0' },
      'node_modules/example-runtime/node_modules/example-glob': { version: '3.0.0' },
    },
  }
  const state = shippedState(globFinding(), lockfile, bundled())
  assert.equal(state.state, 'shipped')
  assert.match(state.reason, /node_modules\/example-runtime\/node_modules\/example-glob/)
  assert.equal(applyBuildToolRule([globFinding()], lockfile, bundled()).remaining.length, 1)
  const aliased = { packages: { ...devLockfile.packages, 'node_modules/glob-alias': { name: 'example-glob', version: '4.0.0' } } }
  assert.equal(shippedState(globFinding(), aliased, bundled()).state, 'shipped')
})

test('an exception for an advisory the rule lets through is reported as no longer needed', () => {
  const { buildOnly, remaining } = applyBuildToolRule([globFinding()], devLockfile, bundled())
  const result = evaluateFindings(remaining, [exception({ id: 'GHSA-2222-3333-4444', package: 'example-glob' })])
  assert.equal(buildOnly.length, 1)
  assert.equal(result.blocking.length, 0)
  assert.deepEqual(result.unused.map(({ package: name }) => name), ['example-glob'])
})

const projectWith = (manifest, records = {}) => {
  const directory = mkdtempSync(path.join(tmpdir(), 'shipped-builds-'))
  const lockfileBytes = Buffer.from(JSON.stringify(devLockfile))
  writeFileSync(path.join(directory, 'package-lock.json'), lockfileBytes)
  if (manifest !== undefined) writeFileSync(path.join(directory, 'package.json'), JSON.stringify(manifest))
  const recordDirectory = path.join(directory, 'node_modules', '.cache', 'shipped-packages')
  mkdirSync(recordDirectory, { recursive: true })
  for (const [build, record] of Object.entries(records)) writeFileSync(path.join(recordDirectory, `${build}.json`), typeof record === 'string' ? record : JSON.stringify(record))
  return { directory, lockfileBytes, lockfile: createHash('sha256').update(lockfileBytes).digest('hex') }
}

test('the bundled packages come from a record of every shipped build made from the current lockfile', (t) => {
  const made = []
  t.after(() => made.forEach((directory) => rmSync(directory, { recursive: true, force: true })))
  const check = (manifest, records) => {
    const project = projectWith(manifest, typeof records === 'function' ? {} : records)
    made.push(project.directory)
    if (typeof records === 'function') {
      for (const [build, record] of Object.entries(records(project.lockfile))) {
        writeFileSync(path.join(project.directory, 'node_modules', '.cache', 'shipped-packages', `${build}.json`), JSON.stringify(record))
      }
    }
    return bundledPackages(project.directory, project.lockfileBytes)
  }
  const good = check({ shippedBuilds: ['app', 'content'] }, (lockfile) => ({
    app: { lockfile, packages: ['node_modules/svelte'] },
    content: { lockfile, packages: ['vite'] },
  }))
  assert.deepEqual([...good.packages].sort(), ['node_modules/svelte', 'vite'])
  assert.match(check(undefined, {}).unknown, /package\.json could not be read/)
  assert.match(check({}, {}).unknown, /names no shipped builds/)
  assert.match(check({ shippedBuilds: [] }, {}).unknown, /names no shipped builds/)
  assert.match(check({ shippedBuilds: ['../app'] }, {}).unknown, /names no shipped builds/)
  assert.match(check({ shippedBuilds: ['app', 'content'] }, (lockfile) => ({ app: { lockfile, packages: [] } })).unknown, /the content build/)
  assert.match(check({ shippedBuilds: ['app'] }, { app: { lockfile: 'a'.repeat(64), packages: [] } }).unknown, /different package-lock\.json/)
  assert.match(check({ shippedBuilds: ['app'] }, { app: '{' }).unknown, /the app build/)
  assert.match(check({ shippedBuilds: ['app'] }, (lockfile) => ({ app: { lockfile, packages: [1] } })).unknown, /malformed/)
})

const root = path.resolve(import.meta.dirname, '..')
const recorderCopies = [
  path.join(root, 'scripts', 'shipped-packages.mjs'),
  ...(existsSync(path.join(root, 'web')) ? readdirSync(path.join(root, 'web')).map((name) => path.join(root, 'web', name, 'scripts', 'shipped-packages.mjs')) : []),
].filter((file) => existsSync(file))

test('every shipped build recorder in the repository is the same file', () => {
  assert.ok(recorderCopies.length > 0, 'no shipped build recorder exists, so every build tool advisory fails as unknown')
  const digests = new Set(recorderCopies.map((file) => createHash('sha256').update(readFileSync(file)).digest('hex')))
  assert.equal(digests.size, 1, `the shipped build recorders differ: ${recorderCopies.join(', ')}`)
})

test('the shipped build recorder names the installed package of every bundled module', async () => {
  const { installPathOf } = await import(recorderCopies[0])
  const project = path.resolve(path.dirname(recorderCopies[0]), '..')
  const at = (...parts) => path.join(project, ...parts)
  assert.equal(installPathOf(at('node_modules', 'svelte', 'src', 'index-client.js')), 'node_modules/svelte')
  assert.equal(installPathOf(`${at('node_modules', '@scope', 'pkg', 'dist', 'a.css')}?inline`), 'node_modules/@scope/pkg')
  assert.equal(installPathOf(at('node_modules', 'a', 'node_modules', 'b', 'index.js')), 'node_modules/a/node_modules/b')
  assert.equal(installPathOf('node_modules/font-pkg/files/a.woff2', project), 'node_modules/font-pkg')
  assert.equal(installPathOf('\0vite/modulepreload-polyfill.js'), 'vite')
  assert.equal(installPathOf('\0@scope/runtime/helpers.js'), '@scope/runtime')
  assert.equal(installPathOf('\0commonjsHelpers.js'), null)
  assert.equal(installPathOf(at('src', 'main.ts')), null)
  assert.match(installPathOf(path.join(path.dirname(project), 'node_modules', 'outside', 'index.js')), /node_modules\/outside$/)
})
