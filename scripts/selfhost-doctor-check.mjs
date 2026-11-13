import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import {
  chmodSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, readlinkSync, rmSync, symlinkSync, writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { readSelfHostingDocument, sectionBlocks } from './selfhost-doc.mjs'

const repositoryRoot = dirname(dirname(fileURLToPath(import.meta.url)))
const runGo = join(repositoryRoot, 'scripts', 'run-go.mjs')
const binaryVersion = '0.0.0-doctor-journey'
const binaryCommit = '0123456789abcdef0123456789abcdef01234567'
const configMarker = 'journey-fictional-config-password'
const environmentMarker = 'journey-fictional-environment-password'
const keyMarker = 'JOURNEY-FICTIONAL-KEY-MATERIAL!!'
const databaseMarker = 'journey-fictional-database-content-not-sqlite'
const secretNames = ['signing.key', 'admin-encryption.key', 'ip-pepper.key']
const serviceNames = ['docker', 'docker-compose', 'podman', 'caddy', 'tailscale', 'systemctl', 'service', 'curl', 'wget', 'ps', 'ss', 'netstat', 'sqlite3']
const checkIDs = ['configuration', 'origin', 'listen', 'proxy_trust', 'config_file', 'data_directory', 'secrets', 'database', 'space']
const reportStates = new Set(['ok', 'action', 'unknown'])

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main(process.argv.slice(2)).catch((error) => {
    console.error(`Doctor check failed: ${error.stack ?? error}`)
    process.exitCode = 1
  })
}

async function main(args) {
  const options = parseArguments(args)
  const workRoot = mkdtempSync(join(options.tempRoot || tmpdir(), 'sesame-doctor-check-'))
  let passed = 0
  let unavailable = 0
  const check = async (name, action) => {
    const value = await action()
    passed += 1
    console.log(`ok - ${name}`)
    return value
  }
  const skip = (name, reason) => {
    unavailable += 1
    console.log(`not run - ${name}: ${reason}`)
  }
  try {
    const binary = await check('the server builds with the doctor command', async () => buildBinary(options, workRoot))
    const version = await check('the binary reports the journey version', async () => readToolVersion(binary, options.binary ? '' : binaryVersion))
    const document = readSelfHostingDocument()
    const guide = parseGuide(document)
    const shim = createServiceShims(workRoot)
    const primary = createFixture(workRoot, 'service')
    const primaryBefore = snapshotTree(primary.data)
    const service = serviceEnvironment(primary, shim)

    const firstProbe = run(binary, ['doctor', '--json'], service)
    assert.equal(
      firstProbe.status === 2 && firstProbe.stdout === '' && /unknown command "doctor"/.test(firstProbe.stderr),
      false,
      'the built binary has no doctor command, so the native head journey is blocked until the core CLI lands',
    )

    const documented = await check('the documented native command prints the offline report', async () => {
      const report = runDocumentedCommand(guide.native, binary, primary, service)
      assert.equal(report.toolVersion, version)
      assert.equal(report.config.publicUrl, 'https://sesame.example.test')
      assert.equal(report.config.publicUrlSource, 'config_file')
      assert.equal(report.config.listenScope, 'loopback')
      assert.equal(report.config.listenPort, 8787)
      assert.deepEqual(report.config.trustedProxies, ['127.0.0.1/32'])
      assert.equal(report.config.dataDirectorySource, 'environment')
      assert.equal(report.config.configFile, 'loaded')
      assertCheck(report, 'configuration', 'ok', 'configuration_valid')
      assertCheck(report, 'origin', 'ok', 'origin_https')
      assertCheck(report, 'listen', 'ok', 'listen_loopback')
      assertCheck(report, 'proxy_trust', 'ok', 'proxy_trust_configured')
      assertCheck(report, 'config_file', 'ok', 'config_file_private')
      assertCheck(report, 'data_directory', 'ok', 'data_directory_private')
      assertCheck(report, 'secrets', 'ok', 'secrets_metadata_ok')
      assertCheck(report, 'database', 'unknown', 'database_not_opened')
      const space = findCheck(report, 'space')
      assert.ok(['space_sufficient', 'space_tight', 'space_unavailable'].includes(space.code), 'the valid fixture reported critical free space')
      assert.ok(['ok', 'unknown'].includes(space.state), 'the valid fixture reported critical free space')
      return report
    })
    assert.deepEqual(snapshotTree(primary.data), primaryBefore, 'the documented doctor command changed the data tree')
    assertNoRuntimeArtifacts(primary.data)
    await check('the human summary uses the same fixed messages as the JSON report', async () => {
      const before = snapshotTree(primary.data)
      const result = run(binary, ['doctor'], service)
      assert.equal(result.status, 3, result.stderr)
      assert.equal(result.signal, null)
      assert.equal(result.stdout.trimStart().startsWith('{'), false, 'human mode printed JSON')
      assertSafe(result.stdout + result.stderr, 'the human summary', primary.data)
      const collapsed = collapse(result.stdout)
      for (const check of documented.checks) {
        assert.ok(collapsed.includes(collapse(check.summary)), `the human summary omits ${check.id}`)
        if (check.next !== null) assert.ok(collapsed.includes(collapse(check.next)), `the human summary omits the next step for ${check.id}`)
      }
      assert.match(collapsed, /unknown/i, 'the human summary hides the unknown state')
      assert.equal(result.stdout.includes('https://sesame.example.test'), false, 'the human summary dumped the public address')
      assert.equal(result.stdout.includes('127.0.0.1/32'), false, 'the human summary dumped a proxy range')
      assert.deepEqual(snapshotTree(primary.data), before, 'human mode changed the data tree')
      assertNoRuntimeArtifacts(primary.data)
    })

    await check('invalid configuration is one safe action finding with config null', async () => {
      const fixture = createFixture(workRoot, 'invalid-config')
      writeFileSync(fixture.configFile, `{"publicUrl":"https://sesame.example.test","smtp":{"password":"${configMarker}"`, { mode: 0o600 })
      chmodSync(fixture.configFile, 0o600)
      const { report, result } = runJsonCase({
        label: 'invalid configuration', binary, dataDir: fixture.data, environment: serviceEnvironment(fixture, shim), expectedExit: 1, expectedOverall: 'action', ids: ['configuration'],
      })
      assert.equal(report.config, null)
      assertCheck(report, 'configuration', 'action', 'configuration_invalid')
      assertSafe(result.stdout + result.stderr, 'invalid configuration', fixture.data)
    })

    if (process.platform === 'win32') {
      skip('broad config file permissions are an action without a credential claim', 'POSIX mode bits are not available on Windows')
    } else {
      await check('a valid configuration above the report bounds is one safe action finding', async () => {
      const fixture = createFixture(workRoot, 'oversized-config')
      const settings = JSON.parse(readFileSync(fixture.configFile, 'utf8'))
      settings.trustedProxies = Array.from({ length: 65 }, (_, index) => `10.0.0.${index}/32`)
      writeFileSync(fixture.configFile, JSON.stringify(settings), { mode: 0o600 })
      chmodSync(fixture.configFile, 0o600)
      const { report } = runJsonCase({
        label: 'oversized configuration', binary, dataDir: fixture.data, environment: serviceEnvironment(fixture, shim), expectedExit: 1, expectedOverall: 'action', ids: ['configuration'],
      })
      assert.equal(report.config, null)
      assertCheck(report, 'configuration', 'action', 'configuration_report_limit')
    })

    await check('broad config file permissions are an action without a credential claim', async () => {
        const fixture = createFixture(workRoot, 'broad-config')
        chmodSync(fixture.configFile, 0o644)
        const before = snapshotTree(fixture.data)
        const { report } = runJsonCase({
          label: 'broad config permissions', binary, dataDir: fixture.data, environment: serviceEnvironment(fixture, shim), expectedExit: 1, expectedOverall: 'action',
        })
        assertCheck(report, 'config_file', 'action', 'config_file_permissions')
        assert.deepEqual(snapshotTree(fixture.data), before, 'the permission case changed the data tree')
      })
    }

    await check('a wide proxy range is an action that matches the existing width rule', async () => {
      const fixture = createFixture(workRoot, 'wide-proxy')
      const { report } = runJsonCase({
        label: 'wide proxy', binary, dataDir: fixture.data, environment: serviceEnvironment(fixture, shim, { SESAME_TRUSTED_PROXIES: '10.0.0.0/8' }), expectedExit: 1, expectedOverall: 'action',
      })
      assertCheck(report, 'proxy_trust', 'action', 'proxy_trust_wide')
    })

    await check('an all-interface listen stays unknown because publication is invisible', async () => {
      const fixture = createFixture(workRoot, 'all-interfaces')
      const { report } = runJsonCase({
        label: 'all interfaces', binary, dataDir: fixture.data, environment: serviceEnvironment(fixture, shim, { SESAME_ADDR: '0.0.0.0:8787' }), expectedExit: 3, expectedOverall: 'unknown',
      })
      assertCheck(report, 'listen', 'unknown', 'listen_all_interfaces')
      assert.equal(report.config.listenScope, 'all')
    })

    await check('a missing database is unknown and creates nothing', async () => {
      const fixture = createFixture(workRoot, 'missing-database')
      rmSync(fixture.database)
      const { report } = runJsonCase({
        label: 'missing database', binary, dataDir: fixture.data, environment: serviceEnvironment(fixture, shim), expectedExit: 3, expectedOverall: 'unknown',
      })
      assertCheck(report, 'database', 'unknown', 'database_missing')
      assert.equal(existsSync(fixture.database), false, 'the doctor created a missing database')
    })

    await check('an empty database is an action and is left unopened', async () => {
      const fixture = createFixture(workRoot, 'empty-database')
      writeFileSync(fixture.database, '', { mode: 0o600 })
      chmodSync(fixture.database, 0o600)
      const { report } = runJsonCase({
        label: 'empty database', binary, dataDir: fixture.data, environment: serviceEnvironment(fixture, shim), expectedExit: 1, expectedOverall: 'action',
      })
      assert.equal(findCheck(report, 'database').state, 'action')
      assert.notEqual(findCheck(report, 'database').code, 'database_not_opened')
    })

    await check('a database with group or other permission bits is an action', async () => {
      const fixture = createFixture(workRoot, 'broad-database')
      chmodSync(fixture.database, 0o644)
      const { report } = runJsonCase({
        label: 'broad database permissions', binary, dataDir: fixture.data, environment: serviceEnvironment(fixture, shim), expectedExit: 1, expectedOverall: 'action',
      })
      assertCheck(report, 'database', 'action', 'database_permissions')
    })

    await check('space remains unknown when a linked database size is not inspected', async () => {
      const fixture = createFixture(workRoot, 'linked-database')
      const target = join(workRoot, 'fictional-linked-database')
      const contents = 'fictional linked database contents'
      writeFileSync(target, contents, { mode: 0o600 })
      rmSync(fixture.database)
      symlinkSync(target, fixture.database)
      const before = snapshotTree(fixture.data)
      const { report } = runJsonCase({
        label: 'linked database', binary, dataDir: fixture.data, environment: serviceEnvironment(fixture, shim), expectedExit: 1, expectedOverall: 'action',
      })
      assertCheck(report, 'database', 'action', 'database_symlink')
      assertCheck(report, 'space', 'unknown', 'space_database_unknown')
      assert.deepEqual(snapshotTree(fixture.data), before, 'the linked database case changed the data tree')
      assert.equal(readFileSync(target, 'utf8'), contents, 'the linked database target changed')
    })

    await check('a key with the wrong size is an action and is not replaced', async () => {
      const fixture = createFixture(workRoot, 'short-key')
      const path = join(fixture.secrets, 'signing.key')
      writeFileSync(path, keyMarker.slice(0, 31), { mode: 0o600 })
      chmodSync(path, 0o600)
      const before = snapshotTree(fixture.data)
      const { report } = runJsonCase({
        label: 'short key', binary, dataDir: fixture.data, environment: serviceEnvironment(fixture, shim), expectedExit: 1, expectedOverall: 'action',
      })
      assertCheck(report, 'secrets', 'action', 'secrets_size')
      assert.deepEqual(snapshotTree(fixture.data), before, 'the short-key case changed the data tree')
    })

    await check('missing instance material next to a database is an action and is not regenerated', async () => {
      const fixture = createFixture(workRoot, 'missing-secret')
      rmSync(join(fixture.secrets, 'signing.key'))
      const before = snapshotTree(fixture.data)
      const { report } = runJsonCase({
        label: 'missing secret', binary, dataDir: fixture.data, environment: serviceEnvironment(fixture, shim), expectedExit: 1, expectedOverall: 'action',
      })
      assert.equal(findCheck(report, 'secrets').state, 'action')
      assert.equal(existsSync(join(fixture.secrets, 'signing.key')), false, 'the doctor generated a replacement key')
      assert.deepEqual(snapshotTree(fixture.data), before, 'the missing-secret case changed the data tree')
    })

    if (!process.getuid || process.getuid() === 0) {
      skip('an unreadable key does not stop the metadata report', 'the check needs a non-root POSIX account')
    } else {
      await check('an unreadable key does not stop the metadata report', async () => {
        const fixture = createFixture(workRoot, 'unreadable-key')
        const path = join(fixture.secrets, 'signing.key')
        chmodSync(path, 0o000)
        const before = snapshotTree(fixture.data)
        const { report } = runJsonCase({
          label: 'unreadable key', binary, dataDir: fixture.data, environment: serviceEnvironment(fixture, shim), expectedExit: 3, expectedOverall: 'unknown',
        })
        assertCheck(report, 'secrets', 'ok', 'secrets_metadata_ok')
        assert.deepEqual(snapshotTree(fixture.data), before, 'the unreadable-key case changed the data tree')
        chmodSync(path, 0o600)
      })
    }

    if (process.platform === 'win32') {
      skip('a FIFO substituted for a key is not opened and does not hang', 'mkfifo is not available on Windows')
    } else {
      await check('a FIFO substituted for a key is not opened and does not hang', async () => {
        const fixture = createFixture(workRoot, 'fifo-key')
        const path = join(fixture.secrets, 'ip-pepper.key')
        rmSync(path)
        const made = spawnSync('mkfifo', ['-m', '0600', path], { encoding: 'utf8' })
        assert.equal(made.status, 0, `mkfifo failed: ${made.stderr}`)
        const before = snapshotTree(fixture.data)
        const started = Date.now()
        const { report } = runJsonCase({
          label: 'FIFO key', binary, dataDir: fixture.data, environment: serviceEnvironment(fixture, shim), expectedExit: 1, expectedOverall: 'action', timeout: 15000,
        })
        assert.ok(Date.now() - started < 15000, 'the FIFO key case did not finish within the timeout')
        assert.equal(findCheck(report, 'secrets').state, 'action')
        assert.deepEqual(snapshotTree(fixture.data), before, 'the FIFO key case changed the data tree')
      })
    }

    if (process.platform === 'win32') {
      skip('a symlinked config file leaves the file check unknown', 'creating a symlink needs extra privilege on Windows')
    } else {
      await check('a symlinked config file leaves the file check unknown', async () => {
        const fixture = createFixture(workRoot, 'symlink-config')
        const external = join(workRoot, 'external-config.json')
        writeFileSync(external, JSON.stringify({
          publicUrl: 'https://sesame.example.test',
          addr: '127.0.0.1:8787',
          trustedProxies: ['127.0.0.1/32'],
          smtp: { addr: 'smtp.example.test:587', username: 'fixture-user', password: configMarker, from: 'fixture@example.test' },
        }), { mode: 0o600 })
        rmSync(fixture.configFile)
        symlinkSync(external, fixture.configFile)
        const before = snapshotTree(fixture.data)
        const { report } = runJsonCase({
          label: 'symlink config', binary, dataDir: fixture.data, environment: serviceEnvironment(fixture, shim), expectedExit: 3, expectedOverall: 'unknown', forbidden: [external],
        })
        assertCheck(report, 'config_file', 'unknown')
        assert.equal(report.config.configFile, 'loaded')
        assert.deepEqual(snapshotTree(fixture.data), before, 'the symlink config case changed the data tree')
        assert.equal(lstatSync(fixture.configFile).isSymbolicLink(), true, 'the doctor replaced the config symlink')
      })
    }

    await check('a missing data directory is unknown and is not created', async () => {
      const missing = join(workRoot, 'missing-data')
      const { report } = runJsonCase({
        label: 'missing data directory', binary, dataDir: missing, environment: plainEnvironment(shim, { SESAME_DATA_DIR: missing }), expectedExit: 3, expectedOverall: 'unknown',
      })
      assert.equal(report.config.publicUrl, 'http://localhost:8787')
      assert.equal(report.config.publicUrlSource, 'default')
      assert.equal(report.config.dataDirectorySource, 'environment')
      assert.equal(report.config.configFile, 'absent')
      assertCheck(report, 'origin', 'ok', 'origin_local')
      assertCheck(report, 'listen', 'ok', 'listen_loopback')
      assertCheck(report, 'proxy_trust', 'ok', 'proxy_trust_empty')
      assertCheck(report, 'config_file', 'ok', 'config_file_absent')
      assertCheck(report, 'data_directory', 'unknown', 'data_directory_missing')
      assertCheck(report, 'database', 'unknown', 'database_missing')
      assertCheck(report, 'space', 'unknown', 'space_unavailable')
      assert.equal(existsSync(missing), false, 'the doctor created a missing data directory')
    })

    await check('usage errors exit 2 with empty stdout and no report', async () => {
      const before = snapshotTree(primary.data)
      const cases = [
        ['--network before --json', ['doctor', '--network', '--json']],
        ['--network after --json', ['doctor', '--json', '--network']],
        ['attached --json value', ['doctor', '--json=true']],
        ['repeated --json', ['doctor', '--json', '--json']],
        ['positional argument', ['doctor', 'extra']],
        ['unknown option', ['doctor', '--repair']],
        ['help before unknown option', ['doctor', '--help', '--bogus']],
        ['help before positional argument', ['doctor', '--help', 'extra']],
        ['help before network', ['doctor', '--help', '--network']],
        ['help before repeated json', ['doctor', '--help', '--json', '--json']],
        ['repeated help', ['doctor', '--help', '-h']],
      ]
      for (const [label, arguments_] of cases) {
        const result = run(binary, arguments_, service)
        assert.equal(result.status, 2, `${label} did not exit 2`)
        assert.equal(result.signal, null)
        assert.equal(result.stdout, '', `${label} printed to stdout`)
        assert.ok(result.stderr.trim(), `${label} printed no error`)
        assert.match(result.stderr, /network|repair|json|argument|help|usage/i, `${label} did not explain the usage error`)
        assertSafe(result.stderr, label, primary.data)
      }
      assert.deepEqual(snapshotTree(primary.data), before, 'a usage error changed the data tree')
      assertNoRuntimeArtifacts(primary.data)
    })

    await check('signals interrupt blocked report output without changing instance data', async () => {
      const result = spawnSync(process.execPath, [runGo, 'test', '-count=1', '-run', '^TestDoctorStopsOnSignalWithBlockedOutput$', './cmd/sesame-server'], {
        cwd: repositoryRoot,
        env: { ...process.env, SESAME_DOCTOR_JOURNEY_BINARY: binary },
        encoding: 'utf8',
        timeout: 120000,
      })
      if (result.error) throw result.error
      assert.equal(result.status, 0, result.stderr || result.stdout)
    })

    await check('doctor --help exits 0 and names only the supported option', async () => {
      const result = run(binary, ['doctor', '--help'], service)
      assert.equal(result.status, 0, result.stderr)
      assert.equal(result.signal, null)
      assert.match(result.stdout, /doctor/)
      assert.match(result.stdout, /--json/)
      assert.doesNotMatch(result.stdout, /--network|--repair|--data-dir|--config|--env-file/)
      assertSafe(result.stdout + result.stderr, 'the doctor help', primary.data)
    })

    await check('no run changed the live fixture or invoked a service command', async () => {
      assert.deepEqual(snapshotTree(primary.data), primaryBefore, 'the live fixture changed')
      assert.equal(existsSync(shim.log), false, `a doctor run invoked a service command: ${shim.read()}`)
    })

    await check('the report stays within the documented bounds and privacy rules', async () => {
      const text = JSON.stringify(documented)
      assert.ok(Buffer.byteLength(documented.config.publicUrl) <= 2048)
      assert.ok(documented.config.trustedProxies.length <= 64)
      assert.doesNotMatch(text, /SESAME_[A-Z]/)
      assert.doesNotMatch(text, /BEGIN [A-Z ]*PRIVATE KEY/)
      assert.equal(text.includes(workRoot), false, 'the report holds an absolute work path')
      assert.equal(text.includes(configMarker), false)
      assert.equal(text.includes(environmentMarker), false)
      assert.equal(text.includes(keyMarker), false)
      assert.equal(text.includes(databaseMarker), false)
    })

    assert.equal(unavailable, 0, 'Required doctor checks were unavailable. Run this journey on Linux as a non-root user.')
    console.log(`Doctor check passed: ${passed} checks, ${unavailable} not run.`)
    console.log('The database fixture is deliberate non-SQLite data and is never opened, so its presence and size are not integrity proof.')
    console.log('Not run by this native check: Docker or Compose execution, a real service process, PostgreSQL, proxies, Tailscale, Windows and macOS runtime, ACLs and a real desktop pairing.')
  } finally {
    if (options.keep) console.log(`Kept ${workRoot}`)
    else rmSync(workRoot, { recursive: true, force: true })
  }
}

function parseArguments(args) {
  const parsed = { binary: '', tempRoot: '', keep: false }
  for (let index = 0; index < args.length; index += 1) {
    const argument = args[index]
    if (argument === '--binary') parsed.binary = value(args, ++index, argument)
    else if (argument === '--temp-root') parsed.tempRoot = value(args, ++index, argument)
    else if (argument === '--keep') parsed.keep = true
    else throw new Error(`Unknown argument ${argument}. Use --binary <path>, --temp-root <dir> or --keep.`)
  }
  return parsed
}

function value(args, index, name) {
  const argument = args[index]
  assert.ok(argument && !argument.startsWith('-'), `${name} needs a value`)
  return resolve(argument)
}

function buildBinary(options, workRoot) {
  if (options.binary) {
    assert.ok(existsSync(options.binary), `${options.binary} does not exist`)
    return options.binary
  }
  assert.ok(existsSync(join(repositoryRoot, 'internal', 'selfhost', 'console', 'dist', 'index.html')), 'the embedded console index is missing')
  const output = join(workRoot, 'sesame-server')
  const ldflags = `-s -w -X usesesame.app/backend/internal/buildinfo.Version=${binaryVersion} -X usesesame.app/backend/internal/buildinfo.Commit=${binaryCommit}`
  const result = spawnSync(process.execPath, [runGo, 'build', '-trimpath', `-ldflags=${ldflags}`, '-o', output, './cmd/sesame-server'], {
    cwd: repositoryRoot,
    env: { ...process.env, CGO_ENABLED: '0' },
    encoding: 'utf8',
    timeout: 300000,
  })
  if (result.error) throw result.error
  assert.equal(result.status, 0, `the server build failed:\n${result.stderr || result.stdout}`)
  return output
}

function readToolVersion(binary, requiredVersion) {
  const result = run(binary, ['version'])
  assert.equal(result.status, 0, result.stderr)
  const match = result.stdout.match(/^sesame-server (\S+) \(([^)]*)\)$/m)
  assert.ok(match, `the version output is not the expected line: ${result.stdout}`)
  if (requiredVersion) assert.equal(match[1], requiredVersion)
  return match[1]
}

function parseGuide(document) {
  const blocks = sectionBlocks(document, '## Diagnose an installation')
  assert.equal(blocks.length, 3, 'the doctor section must hold the native, running container and one-off commands')
  const native = blocks[0]
  assert.ok(native.includes('SESAME_DATA_DIR=/var/lib/sesame'), 'the native doctor command does not set the data directory')
  assert.ok(native.includes('SESAME_CONFIG_FILE=/var/lib/sesame/config.json'), 'the native doctor command does not set the config file')
  assert.ok(native.includes('/usr/local/bin/sesame-server doctor --json'), 'the native doctor command is not the documented one')
  assert.equal(blocks[1], 'docker compose exec -T sesame /sesame-server doctor --json')
  assert.equal(blocks[2], 'docker compose run --rm --no-deps -T sesame doctor --json')
  return { native }
}

function runDocumentedCommand(block, binary, fixture, environment) {
  const command = block
    .replaceAll('/var/lib/sesame', fixture.data)
    .replaceAll('/usr/local/bin/sesame-server', binary)
  assert.equal(command.includes('/var/lib/sesame'), false, 'the journey did not replace the documented data path')
  const before = snapshotTree(fixture.data)
  const result = spawnSync('bash', ['-euo', 'pipefail', '-c', command], {
    cwd: repositoryRoot,
    env: environment,
    encoding: 'utf8',
    maxBuffer: 16 * 1024 * 1024,
    timeout: 60000,
  })
  if (result.error) throw result.error
  assert.equal(result.status, 3, `the documented command exited ${result.status}: ${result.stderr}`)
  assert.equal(result.signal, null)
  assert.equal(result.stderr, '', `the documented command wrote to stderr: ${result.stderr}`)
  assertSafe(result.stdout, 'the documented command', fixture.data)
  const report = parseDoctorReport(result.stdout, 'the documented command')
  assert.equal(report.overall, 'unknown')
  assert.deepEqual(snapshotTree(fixture.data), before, 'the documented command changed the data tree')
  assertNoRuntimeArtifacts(fixture.data)
  return report
}

function runJsonCase({ label, binary, dataDir, environment, expectedExit, expectedOverall, ids, timeout, forbidden = [] }) {
  const before = snapshotTree(dataDir)
  const result = run(binary, ['doctor', '--json'], environment, { timeout })
  assert.equal(result.status, expectedExit, `${label} exited ${result.status}: ${result.stderr}`)
  assert.equal(result.signal, null, `${label} ended on a signal`)
  assertSafe(result.stdout + result.stderr, label, dataDir, forbidden)
  assertNoRuntimeArtifacts(dataDir)
  assert.deepEqual(snapshotTree(dataDir), before, `${label} changed the data tree`)
  const report = parseDoctorReport(result.stdout, label)
  assert.equal(report.overall, expectedOverall, `${label} overall`)
  if (ids) assert.deepEqual(report.checks.map((check) => check.id), ids, `${label} check order`)
  else assert.deepEqual(report.checks.map((check) => check.id), checkIDs, `${label} check order`)
  return { report, result }
}

function parseDoctorReport(stdout, label) {
  assert.ok(stdout.endsWith('\n'), `${label} does not end with a newline`)
  let report
  try {
    report = JSON.parse(stdout)
  } catch (error) {
    assert.fail(`${label} does not hold one JSON object: ${error.message}`)
  }
  assert.ok(report && typeof report === 'object' && !Array.isArray(report), `${label} is not an object`)
  assert.deepEqual(Object.keys(report).sort(), ['checks', 'config', 'generatedAt', 'limitations', 'mode', 'overall', 'schemaVersion', 'toolVersion'])
  assert.equal(report.schemaVersion, 1)
  assert.match(report.toolVersion, /^[\x20-\x7e]{1,128}$/)
  assert.match(report.generatedAt, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/, `${label} generatedAt is not seconds-precision UTC`)
  assert.equal(new Date(report.generatedAt).toISOString().replace('.000Z', 'Z'), report.generatedAt)
  assert.equal(report.mode, 'offline')
  assert.ok(reportStates.has(report.overall), `${label} overall is not a report state`)
  assert.ok(Array.isArray(report.checks) && report.checks.length > 0, `${label} checks is not a non-empty array`)
  assert.ok(Array.isArray(report.limitations) && report.limitations.length >= 8, `${label} limitations is incomplete`)
  assert.equal(new Set(report.limitations).size, report.limitations.length, `${label} repeats a limitation`)
  for (const limitation of report.limitations) assert.match(limitation, /^[a-z][a-z0-9_]*$/)
  const limitations = report.limitations.join(' ')
  for (const word of ['clock', 'database', 'network', 'service', 'write']) assert.match(limitations, new RegExp(word), `${label} limitations omit ${word}`)
  assert.match(limitations, /key|secret/, `${label} limitations omit key contents`)
  assert.match(limitations, /bind|port/, `${label} limitations omit bindability`)
  assert.match(limitations, /proxy|host/, `${label} limitations omit the proxy path`)
  for (const check of report.checks) {
    assert.deepEqual(Object.keys(check).sort(), ['code', 'id', 'next', 'state', 'summary'])
    assert.match(check.id, /^[a-z][a-z0-9_]*$/)
    assert.ok(reportStates.has(check.state), `${label} ${check.id} state`)
    assert.match(check.code, /^[a-z][a-z0-9_]*$/)
    assert.match(check.summary, /^[A-Z][^\n]*[.!?]$/, `${label} ${check.id} summary is not a complete sentence`)
    if (check.next !== null) assert.match(check.next, /^[A-Z][^\n]*[.!?]$/, `${label} ${check.id} next is not a complete sentence`)
  }
  const expectedOverall = report.checks.some((check) => check.state === 'action')
    ? 'action'
    : report.checks.some((check) => check.state === 'unknown') ? 'unknown' : 'ok'
  assert.equal(report.overall, expectedOverall, `${label} overall does not match its checks`)
  if (report.config === null) {
    assert.deepEqual(report.checks.map((check) => check.id), ['configuration'], `${label} invalid configuration has extra checks`)
  } else {
    assert.deepEqual(Object.keys(report.config).sort(), ['configFile', 'dataDirectorySource', 'listenPort', 'listenScope', 'publicUrl', 'publicUrlSource', 'trustedProxies'])
    assert.equal(typeof report.config.publicUrl, 'string')
    assert.ok(Buffer.byteLength(report.config.publicUrl) <= 2048)
    assert.ok(['default', 'environment', 'config_file'].includes(report.config.publicUrlSource))
    assert.ok(['loopback', 'all', 'private', 'public', 'other'].includes(report.config.listenScope))
    assert.ok(Number.isInteger(report.config.listenPort) && report.config.listenPort >= 1 && report.config.listenPort <= 65535)
    assert.ok(Array.isArray(report.config.trustedProxies) && report.config.trustedProxies.length <= 64)
    for (const proxy of report.config.trustedProxies) assert.equal(typeof proxy, 'string')
    assert.ok(['default', 'environment'].includes(report.config.dataDirectorySource))
    assert.ok(['loaded', 'absent'].includes(report.config.configFile))
  }
  return report
}

function findCheck(report, id) {
  const found = report.checks.find((check) => check.id === id)
  assert.ok(found, `the report has no ${id} check`)
  return found
}

function assertCheck(report, id, state, code) {
  const check = findCheck(report, id)
  assert.equal(check.state, state, `${id} state`)
  if (code) assert.equal(check.code, code, `${id} code`)
  return check
}

function createFixture(workRoot, name) {
  const data = join(workRoot, name)
  const secrets = join(data, 'secrets')
  mkdirSync(secrets, { recursive: true, mode: 0o700 })
  chmodSync(data, 0o700)
  chmodSync(secrets, 0o700)
  const configFile = join(data, 'config.json')
  writeFileSync(configFile, JSON.stringify({
    publicUrl: 'https://sesame.example.test',
    addr: '127.0.0.1:8787',
    trustedProxies: ['127.0.0.1/32'],
    smtp: { addr: 'smtp.example.test:587', username: 'fixture-user', password: configMarker, from: 'fixture@example.test' },
  }), { mode: 0o600 })
  chmodSync(configFile, 0o600)
  const database = join(data, 'sesame.db')
  writeFileSync(database, databaseMarker, { mode: 0o600 })
  chmodSync(database, 0o600)
  for (const fileName of secretNames) {
    const path = join(secrets, fileName)
    writeFileSync(path, keyMarker, { mode: 0o600 })
    chmodSync(path, 0o600)
  }
  return { data, secrets, configFile, database }
}

function createServiceShims(workRoot) {
  const directory = join(workRoot, 'shims')
  const log = join(workRoot, 'service-commands.log')
  mkdirSync(directory, { recursive: true, mode: 0o700 })
  for (const name of serviceNames) {
    const path = join(directory, name)
    writeFileSync(path, '#!/bin/sh\nprintf \'%s %s\\n\' "$0" "$*" >> "$SESAME_DOCTOR_COMMAND_LOG"\nexit 97\n', { mode: 0o700 })
    chmodSync(path, 0o700)
  }
  return {
    directory,
    log,
    read() {
      return existsSync(log) ? readFileSync(log, 'utf8').trim() : ''
    },
  }
}

function baseEnvironment(shim, extra) {
  const environment = Object.fromEntries(Object.entries(process.env).filter(([name]) => !name.startsWith('SESAME_')))
  return {
    ...environment,
    PATH: [shim.directory, dirname(process.execPath), environment.PATH, '/usr/bin:/bin'].filter(Boolean).join(':'),
    SESAME_DOCTOR_COMMAND_LOG: shim.log,
    ...extra,
  }
}

function serviceEnvironment(fixture, shim, extra = {}) {
  return baseEnvironment(shim, {
    SESAME_DATA_DIR: fixture.data,
    SESAME_CONFIG_FILE: fixture.configFile,
    SESAME_SMTP_PASSWORD: environmentMarker,
    ...extra,
  })
}

function plainEnvironment(shim, extra = {}) {
  return baseEnvironment(shim, extra)
}

function run(binary, args, environment, options = {}) {
  const result = spawnSync(binary, args, {
    cwd: repositoryRoot,
    env: environment,
    encoding: 'utf8',
    maxBuffer: 16 * 1024 * 1024,
    timeout: options.timeout ?? 60000,
  })
  if (result.error) throw result.error
  return { status: result.status, signal: result.signal, stdout: result.stdout, stderr: result.stderr }
}

function assertSafe(text, label, dataDir, forbidden = []) {
  for (const value of [dataDir, ...forbidden, configMarker, environmentMarker, keyMarker, databaseMarker]) {
    assert.equal(text.includes(value), false, `${label} exposes a private value`)
  }
}

function collapse(text) {
  return text.replace(/\s+/g, ' ').trim()
}

function snapshotTree(path) {
  if (!existsSync(path)) return null
  const stat = lstatSync(path)
  const mode = process.platform === 'win32' ? null : stat.mode & 0o777
  if (stat.isSymbolicLink()) return { type: 'symlink', target: readlinkSync(path) }
  if (stat.isFile()) return { type: 'file', mode, bytes: stat.size, digest: readableDigest(path) }
  if (stat.isFIFO()) return { type: 'fifo', mode }
  if (stat.isDirectory()) {
    const entries = {}
    for (const name of readdirSync(path).sort()) entries[name] = snapshotTree(join(path, name))
    return { type: 'directory', mode, entries }
  }
  return { type: 'other', mode }
}

function readableDigest(path) {
  try {
    return sha256(readFileSync(path))
  } catch (error) {
    if (error.code === 'EACCES') return null
    throw error
  }
}

function assertNoRuntimeArtifacts(path) {
  if (!existsSync(path)) return
  const stack = [path]
  while (stack.length > 0) {
    const current = stack.pop()
    for (const name of readdirSync(current)) {
      assert.equal(name === 'sesame.lock', false, `a lock appeared at ${name}`)
      assert.equal(name.startsWith('sesame.db-'), false, `a database sidecar appeared at ${name}`)
      assert.equal(name.endsWith('.tmp') || name.startsWith('.tmp-') || name.includes('.tmp.'), false, `a temporary file appeared at ${name}`)
      assert.equal(name.startsWith('.restore-'), false, `restore state appeared at ${name}`)
      const child = join(current, name)
      const stat = lstatSync(child)
      if (stat.isDirectory()) stack.push(child)
    }
  }
}

function sha256(bytes) {
  return createHash('sha256').update(bytes).digest('hex')
}
