import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { docker } from './selfhost-docker.mjs'
import { repositoryRoot } from './selfhost-doc.mjs'

const image = readFileSync(join(repositoryRoot, 'Dockerfile.selfhost'), 'utf8').match(/^FROM --platform=\$BUILDPLATFORM (\S+) AS console$/m)?.[1]
assert.match(image ?? '', /^node:[^\s@]+@sha256:[0-9a-f]{64}$/)

function inspectVolume(container, script) {
  const mounts = JSON.parse(docker(['inspect', '--format', '{{json .Mounts}}', container]).stdout)
  const data = mounts.filter((mount) => mount.Destination === '/data')
  assert.equal(data.length, 1, 'the fixture must have one data mount')
  assert.equal(data[0].Type, 'volume', 'the fixture data mount must be a named volume')
  assert.match(data[0].Name, /^[a-zA-Z0-9][a-zA-Z0-9_.-]+$/)
  const result = spawnSync('docker', [
    'run', '--rm', '--network', 'none', '--read-only', '--user', '65532:65532',
    '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true',
    '--memory', '256m', '--cpus', '1', '--pids-limit', '64',
    '--mount', `type=volume,source=${data[0].Name},target=/input,readonly`,
    '--entrypoint', 'node', image, '--input-type=module', '-e', script,
  ], { encoding: null, maxBuffer: 64 * 1024 * 1024, timeout: 60_000 })
  if (result.error || result.status !== 0) throw new Error('The isolated read-only volume inspection failed.')
  return result.stdout
}

export function fixtureDataDigest(container) {
  const value = inspectVolume(container, `
    import { createHash } from 'node:crypto'
    import { readFileSync } from 'node:fs'
    const hash = createHash('sha256')
    for (const name of ['sesame.db', 'secrets/admin-encryption.key', 'secrets/signing.key', 'secrets/ip-pepper.key']) {
      hash.update(name)
      hash.update(readFileSync('/input/' + name))
    }
    process.stdout.write(hash.digest('hex'))
  `).toString('utf8')
  assert.match(value, /^[a-f0-9]{64}$/)
  return value
}

export function fixtureInstallationFiles(container) {
  return JSON.parse(inspectVolume(container, `
    import assert from 'node:assert/strict'
    import { lstatSync, readFileSync, readdirSync } from 'node:fs'
    assert.deepEqual(readdirSync('/input'), ['plan'])
    const directory = lstatSync('/input/plan')
    assert.ok(directory.isDirectory() && !directory.isSymbolicLink())
    const files = readdirSync('/input/plan').sort().map((name) => {
      assert.ok(['.env', 'Caddyfile', 'caddy.env', 'compose.yaml', 'plan.json'].includes(name))
      const path = '/input/plan/' + name
      const info = lstatSync(path)
      assert.ok(info.isFile() && !info.isSymbolicLink() && info.size < 65536)
      return { name, mode: info.mode & 0o777, content: readFileSync(path, 'utf8') }
    })
    process.stdout.write(JSON.stringify({ mode: directory.mode & 0o777, files }))
  `).toString('utf8'))
}

export function copyFixtureBackup(container, name, destination) {
  assert.match(name, /^[a-zA-Z0-9][a-zA-Z0-9_.-]+\.tar$/)
  const archive = inspectVolume(container, `
    import { lstatSync, readFileSync } from 'node:fs'
    const path = '/input/backups/' + ${JSON.stringify(name)}
    const info = lstatSync(path)
    if (!info.isFile() || info.isSymbolicLink() || info.size > 32 * 1024 * 1024) process.exit(1)
    process.stdout.write(readFileSync(path))
  `)
  writeFileSync(destination, archive, { mode: 0o600, flag: 'wx' })
}
