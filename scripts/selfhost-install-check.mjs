import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { chmodSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, readlinkSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { readSelfHostingDocument, sectionBlocks } from './selfhost-doc.mjs'

const repositoryRoot = dirname(dirname(fileURLToPath(import.meta.url)))
const runGo = join(repositoryRoot, 'scripts', 'run-go.mjs')
const binaryVersion = '0.0.0-install-journey'
const binaryCommit = '0123456789abcdef0123456789abcdef01234567'
const secretMarker = 'journey-fictional-secret-value'
const liveDatabaseMarker = 'journey-fictional-live-database'
const serviceNames = ['docker', 'docker-compose', 'podman', 'caddy', 'tailscale', 'systemctl', 'service', 'curl', 'wget']
const dockerConfig = 'docker compose config'
const expected = {
  local: {
    host: '',
    publicUrl: 'http://localhost:8787',
    trustedProxies: [],
    files: ['compose.yaml', '.env'],
    prerequisite: [/docker/i, /port|listener/i, /subnet|network/i],
  },
  caddy: {
    host: 'vault.example.com',
    publicUrl: 'https://vault.example.com',
    trustedProxies: ['172.31.88.0/24'],
    files: ['compose.yaml', '.env', 'Caddyfile', 'caddy.env'],
    prerequisite: [/docker/i, /port|listener/i, /subnet|network/i, /dns/i, /tls|certificate/i],
  },
  tailscale: {
    host: 'sesame.example-tailnet.ts.net',
    publicUrl: 'https://sesame.example-tailnet.ts.net',
    trustedProxies: ['172.31.88.0/24'],
    files: ['compose.yaml', '.env'],
    prerequisite: [/docker/i, /port|listener/i, /subnet|network/i, /tailnet/i, /https/i],
  },
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main(process.argv.slice(2)).catch((error) => {
    console.error(`Guided install check failed: ${error.stack ?? error}`)
    process.exitCode = 1
  })
}

async function main(args) {
  const options = parseArguments(args)
  const workRoot = mkdtempSync(join(options.tempRoot || tmpdir(), 'sesame-install-check-'))
  let passed = 0
  const check = async (name, action) => {
    const value = await action()
    passed += 1
    console.log(`ok - ${name}`)
    return value
  }
  try {
    const binary = await check('the server builds with the install command', async () => buildBinary(options, workRoot))
    const version = await check('the binary reports the calling tool version', async () => readToolVersion(binary, options.binary ? '' : binaryVersion))
    const document = readSelfHostingDocument()
    const guide = parseGuide(document)
    const shim = createServiceShims(workRoot)
    const liveData = createLiveData(workRoot)
    const liveBefore = snapshotTree(liveData)
    const environment = installEnvironment(liveData, shim, dirname(binary))
    const work = join(workRoot, 'guide')
    mkdirSync(work, { recursive: true, mode: 0o700 })

    const guideResult = await check('the documented generation block runs with the real binary', async () => {
      const result = runGuideBlock(guide.block, work, environment)
      assert.equal(result.status, 0, result.stderr || result.stdout)
      return result
    })
    assert.ok(guideResult.stdout.includes(dockerConfig), 'the human output does not direct the operator to docker compose config')
    assert.ok(guideResult.stdout.includes('Inspect the files'), 'the human output does not direct the operator to inspect the files')

    const plans = {}
    for (const profile of ['local', 'caddy', 'tailscale']) {
      const target = join(work, guide.targets[profile])
      plans[profile] = await check(`the ${profile} directory holds the exact generated plan and files`, async () => inspectTarget({
        profile, target, version, expected: expected[profile],
      }))
    }

    await check('the human output quotes the target path and names the canonical files', async () => {
      for (const profile of ['local', 'caddy', 'tailscale']) {
        const directory = guide.targets[profile]
        assert.ok(guideResult.stdout.includes(`"${directory}"`), `the human output does not quote ${directory}`)
      }
    })

    await check('a second run of each profile produces the same plan and stable file order', async () => {
      const repeat = join(workRoot, 'repeat')
      mkdirSync(repeat, { recursive: true, mode: 0o700 })
      for (const profile of ['local', 'caddy', 'tailscale']) {
        const target = join(repeat, `sesame-${profile}`)
        const args = [...installArguments(profile, guide.imageTag, target), '--json']
        const result = run(binary, args, environment)
        assert.equal(result.status, 0, result.stderr)
        const printed = parsePlan(result.stdout, `${profile} --json output`)
        assert.deepEqual(printed, plans[profile], `the ${profile} plan changed between runs`)
        assert.deepEqual(printed.files.map((file) => file.path), plans[profile].files.map((file) => file.path), `the ${profile} file order is not stable`)
        const onDisk = parsePlan(readFileSync(join(target, 'plan.json'), 'utf8'), `${profile} plan.json`)
        assert.deepEqual(printed, onDisk, `the ${profile} printed plan differs from plan.json`)
      }
    })

    await check('an existing directory, file, symlink and broken symlink are refused unchanged', async () => {
      await assertExistingTargetsRefused(binary, workRoot, environment)
    })

    await check('a repeated invocation is refused and leaves the completed target unchanged', async () => {
      const target = join(work, guide.targets.local)
      const before = snapshotTree(target)
      const result = run(binary, [...installArguments('local', guide.imageTag, target), '--json'], environment)
      assertRefused(result, 'the repeated invocation')
      assert.deepEqual(snapshotTree(target), before, 'the repeated invocation changed the completed target')
    })

    await check('a missing parent is refused and no ancestor is created', async () => {
      const parent = join(workRoot, 'missing')
      const target = join(parent, 'sesame')
      assertRefused(run(binary, installArguments('local', guide.imageTag, target), environment), 'the missing parent')
      assert.equal(existsSync(parent), false, 'the refused run created an ancestor directory')
    })

    await check('invalid profiles, versions, hosts and injections are refused with empty stdout', async () => {
      await assertInvalidValuesRefused(binary, workRoot, guide.imageTag, environment)
    })

    await check('concurrent generation has one winner and leaves a complete target', async () => {
      const parent = join(workRoot, 'concurrent')
      mkdirSync(parent, { recursive: true, mode: 0o700 })
      const target = join(parent, 'sesame')
      const first = start(binary, installArguments('local', guide.imageTag, target), environment)
      const second = start(binary, installArguments('local', guide.imageTag, target), environment)
      const results = await Promise.all([first, second])
      const winners = results.filter((result) => result.status === 0)
      assert.equal(winners.length, 1, `concurrent generation produced ${winners.length} winners`)
      for (const result of results) {
        if (result.status !== 0) assert.equal(result.stdout, '', 'the losing concurrent run printed stdout')
      }
      inspectTarget({ profile: 'local', target, version, expected: expected.local })
    })

    await check('a directory with spaces is accepted and quoted in human output', async () => {
      const parent = join(workRoot, 'human')
      mkdirSync(parent, { recursive: true, mode: 0o700 })
      const target = join(parent, 'sesame local')
      const result = run(binary, installArguments('local', guide.imageTag, target), environment)
      assert.equal(result.status, 0, result.stderr)
      assert.ok(result.stdout.includes(`"${target}"`), 'the human output does not quote the target path')
      assert.ok(result.stdout.includes(dockerConfig), 'the human output does not direct the operator to docker compose config')
      inspectTarget({ profile: 'local', target, version, expected: expected.local })
    })

    await check('no operation changed the live data directory or invoked a service command', async () => {
      assert.deepEqual(snapshotTree(liveData), liveBefore, 'generation changed the live data directory')
      assert.equal(existsSync(shim.log), false, `generation invoked a service command: ${shim.read()}`)
    })

    await check('the plan holds no absolute operator path, environment dump or secret', async () => {
      const text = JSON.stringify(plans)
      for (const value of [workRoot, liveData, secretMarker, liveDatabaseMarker, binaryCommit]) {
        assert.equal(text.includes(value), false, `the plan holds ${value}`)
      }
      assert.doesNotMatch(text, /SESAME_(?:DATA|CONFIG|PUBLIC|TRUSTED|CAPABILITY)/)
    })

    console.log(`Guided install check passed: ${passed} checks.`)
    console.log('Not run by this native check: docker compose config, image pull, container start, Caddy certificate issuance and Tailscale serving.')
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
    const directory = join(workRoot, 'bin')
    mkdirSync(directory, { recursive: true, mode: 0o700 })
    const link = join(directory, 'sesame-server')
    symlinkSync(options.binary, link)
    return link
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
  if (requiredVersion) assert.equal(match[1], requiredVersion, 'the built binary does not report the fictional journey version')
  return match[1]
}

function parseGuide(document) {
  const blocks = sectionBlocks(document, '## Guided installation')
  assert.ok(blocks.length >= 4, 'the guided installation section must hold the generation, inspection, Caddy and Tailscale blocks')
  const block = blocks.find((candidate) => candidate.includes('install --profile local'))
  assert.ok(block, 'the guided installation section has no local install command')
  const lines = block.split('\n').map((line) => line.trim()).filter((line) => line && !line.startsWith('#'))
  assert.equal(lines[0], 'SESAME_IMAGE_TAG=0.4.1', 'the guided installation does not choose an explicit image tag')
  const commands = lines.slice(1)
  assert.equal(commands.length, 3, 'the guided installation must show one command for each profile')
  const targets = {}
  for (const command of commands) {
    const match = command.match(/^sesame-server install --profile (local|caddy|tailscale)(?: --host (\S+))? --version "\$SESAME_IMAGE_TAG" --dir (\.\/\S+)$/)
    assert.ok(match, `the guided installation command has an unexpected shape: ${command}`)
    const [, profile, host = '', directory] = match
    targets[profile] = directory
    assert.equal(host, expected[profile].host, `the ${profile} guide host is not the fictional value`)
  }
  assert.deepEqual(Object.keys(targets).sort(), ['caddy', 'local', 'tailscale'], 'the guided installation does not cover the three profiles')
  assert.ok(blocks.some((candidate) => candidate.includes(dockerConfig)), 'the guided installation does not show docker compose config')
  assert.ok(blocks.some((candidate) => candidate.includes('caddy run --config ./Caddyfile --envfile ./caddy.env')), 'the guided installation has no Caddy envfile command')
  assert.ok(blocks.some((candidate) => candidate.includes('tailscale serve --bg --https=443 http://127.0.0.1:8787')), 'the guided installation has no private Tailscale Serve command')
  assert.match(document.slice(document.indexOf('## Guided installation'), document.indexOf('## First run and pairing')), /EnvironmentFile=\/absolute\/path\/to\/sesame-caddy\/caddy\.env/)
  return { block, imageTag: '0.4.1', targets }
}

function runGuideBlock(block, cwd, environment) {
  return spawnSync('bash', ['-euo', 'pipefail', '-c', block], {
    cwd,
    env: environment,
    encoding: 'utf8',
    timeout: 120000,
  })
}

function installEnvironment(liveData, shim, binaryDirectory) {
  const environment = Object.fromEntries(Object.entries(process.env).filter(([name]) => !name.startsWith('SESAME_')))
  return {
    ...environment,
    PATH: `${shim.directory}:${binaryDirectory}:${dirname(process.execPath)}:${environment.PATH}`,
    SESAME_DATA_DIR: liveData,
    SESAME_CONFIG_FILE: join(liveData, 'config.json'),
    SESAME_PUBLIC_URL: 'http://sesame.example.net',
    SESAME_TRUSTED_PROXIES: '0.0.0.0/0',
    SESAME_BACKUP_INTERVAL: 'not-a-duration',
    SESAME_CAPABILITY_SIGNING_KEY: secretMarker,
    SESAME_INSTALL_COMMAND_LOG: shim.log,
  }
}

function createServiceShims(workRoot) {
  const directory = join(workRoot, 'shims')
  const log = join(workRoot, 'service-commands.log')
  mkdirSync(directory, { recursive: true, mode: 0o700 })
  for (const name of serviceNames) {
    const path = join(directory, name)
    writeFileSync(path, '#!/bin/sh\nprintf \'%s %s\\n\' "$0" "$*" >> "$SESAME_INSTALL_COMMAND_LOG"\nexit 97\n', { mode: 0o700 })
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

function createLiveData(workRoot) {
  const directory = join(workRoot, 'live-data')
  const secrets = join(directory, 'secrets')
  mkdirSync(secrets, { recursive: true, mode: 0o700 })
  writeFileSync(join(directory, 'sesame.db'), liveDatabaseMarker, { mode: 0o600 })
  writeFileSync(join(directory, 'config.json'), '{ this is not valid JSON\n', { mode: 0o600 })
  writeFileSync(join(secrets, 'admin-encryption.key'), 'fictional-key-material\n', { mode: 0o600 })
  chmodSync(join(directory, 'sesame.db'), 0o600)
  chmodSync(join(directory, 'config.json'), 0o600)
  chmodSync(join(secrets, 'admin-encryption.key'), 0o600)
  return directory
}

function installArguments(profile, version, target) {
  const arguments_ = ['install', '--profile', profile, '--version', version, '--dir', target]
  if (expected[profile].host) arguments_.push('--host', expected[profile].host)
  return arguments_
}

function run(binary, args, environment, options = {}) {
  const result = spawnSync(binary, args, {
    cwd: options.cwd ?? repositoryRoot,
    env: environment,
    encoding: 'utf8',
    maxBuffer: 16 * 1024 * 1024,
    timeout: 60000,
  })
  if (result.error) throw result.error
  return result
}

function start(binary, args, environment) {
  return new Promise((resolveRun, rejectRun) => {
    const child = spawn(binary, args, { env: environment, stdio: ['ignore', 'pipe', 'pipe'] })
    let stdout = ''
    let stderr = ''
    child.stdout.on('data', (chunk) => { stdout += chunk })
    child.stderr.on('data', (chunk) => { stderr += chunk })
    child.once('error', rejectRun)
    child.once('close', (status, signal) => resolveRun({ status, signal, stdout, stderr }))
  })
}

function inspectTarget({ profile, target, version, expected: wanted }) {
  const stat = lstatSync(target)
  assert.equal(stat.isDirectory(), true, `${profile} target is not a directory`)
  assert.equal(stat.isSymbolicLink(), false, `${profile} target is a symlink`)
  if (process.platform !== 'win32') assert.equal(stat.mode & 0o777, 0o700, `${profile} target is not mode 0700`)

  const expectedFiles = [...wanted.files, 'plan.json'].sort()
  assert.deepEqual(readdirSync(target).sort(), expectedFiles, `${profile} target holds unexpected entries`)

  const planText = readFileSync(join(target, 'plan.json'), 'utf8')
  const plan = parsePlan(planText, `${profile} plan.json`)
  assert.deepEqual(Object.keys(plan).sort(), ['files', 'imageVersion', 'prerequisites', 'profile', 'publicUrl', 'schemaVersion', 'toolVersion', 'trustedProxies'], `${profile} plan fields are not exact`)
  assert.equal(plan.schemaVersion, 1, `${profile} schemaVersion`)
  assert.equal(plan.toolVersion, version, `${profile} toolVersion`)
  assert.equal(plan.profile, profile, `${profile} profile`)
  assert.equal(plan.imageVersion, '0.4.1', `${profile} imageVersion`)
  assert.equal(plan.publicUrl, wanted.publicUrl, `${profile} publicUrl`)
  assert.deepEqual(plan.trustedProxies, wanted.trustedProxies, `${profile} trustedProxies`)

  assert.ok(Array.isArray(plan.files), `${profile} files is not an array`)
  assert.equal(plan.files.length, wanted.files.length, `${profile} plan lists the wrong number of generated files`)
  assert.deepEqual(plan.files.map((file) => file.path).sort(), [...wanted.files].sort(), `${profile} plan lists the wrong file paths`)
  for (const file of plan.files) {
    assert.deepEqual(Object.keys(file).sort(), ['mode', 'path', 'sha256'], `${profile} file entry fields are not exact`)
    assert.equal(file.mode, '0600', `${profile} ${file.path} mode`)
    assert.match(file.sha256, /^[0-9a-f]{64}$/, `${profile} ${file.path} hash is not a lowercase SHA-256`)
    const path = join(target, file.path)
    const fileStat = lstatSync(path)
    assert.equal(fileStat.isFile(), true, `${profile} ${file.path} is not a regular file`)
    assert.equal(fileStat.isSymbolicLink(), false, `${profile} ${file.path} is a symlink`)
    if (process.platform !== 'win32') assert.equal(fileStat.mode & 0o777, 0o600, `${profile} ${file.path} is not mode 0600`)
    assert.equal(sha256(readFileSync(path)), file.sha256, `${profile} ${file.path} hash does not match the file`)
  }
  assert.equal(plan.files.some((file) => file.path === 'plan.json'), false, `${profile} plan lists plan.json as a generated file`)
  const planStat = lstatSync(join(target, 'plan.json'))
  assert.equal(planStat.isFile(), true, `${profile} plan.json is not a regular file`)
  if (process.platform !== 'win32') assert.equal(planStat.mode & 0o777, 0o600, `${profile} plan.json is not mode 0600`)

  const compose = readFileSync(join(target, 'compose.yaml'))
  assert.deepEqual(compose, readFileSync(join(repositoryRoot, 'deploy', 'selfhost', 'compose.yaml')), `${profile} compose.yaml is not the canonical file`)
  const environment = parseDotenv(readFileSync(join(target, '.env'), 'utf8'), `${profile} .env`)
  assert.deepEqual([...environment.keys()].sort(), ['SESAME_IMAGE_TAG', 'SESAME_PUBLIC_URL', 'SESAME_TRUSTED_PROXIES'], `${profile} .env keys are not exact`)
  assert.equal(environment.get('SESAME_IMAGE_TAG'), '0.4.1', `${profile} .env image tag`)
  assert.equal(environment.get('SESAME_PUBLIC_URL'), wanted.publicUrl, `${profile} .env public URL`)
  assert.equal(environment.get('SESAME_TRUSTED_PROXIES'), wanted.trustedProxies.join(','), `${profile} .env trusted proxies`)
  if (profile === 'caddy') {
    assert.deepEqual(readFileSync(join(target, 'Caddyfile')), readFileSync(join(repositoryRoot, 'deploy', 'selfhost', 'Caddyfile')), 'the Caddyfile is not the canonical file')
    assert.equal(readFileSync(join(target, 'caddy.env'), 'utf8'), `SESAME_HOST=${wanted.host}\n`, 'caddy.env is not the selected host')
  }

  assert.ok(Array.isArray(plan.prerequisites), `${profile} prerequisites is not an array`)
  assert.ok(plan.prerequisites.length >= wanted.prerequisite.length, `${profile} plan has too few prerequisites`)
  const ids = new Set()
  for (const prerequisite of plan.prerequisites) {
    assert.deepEqual(Object.keys(prerequisite).sort(), ['id', 'message', 'state'], `${profile} prerequisite fields are not exact`)
    assert.equal(prerequisite.state, 'unknown', `${profile} prerequisite ${prerequisite.id} is not unknown`)
    assert.match(prerequisite.message, /^[A-Z][^\n]*[.!?]$/, `${profile} prerequisite ${prerequisite.id} is not a complete sentence`)
    assert.equal(ids.has(prerequisite.id), false, `${profile} prerequisite ${prerequisite.id} is repeated`)
    ids.add(prerequisite.id)
  }
  for (const pattern of wanted.prerequisite) {
    assert.ok([...ids].some((id) => pattern.test(id)), `${profile} prerequisites do not cover ${pattern}`)
  }
  return plan
}

function parsePlan(text, label) {
  assert.ok(text.trim(), `${label} is empty`)
  let plan
  try {
    plan = JSON.parse(text)
  } catch (error) {
    assert.fail(`${label} is not JSON: ${error.message}`)
  }
  assert.ok(plan && typeof plan === 'object' && !Array.isArray(plan), `${label} is not an object`)
  return plan
}

function parseDotenv(text, label) {
  const values = new Map()
  for (const line of text.split('\n')) {
    if (!line) continue
    const match = line.match(/^([A-Z][A-Z0-9_]*)=(.*)$/)
    assert.ok(match, `${label} holds an unexpected line`)
    assert.equal(values.has(match[1]), false, `${label} repeats ${match[1]}`)
    assert.equal(/["'`$\\#;\s]/.test(match[2]), false, `${label} ${match[1]} holds an unsafe character`)
    values.set(match[1], match[2])
  }
  return values
}

async function assertExistingTargetsRefused(binary, workRoot, environment) {
  const parent = join(workRoot, 'existing')
  mkdirSync(parent, { recursive: true, mode: 0o700 })

  const directory = join(parent, 'directory')
  mkdirSync(directory, { mode: 0o700 })
  writeFileSync(join(directory, 'keep.txt'), 'existing directory\n', { mode: 0o600 })
  const directoryBefore = snapshotTree(directory)
  assertRefused(run(binary, [...installArguments('local', '0.4.1', directory), '--json'], environment), 'the existing directory')
  assert.deepEqual(snapshotTree(directory), directoryBefore, 'the existing directory changed')

  const file = join(parent, 'file')
  writeFileSync(file, 'existing file\n', { mode: 0o600 })
  const fileBefore = snapshotTree(file)
  assertRefused(run(binary, installArguments('local', '0.4.1', file), environment), 'the existing file')
  assert.deepEqual(snapshotTree(file), fileBefore, 'the existing file changed')

  const destination = join(parent, 'symlink-destination')
  mkdirSync(destination, { mode: 0o700 })
  writeFileSync(join(destination, 'keep.txt'), 'symlink destination\n', { mode: 0o600 })
  const link = join(parent, 'link')
  symlinkSync(destination, link)
  const destinationBefore = snapshotTree(destination)
  assert.equal(lstatSync(link).isSymbolicLink(), true, 'the test link is not a symlink')
  assertRefused(run(binary, installArguments('local', '0.4.1', link), environment), 'the existing symlink')
  assert.equal(lstatSync(link).isSymbolicLink(), true, 'the refused run replaced the symlink')
  assert.deepEqual(snapshotTree(destination), destinationBefore, 'the symlink destination changed')

  const broken = join(parent, 'broken')
  symlinkSync(join(parent, 'missing-destination'), broken)
  assertRefused(run(binary, installArguments('local', '0.4.1', broken), environment), 'the broken symlink')
  assert.equal(lstatSync(broken).isSymbolicLink(), true, 'the refused run replaced the broken symlink')
}

async function assertInvalidValuesRefused(binary, workRoot, imageTag, environment) {
  const parent = join(workRoot, 'invalid')
  mkdirSync(parent, { recursive: true, mode: 0o700 })
  const cases = [
    ['unknown profile', ['--profile', 'nginx', '--host', 'vault.example.com', '--version', imageTag]],
    ['local with a host', ['--profile', 'local', '--host', 'vault.example.com', '--version', imageTag]],
    ['caddy without a host', ['--profile', 'caddy', '--version', imageTag]],
    ['tailscale without a host', ['--profile', 'tailscale', '--version', imageTag]],
    ['tailscale with a public host', ['--profile', 'tailscale', '--host', 'vault.example.com', '--version', imageTag]],
    ['caddy with an IP literal', ['--profile', 'caddy', '--host', '203.0.113.10', '--version', imageTag]],
    ['caddy with localhost', ['--profile', 'caddy', '--host', 'localhost', '--version', imageTag]],
    ['caddy with an internal name', ['--profile', 'caddy', '--host', 'vault.internal', '--version', imageTag]],
    ['host with a shell separator', ['--profile', 'caddy', '--host', 'vault.example.com;touch journey-injection-marker', '--version', imageTag]],
    ['host with a command substitution', ['--profile', 'caddy', '--host', 'vault.example.com$(id)', '--version', imageTag]],
    ['host with a newline', ['--profile', 'caddy', '--host', 'vault.example.com\nvault.example.net', '--version', imageTag]],
    ['host with a dotenv reference', ['--profile', 'caddy', '--host', 'vault.example.com${SESAME_HOST}', '--version', imageTag]],
    ['version with a leading v', ['--profile', 'local', '--version', 'v0.4.1']],
    ['version with two components', ['--profile', 'local', '--version', '0.4']],
    ['version with build metadata', ['--profile', 'local', '--version', '0.4.1+build']],
    ['version with a shell separator', ['--profile', 'local', '--version', '0.4.1;id']],
    ['version with a prerelease leading zero', ['--profile', 'local', '--version', '0.4.1-01']],
    ['unknown option', ['--profile', 'local', '--version', imageTag, '--bogus']],
    ['repeated profile', ['--profile', 'local', '--profile', 'caddy', '--version', imageTag]],
    ['repeated host', ['--profile', 'caddy', '--host', 'vault.example.com', '--host', 'vault.example.net', '--version', imageTag]],
    ['positional argument', ['--profile', 'local', '--version', imageTag, 'extra']],
    ['empty host', ['--profile', 'caddy', '--host', '', '--version', imageTag]],
    ['empty version', ['--profile', 'local', '--version', '']],
    ['control character in host', ['--profile', 'caddy', '--host', 'vault.example.com\tvault.example.net', '--version', imageTag]],
  ]
  for (const [label, args] of cases) {
    const target = join(parent, label.replace(/[^a-z0-9]+/gi, '-').toLowerCase())
    const result = run(binary, ['install', ...args, '--dir', target], environment, { cwd: parent })
    assertRefused(result, label)
    assert.equal(existsSync(target), false, `${label} created the target`)
  }
  assertRefused(run(binary, ['install', '--profile', 'local', '--version', imageTag, '--dir', ''], environment, { cwd: parent }), 'the empty directory')
  assert.equal(existsSync(join(parent, 'journey-injection-marker')), false, 'an injected host value ran a shell command')
}

function assertRefused(result, label) {
  assert.notEqual(result.status, 0, `${label} was accepted`)
  assert.equal(result.stdout, '', `${label} printed to stdout`)
  assert.ok(result.stderr.trim(), `${label} printed no error`)
  assert.equal(result.stderr.includes(secretMarker), false, `${label} printed a secret`)
  assert.equal(result.stderr.includes(liveDatabaseMarker), false, `${label} printed live data`)
}

function snapshotTree(path) {
  if (!existsSync(path)) return null
  const stat = lstatSync(path)
  if (stat.isSymbolicLink()) {
    return { type: 'symlink', target: readlinkSync(path) }
  }
  if (stat.isFile()) {
    return {
      type: 'file',
      mode: process.platform === 'win32' ? null : stat.mode & 0o777,
      bytes: stat.size,
      digest: sha256(readFileSync(path)),
    }
  }
  const entries = {}
  for (const name of readdirSync(path).sort()) {
    entries[name] = snapshotTree(join(path, name))
  }
  return {
    type: 'directory',
    mode: process.platform === 'win32' ? null : stat.mode & 0o777,
    entries,
  }
}

function sha256(bytes) {
  return createHash('sha256').update(bytes).digest('hex')
}
