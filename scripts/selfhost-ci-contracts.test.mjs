import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { readSelfHostingDocument, repositoryRoot, replaceOnce, sectionBlocks } from './selfhost-doc.mjs'
import {
  approvalArguments,
  approvalEnvironment,
  approvalSelection,
  assertApprovalResult,
  assertRecoveryStatus,
  parseRecoveryJson,
  redactSensitiveLog,
  schemaFromMigrationNames,
  skipAllowedWithoutUpgrade,
} from './selfhost-recovery.mjs'

const read = (...parts) => readFileSync(join(repositoryRoot, ...parts), 'utf8')
const document = readSelfHostingDocument()

const jobBlock = (body, job) => {
  const start = body.indexOf(`\n  ${job}:\n`)
  assert.ok(start >= 0, `the ${job} job is missing from the workflow`)
  const rest = body.slice(start + 1)
  const afterFirst = rest.indexOf('\n') + 1
  const next = rest.slice(afterFirst).match(/^ {2}[a-z0-9_-]+:$/m)
  return next ? rest.slice(0, afterFirst + next.index) : rest
}

const stepBlocks = (body) => {
  const starts = [...body.matchAll(/^ {6}- /gm)].map((match) => match.index)
  return starts.map((start, index) => body.slice(start, starts[index + 1] ?? body.length))
}

const ownerID = 'a'.repeat(32)
const goodDeviceID = 'b'.repeat(32)
const badDeviceID = 'c'.repeat(32)
const memberID = 'd'.repeat(32)
const generation = '0123456789abcdef0123456789abcdef'

const fictionalStatus = (overrides = {}) => ({
  pending: true,
  generation,
  startedAt: '2026-01-02T03:04:05Z',
  completedAt: null,
  owners: [{ id: ownerID, name: 'Owner Fixture', setupPending: false, lastLoginAt: '2026-01-01T00:00:00Z' }],
  devices: [
    {
      id: goodDeviceID, name: 'Laptop Fixture', holder: { kind: 'owner', id: ownerID, name: 'Owner Fixture' },
      expiresAt: '2099-01-01T00:00:00Z', lastSeenAt: '2026-01-01T00:00:00Z', revokedAt: null,
    },
    {
      id: badDeviceID, name: 'Phone Fixture', holder: { kind: 'member', id: memberID, name: 'Member Fixture' },
      expiresAt: '2099-01-01T00:00:00Z', lastSeenAt: '2026-01-01T00:00:00Z', revokedAt: null,
    },
  ],
  ...overrides,
})

test('sectionBlocks returns the shell blocks of a section and stops at the next heading of the same level', () => {
  const sample = ['## One', '', '1. Step', '', '   ```sh', '   echo a', '   ```', '', '   ```text', '   not a command', '   ```', '', '### Inner', '```sh', 'echo b', '```', '## Two', '```sh', 'echo c', '```'].join('\n')
  assert.deepEqual(sectionBlocks(sample, '## One'), ['echo a', 'echo b'])
  assert.deepEqual(sectionBlocks(sample, '### Inner'), ['echo b'])
  assert.deepEqual(sectionBlocks(sample, '## Two'), ['echo c'])
  assert.throws(() => sectionBlocks(sample, '## Missing'))
})

test('sectionBlocks closes a block only with a matching fence and rejects one that never closes', () => {
  const nested = ['## One', '````sh', 'echo a', '```text', 'echo inner', '```', 'echo b', '````', '~~~sh', 'echo c', '~~~'].join('\n')
  assert.deepEqual(sectionBlocks(nested, '## One'), ['echo a\n```text\necho inner\n```\necho b', 'echo c'])
  assert.throws(() => sectionBlocks(['## One', '```sh', 'echo a'].join('\n'), '## One'), /never closed/)
})

test('replaceOnce refuses a block that no longer holds the text it rewrites', () => {
  assert.equal(replaceOnce('a b c', 'b', 'x', 'letter'), 'a x c')
  assert.throws(() => replaceOnce('a b c', 'z', 'x', 'letter'), /exactly once/)
  assert.throws(() => replaceOnce('b b', 'b', 'x', 'letter'), /exactly once/)
})

test('the sections the quick start check runs still hold the commands it expects', () => {
  const quickStart = sectionBlocks(document, '## Quick start')
  assert.equal(quickStart.length, 2)
  replaceOnce(quickStart[0], 'https://api.github.com/repos/usesesame/sesame-server/releases/latest', 'x', 'the release lookup address')
  replaceOnce(quickStart[0], 'https://raw.githubusercontent.com/usesesame/sesame-server/v${SESAME_IMAGE_TAG}/deploy/selfhost/compose.yaml', 'x', 'the compose file address')
  assert.match(quickStart[1], /docker compose up -d/)
  assert.match(quickStart[1], /\/livez/)
  assert.match(quickStart[1], /grep 'setup#token'/)
  const backup = sectionBlocks(document, '### Back up')
  assert.ok(backup.length >= 2)
  assert.match(backup[0], /backup \/data\/backups\/before-trip\.tar/)
  assert.match(backup[1], /check \/data\/backups\/before-trip\.tar/)
  const restore = sectionBlocks(document, '### Restore')
  assert.equal(restore.length, 1)
  assert.match(restore[0], /restore \/data\/backups\/before-trip\.tar/)
  assert.doesNotMatch(restore[0], /docker compose up -d/)
  const approve = sectionBlocks(document, '### Approve a restore')
  assert.equal(approve.length, 4)
  assert.match(approve[0], /^docker compose run --rm --no-deps sesame recover --status$/m)
  assert.match(approve[1], /^docker compose run --rm --no-deps sesame recover --status --json$/m)
  assert.match(approve[3], /^docker compose up -d$/m)
  const external = sectionBlocks(document, '### Restore a backup outside the data volume')
  assert.equal(external.length, 1)
  assert.match(sectionBlocks(document, '## Verify and build the image')[0], /gh attestation verify/)
})

test('the external restore recipe keeps the backup private and reads it from standard input', () => {
  const external = sectionBlocks(document, '### Restore a backup outside the data volume')[0]
  assert.match(external, /^chmod 0600 before-trip\.tar$/m)
  assert.match(external, /^docker compose run --rm --no-deps -T sesame restore - < before-trip\.tar$/m)
  assert.doesNotMatch(external, /chmod (?:0?644|0?664|0?666|0?777|a\+r|o\+r|go\+r)/)
  assert.doesNotMatch(external, /(?:^|\s)(?:-v|--volume)(?:\s|$)/)
  assert.doesNotMatch(external, /(?:^|\s)(?:-u|--user)(?:\s|$)/)
  assert.doesNotMatch(external, /:ro\b/)
  const quickstart = read('scripts', 'selfhost-quickstart.mjs')
  const modeChecks = quickstart.split('statSync(copy).mode & 0o777, 0o600').length - 1
  assert.equal(modeChecks, 2, 'the quick start must check the private source mode before and after the restore')
})

test('the approval is one shared block with closed placeholders and no retain-all shortcut', () => {
  const approve = sectionBlocks(document, '### Approve a restore')
  assert.equal(approve.length, 4)
  const block = approve[2]
  assert.match(block, /^docker compose run --rm --no-deps sesame recover --json \\$/m)
  assert.match(block, /^  --confirm "\$SESAME_RECOVERY_GENERATION" \\$/m)
  assert.match(block, /^  --retain-owner "\$SESAME_RETAIN_OWNER_ID" \\$/m)
  assert.match(block, /^  --retain-device "\$SESAME_RETAIN_DEVICE_ID"$/m)
  assert.doesNotMatch(block, /[0-9a-f]{32}/)
  assert.doesNotMatch(document, /--retain-all|(?:^|\s)--all(?:\s|$)/m)
  const quickstart = read('scripts', 'selfhost-quickstart.mjs')
  assert.match(quickstart, /approvalEnvironment\(/)
  assert.match(quickstart, /assertApprovalResult\(/)
  assert.match(quickstart, /runRejectedApproval\(/)
  assert.match(quickstart, /alreadyComplete/)
})

test('the guide tells older installed releases to follow their own tagged commands', () => {
  assert.match(document, /predates recovery approval/)
  assert.match(document, /release's own `SELF-HOSTING\.md`/s)
  assert.match(document, /has no `recover` command/)
  assert.match(document, /`\.restore-input-` followed only by digits/)
  assert.match(document, /`\.restore-new-` followed by exactly 12 lowercase hexadecimal characters/)
  assert.match(document, /needs room in its data volume/)
})

test('the documented approval block runs unchanged with closed placeholder values', () => {
  const block = sectionBlocks(document, '### Approve a restore')[2]
  const status = fictionalStatus()
  const environment = approvalEnvironment(status, { ownerIds: [ownerID], deviceIds: [goodDeviceID] })
  const directory = mkdtempSync(join(tmpdir(), 'sesame-approval-block-'))
  try {
    const bin = join(directory, 'bin')
    const argsFile = join(directory, 'args')
    mkdirSync(bin)
    writeFileSync(join(bin, 'docker'), '#!/bin/sh\nprintf \'%s\\n\' "$@" > "$SESAME_STUB_ARGS"\n')
    chmodSync(join(bin, 'docker'), 0o755)
    const result = spawnSync('bash', ['-euo', 'pipefail', '-c', block], {
      encoding: 'utf8',
      env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, SESAME_STUB_ARGS: argsFile, ...environment },
    })
    assert.equal(result.status, 0, result.stderr)
    assert.deepEqual(readFileSync(argsFile, 'utf8').trim().split('\n'), [
      'compose', 'run', '--rm', '--no-deps', 'sesame', 'recover', '--json',
      '--confirm', generation,
      '--retain-owner', ownerID,
      '--retain-device', goodDeviceID,
    ])
  } finally {
    rmSync(directory, { recursive: true, force: true })
  }
})

test('the recovery status parser accepts the documented JSON and rejects malformed state', () => {
  const status = fictionalStatus()
  assert.equal(assertRecoveryStatus(status, 'fixture').generation, generation)
  assert.doesNotThrow(() => assertRecoveryStatus({ pending: false, generation: '', startedAt: null, completedAt: null, owners: [], devices: [] }, 'empty fixture'))
  assert.doesNotThrow(() => assertRecoveryStatus({ pending: false, generation, startedAt: '2026-01-02T03:04:05Z', completedAt: '2026-01-02T03:05:05Z', owners: [], devices: [] }, 'completed fixture'))
  assert.throws(() => assertRecoveryStatus({ ...status, generation: generation.toUpperCase() }, 'fixture'), /generation/)
  assert.throws(() => assertRecoveryStatus({ ...status, generation: generation.slice(1) }, 'fixture'), /generation/)
  assert.throws(() => assertRecoveryStatus({ ...status, completedAt: '2026-01-02T03:04:06Z' }, 'fixture'), /completion/)
  assert.throws(() => assertRecoveryStatus({ ...status, owners: [status.owners[0], status.owners[0]] }, 'fixture'), /twice/)
  assert.throws(() => assertRecoveryStatus({ ...status, devices: [{ ...status.devices[0], revokedAt: 'yesterday' }] }, 'fixture'), /revokedAt/)
})

test('the approval selection accepts explicit owners and devices and refuses unsafe selections', () => {
  const status = fictionalStatus()
  const chosen = approvalSelection(status, { ownerIds: [ownerID], deviceIds: [goodDeviceID, badDeviceID] })
  assert.deepEqual(chosen, { ownerIds: [ownerID], deviceIds: [goodDeviceID, badDeviceID] })
  assert.deepEqual(approvalArguments(status, chosen), [
    'recover', '--confirm', generation,
    '--retain-owner', ownerID,
    '--retain-device', goodDeviceID,
    '--retain-device', badDeviceID,
    '--json',
  ])
  assert.deepEqual(approvalEnvironment(status, { ownerIds: [ownerID], deviceIds: [goodDeviceID] }), {
    SESAME_RECOVERY_GENERATION: generation,
    SESAME_RETAIN_OWNER_ID: ownerID,
    SESAME_RETAIN_DEVICE_ID: goodDeviceID,
  })
  assert.deepEqual(approvalArguments(status, { ownerIds: [ownerID], deviceIds: [] }), [
    'recover', '--confirm', generation, '--retain-owner', ownerID, '--json',
  ])
  assert.throws(() => approvalSelection(status, { ownerIds: [], deviceIds: [] }), /owner must be retained/)
  assert.throws(() => approvalSelection(status, { ownerIds: [ownerID], deviceIds: [goodDeviceID, goodDeviceID] }), /twice/)
  assert.throws(() => approvalSelection(status, { ownerIds: ['e'.repeat(32)], deviceIds: [] }), /not part/)
  assert.throws(() => approvalSelection(status, { ownerIds: [ownerID], deviceIds: ['e'.repeat(32)] }), /not part/)
  assert.throws(() => approvalSelection({ ...status, owners: [{ ...status.owners[0], setupPending: true }] }, { ownerIds: [ownerID], deviceIds: [] }), /setup/)
  assert.throws(() => approvalEnvironment(status, { ownerIds: [ownerID], deviceIds: [goodDeviceID, badDeviceID] }), /one device placeholder/)
  assert.throws(() => approvalEnvironment({ ...status, generation: generation.toUpperCase() }, { ownerIds: [ownerID], deviceIds: [] }), /generation/)
})

test('the approval selection refuses revoked, expired and removed-owner devices', () => {
  const status = fictionalStatus()
  assert.throws(() => approvalSelection({ ...status, devices: [{ ...status.devices[0], revokedAt: '2026-01-02T00:00:00Z' }] }, { ownerIds: [ownerID], deviceIds: [goodDeviceID] }), /revoked/)
  assert.throws(() => approvalSelection({ ...status, devices: [{ ...status.devices[0], expiresAt: '2020-01-01T00:00:00Z' }] }, { ownerIds: [ownerID], deviceIds: [goodDeviceID] }), /expired/)
  assert.throws(() => approvalSelection(status, { ownerIds: [], deviceIds: [goodDeviceID] }), /owner must be retained/)
  assert.throws(() => approvalSelection({ ...status, owners: [] }, { ownerIds: [], deviceIds: [goodDeviceID] }), /not retained/)
})

test('an approval result must match the pending generation and the explicit selection', () => {
  const status = fictionalStatus()
  const result = { pending: false, generation, alreadyComplete: false, retainedOwners: 1, removedOwners: 0, retainedDevices: 1, revokedDevices: 1 }
  assert.equal(assertApprovalResult(result, { status, ownerIds: [ownerID], deviceIds: [goodDeviceID] }).retainedDevices, 1)
  assert.throws(() => assertApprovalResult({ ...result, generation: 'f'.repeat(32) }, { status, ownerIds: [ownerID], deviceIds: [goodDeviceID] }), /generation/)
  assert.throws(() => assertApprovalResult({ ...result, revokedDevices: 0 }, { status, ownerIds: [ownerID], deviceIds: [goodDeviceID] }), /revoked/)
  assert.throws(() => assertApprovalResult({ ...result, pending: true }, { status }), /pending/)
  assert.doesNotThrow(() => assertApprovalResult({ ...result, alreadyComplete: true, retainedOwners: 0, retainedDevices: 0 }, { status }))
})

test('recovery JSON is extracted without copying unrelated or sensitive output into an error', () => {
  assert.deepEqual(parseRecoveryJson('noise\n{"pending":true}\nmore', 'fixture'), { pending: true })
  assert.throws(() => parseRecoveryJson('download progress only', 'fixture'), (error) => error.message === 'fixture printed no JSON object')
  assert.throws(() => parseRecoveryJson('{ not json }', 'fixture'), /malformed JSON/)
  const sentinel = 'fictional-token-value'
  assert.throws(() => parseRecoveryJson(`download ${sentinel}`, 'fixture'), (error) => !error.message.includes(sentinel))
})

test('failure logs redact setup links and pairing codes', () => {
  assert.equal(
    redactSensitiveLog('setup http://localhost:8787/setup#token=abcdefghijklmnopqrstuvwxyz and link http://localhost/pair#code=ABCDEFGHIJKLMNOPQRSTUVWXYZ&fp=abc'),
    'setup http://localhost:8787/setup#token=[redacted] and link http://localhost/pair#code=[redacted]&fp=abc',
  )
  assert.equal(redactSensitiveLog('nothing to redact'), 'nothing to redact')
})

test('the installed schema comes from the migrations and a required transition cannot be skipped', () => {
  assert.equal(schemaFromMigrationNames(['0001_init.sql', '0002_updates.sql', '0006_recovery.sql']), 6)
  assert.throws(() => schemaFromMigrationNames([]), /no database migrations/)
  assert.throws(() => schemaFromMigrationNames(['0001_init.sql', '0001_again.sql']), /one number twice/)
  assert.equal(skipAllowedWithoutUpgrade(1), true)
  assert.equal(skipAllowedWithoutUpgrade(6), false)
})

test('the reverse proxy recipes still hold the lines the proxy check rewrites', () => {
  const nginx = read('deploy', 'selfhost', 'nginx.conf')
  replaceOnce(nginx, 'ssl_certificate /etc/letsencrypt/live/sesame.example.net/fullchain.pem;', 'x', 'the certificate path')
  replaceOnce(nginx, 'ssl_certificate_key /etc/letsencrypt/live/sesame.example.net/privkey.pem;', 'x', 'the key path')
  assert.equal(nginx.split('server_name sesame.example.net;').length - 1, 2)
  assert.match(read('deploy', 'selfhost', 'Caddyfile'), /^\{\$SESAME_HOST\} \{/)
  assert.match(read('deploy', 'selfhost', 'compose.traefik.yaml'), /traefik\.http\.routers\.sesame\.entrypoints: websecure/)
})

test('the proxy check images are pinned by digest', () => {
  const dockerfile = read('scripts', 'selfhost-proxy-images', 'Dockerfile')
  const bases = [...dockerfile.matchAll(/^FROM (\S+) AS (\w+)$/gm)]
  assert.deepEqual(bases.map((match) => match[2]).sort(), ['caddy', 'nginx', 'traefik'])
  for (const [, image] of bases) assert.match(image, /@sha256:[0-9a-f]{64}$/)
})

test('pull requests build and run the release image, the quick start, the upgrade and every proxy recipe', () => {
  const workflow = read('.github', 'workflows', 'ci.yml')
  const image = jobBlock(workflow, 'selfhost-image')
  assert.match(image, /run: node scripts\/build-selfhost-images\.mjs\n/)
  assert.match(image, /docker\/setup-qemu-action@[0-9a-f]{40}/)
  assert.match(image, /--arch arm64 .* --run\n/)
  for (const [job, command] of [
    ['selfhost-quickstart', 'node scripts/selfhost-quickstart.mjs --mode local'],
    ['selfhost-upgrade', 'node scripts/selfhost-upgrade-check.mjs --from-published'],
    ['selfhost-proxy', 'node scripts/selfhost-proxy-check.mjs'],
  ]) {
    const block = jobBlock(workflow, job)
    assert.match(block, /needs: selfhost-image\n/, `${job} must reuse the image the selfhost-image job built`)
    assert.ok(block.includes(command), `${job} must run ${command}`)
  }
  assert.match(jobBlock(workflow, 'selfhost-proxy'), /proxy: \[caddy, nginx, traefik\]/)
  assert.match(workflow, /schedule:\n\s+- cron: /, 'a scheduled run must notice a pinned package that drifted from its base image')
})

test('candidate CI tests recovery from the current checkout and image without skipping it', () => {
  const workflow = read('.github', 'workflows', 'ci.yml')
  const quickstart = jobBlock(workflow, 'selfhost-quickstart')
  assert.match(quickstart, /node scripts\/selfhost-quickstart\.mjs --mode local --image sesame-selfhost:ci-amd64/)
  assert.doesNotMatch(quickstart, /\.selfhost-release/)
  const upgrade = jobBlock(workflow, 'selfhost-upgrade')
  assert.match(upgrade, /node scripts\/selfhost-upgrade-check\.mjs --from-published --to sesame-selfhost:ci-amd64/)
  assert.doesNotMatch(upgrade, /--none-ok/)
  const smoke = jobBlock(workflow, 'selfhost')
  assert.match(smoke, /npm run selfhost:smoke/)
  const smokeSource = read('scripts', 'selfhost-smoke.mjs')
  assert.match(smokeSource, /refuses to start before recovery approval/)
  assert.match(smokeSource, /retained device and signed Sync history/)
  const upgradeSource = read('scripts', 'selfhost-upgrade-check.mjs')
  assert.match(upgradeSource, /readInstalledSchema/)
  assert.match(upgradeSource, /skipAllowedWithoutUpgrade/)
  assert.match(upgradeSource, /schema transition cannot be skipped/)
})

test('the published image job runs the selected release source, not the workflow checkout', () => {
  const reusable = read('.github', 'workflows', 'selfhost-published.yml')
  assert.match(reusable, /^permissions:\n  contents: read$/m)
  const steps = stepBlocks(jobBlock(reusable, 'published'))
  const hidden = steps.filter((step) => step.includes('path: .selfhost-release'))
  assert.equal(hidden.length, 1, 'the workflow must check the selected release into one separate hidden directory')
  assert.match(hidden[0], /uses: actions\/checkout@[0-9a-f]{40}/)
  assert.match(hidden[0], /ref: refs\/tags\/v\$\{\{ steps\.release\.outputs\.version \}\}/)
  assert.match(hidden[0], /persist-credentials: false/)
  const runs = steps.filter((step) => step.includes('node scripts/selfhost-'))
  assert.equal(runs.length, 3, 'the published job must run the quick start, image and upgrade scripts')
  for (const step of runs) {
    assert.match(step, /working-directory: \.selfhost-release/, 'a published check runs a script from the wrong checkout')
    assert.ok(steps.indexOf(hidden[0]) < steps.indexOf(step), 'a published check runs before the release source is checked out')
  }
  assert.match(reusable, /docker logout ghcr\.io/)
  assert.match(reusable, /selfhost-quickstart\.mjs --mode published .*--verify-attestations/)
  assert.match(reusable, /--arch arm64 .* --run/)
  assert.match(reusable, /selfhost-upgrade-check\.mjs --from-published/)
  assert.doesNotMatch(reusable, /docker\/login-action/)
  assert.doesNotMatch(reusable, /packages: write/)
})

test('a release is not finished until the published image source passes its own checks without credentials', () => {
  const release = read('.github', 'workflows', 'release.yml')
  assert.match(jobBlock(release, 'publish'), /needs: \[images, selfhost\]\n/, 'the release that the quick start reads must not exist before the self-host image does')
  const published = jobBlock(release, 'published')
  assert.match(published, /needs: \[verify, publish, selfhost\]\n/)
  assert.match(published, /uses: \.\/\.github\/workflows\/selfhost-published\.yml\n/)
})
