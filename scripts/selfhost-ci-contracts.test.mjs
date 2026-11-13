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
import { assertArchiveCheckReport, assertDrillRecord, assertExportRecord, assertVerifyRecord, resolveAgeTools } from './selfhost-export-check.mjs'

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
    ['selfhost-quickstart', 'node scripts/selfhost-export-check.mjs --mode docker --image sesame-selfhost:ci-amd64'],
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
  assert.match(quickstart, /node scripts\/selfhost-export-check\.mjs --mode docker --image sesame-selfhost:ci-amd64/)
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

const archiveCheckReport = Object.freeze({
  schemaVersion: 6,
  supported: 6,
  needsMigration: false,
  auditRows: 4,
  auditHeadHash: 'c'.repeat(64),
  ownersVerified: 1,
  secretsVerified: true,
  hasConfig: false,
})

const archiveDigest = 'b'.repeat(64)
const commonRecord = (operation) => ({
  operation,
  startedAt: '2026-01-02T03:04:05Z',
  completedAt: '2026-01-02T03:05:05Z',
  success: true,
  errorCode: null,
  snapshot: null,
  encryption: null,
  publication: null,
  verification: null,
  drill: null,
})
const exportRecord = {
  ...commonRecord('export'),
  snapshot: { completedAt: '2026-01-02T03:04:30Z', bytes: 4096, sha256: 'a'.repeat(64) },
  encryption: { completedAt: '2026-01-02T03:04:45Z', bytes: 4200, sha256: archiveDigest },
  publication: { path: '/mnt/sesame-exports/sesame-example.tar.age', readbackConfirmed: true, sameFilesystemAsSource: true },
}
const verifyRecord = {
  ...commonRecord('verify'),
  verification: { archiveSha256: archiveDigest, checkedAt: '2026-01-02T03:06:05Z', report: { ...archiveCheckReport } },
}
const drillRecord = {
  ...commonRecord('drill'),
  drill: { restoredAt: '2026-01-02T03:07:05Z', recoveryPending: true, serviceProbed: false },
}

test('the encrypted backup records hold the facts the operator decisions require', () => {
  assert.equal(assertExportRecord(exportRecord).publication.readbackConfirmed, true)
  assert.equal(assertExportRecord({ ...exportRecord, publication: { ...exportRecord.publication, sameFilesystemAsSource: null } }).publication.sameFilesystemAsSource, null)
  assert.equal(assertVerifyRecord(verifyRecord, { archiveDigest, expectedSchema: 6 }).verification.report.schemaVersion, 6)
  assert.equal(assertDrillRecord(drillRecord).drill.recoveryPending, true)
  assert.equal(assertArchiveCheckReport(archiveCheckReport, { expectedSchema: 6 }).ownersVerified, 1)
})

test('an encrypted backup record refuses missing facts, failed operations and leaked values', () => {
  assert.throws(() => assertExportRecord({ ...exportRecord, success: false }), /failure record|success/)
  assert.throws(() => assertExportRecord({ ...exportRecord, snapshot: null }), /snapshot/)
  assert.throws(() => assertExportRecord({ ...exportRecord, publication: { ...exportRecord.publication, readbackConfirmed: false } }), /read back/)
  assert.throws(() => assertExportRecord({ ...exportRecord, publication: { ...exportRecord.publication, path: 'relative.tar.age' } }), /absolute/)
  assert.throws(() => assertExportRecord(exportRecord, { secrets: ['/mnt/sesame-exports'] }), /private identity, recipient or credential/)
  assert.throws(() => assertExportRecord({ ...exportRecord, success: true, errorCode: 'history_failed' }), /error code/)
  assert.throws(() => assertVerifyRecord({ ...verifyRecord, verification: { ...verifyRecord.verification, report: { ...archiveCheckReport, extra: true } } }), /exactly the expected fields/)
  const missingReport = { ...archiveCheckReport }
  delete missingReport.secretsVerified
  assert.throws(() => assertVerifyRecord({ ...verifyRecord, verification: { ...verifyRecord.verification, report: missingReport } }), /exactly the expected fields/)
  assert.throws(() => assertVerifyRecord(verifyRecord, { archiveDigest: 'd'.repeat(64) }), /another archive digest/)
  assert.throws(() => assertDrillRecord({ ...drillRecord, drill: { ...drillRecord.drill, recoveryPending: false } }), /pending recovery/)
  assert.throws(() => assertDrillRecord({ ...drillRecord, drill: { ...drillRecord.drill, serviceProbed: true } }), /network service/)
  assert.throws(() => assertArchiveCheckReport({ ...archiveCheckReport, schemaVersion: 5 }), /another schema/)
  assert.throws(() => assertArchiveCheckReport({ ...archiveCheckReport, auditRows: -1 }), /auditRows/)
  const emptyAudit = { ...archiveCheckReport, auditRows: 0, auditHeadHash: '0'.repeat(64) }
  assert.equal(assertArchiveCheckReport(emptyAudit).auditRows, 0)
  assert.throws(() => assertArchiveCheckReport({ ...emptyAudit, auditHeadHash: '' }), /SHA-256/)
  assert.throws(() => assertArchiveCheckReport({ ...emptyAudit, auditHeadHash: 'a'.repeat(64) }), /genesis/)
})

test('the real-age runner fails when the required age tools are absent or the wrong version', () => {
  assert.throws(() => resolveAgeTools({ age: '/nonexistent/sesame-age', ageKeygen: '/nonexistent/sesame-age-keygen' }), /age v1\.3\.2 is required/)
  assert.throws(() => resolveAgeTools({ age: '/bin/echo', ageKeygen: '/bin/echo' }), /instead of v1\.3\.2/)
})

test('the guide documents encrypted export, offline identity checks and a disposable drill', () => {
  const section = sectionBlocks(document, '### Encrypted backup export')
  assert.equal(section.length, 6, 'the encrypted export section must hold the identity, native export, Compose export, native verify, image verify and drill blocks')
  assert.match(section[0], /age-keygen -o \/media\/recovery\/identity\.txt/)
  assert.match(section[0], /age-keygen -y \/media\/recovery\/identity\.txt > \/etc\/sesame\/recipients\.txt/)
  assert.match(section[1], /export-backup\.mjs export \\/)
  assert.match(section[1], /--native \/usr\/local\/bin\/sesame-server --data-dir \/var\/lib\/sesame/)
  assert.match(section[1], /--destination \/mnt\/sesame-exports --recipients-file \/etc\/sesame\/recipients\.txt/)
  assert.doesNotMatch(section[1], /--identity-file|AGE-SECRET-KEY/)
  assert.match(section[2], /--compose-service sesame/)
  assert.doesNotMatch(section[2], /--identity-file|AGE-SECRET-KEY/)
  assert.match(section[3], /export-backup\.mjs verify \\/)
  assert.match(section[3], /--archive \/mnt\/sesame-exports\/sesame-example\.tar\.age/)
  assert.match(section[3], /--identity-file \/media\/recovery\/identity\.txt --native \/usr\/local\/bin\/sesame-server/)
  assert.match(section[4], /export-backup\.mjs verify \\/)
  assert.match(section[4], /--image "ghcr\.io\/usesesame\/sesame-server-selfhost:\$\{SESAME_IMAGE_TAG:\?/)
  assert.match(section[5], /export-backup\.mjs drill \\/)
  assert.match(section[5], /--identity-file \/media\/recovery\/identity\.txt --native \/usr\/local\/bin\/sesame-server/)
  const sectionText = document.slice(document.indexOf('### Encrypted backup export'), document.indexOf('### Restore'))
  assert.match(sectionText, /4 GiB/)
  assert.match(sectionText, /1 MiB/)
  assert.match(sectionText, /scheduled local backups still run unchanged/)
  assert.match(sectionText, /recovery point objective/)
  assert.match(sectionText, /restoration time/)
  assert.match(sectionText, /not off-host protection/)
  assert.match(sectionText, /source checkout of the release tag/)
  assert.match(sectionText, /predates the encrypted export flags/)
  assert.match(sectionText, /never accepts or asks for a private identity/)
  assert.match(sectionText, /restored state is left pending/)
  assert.match(sectionText, /does not approve recovery, retain a device, start a network service/)
  assert.match(sectionText, /fresh volume/)
  assert.match(sectionText, /--state-dir/)
  assert.match(sectionText, /--age/)
  assert.match(sectionText, /--work-dir/)
  assert.doesNotMatch(sectionText, /[—–]/)
})

test('the encrypted backup scripts and package scripts run the operator contract and the real-age journey', () => {
  const packageJson = JSON.parse(read('package.json'))
  assert.equal(packageJson.scripts['backup:contract'], 'node --test scripts/export-backup.test.mjs')
  assert.match(packageJson.scripts['backup:contract:age'], /selfhost-export-check\.mjs --mode native --skip-console-build/)
  for (const gate of ['release:contract', 'deploy:contract', 'sync:contract', 'sync:wire', 'audit:contract', 'governance:contract', 'setup:contract', 'selfhost:contract', 'backup:contract', 'openapi:check', 'lint', 'build', 'test', 'test:fuzz']) {
    assert.ok(packageJson.scripts.ci.includes(`npm run ${gate}`), `the ordinary CI command lost ${gate}`)
  }
  assert.equal(packageJson.dependencies, undefined)
  assert.equal(packageJson.devDependencies, undefined)

  const runner = read('scripts', 'selfhost-export-check.mjs')
  for (const token of ['--mode', '--image', '--age', '--age-keygen', '--skip-console-build', '--identity-file', '--compose-service', '--work-dir', '--state-dir', 'runEncryptedJourney', 'corrupt-final', 'not-an-archive', 'recoveryPending', 'serviceProbed', '--encrypted-backup']) {
    assert.ok(runner.includes(token), `the encrypted backup runner does not exercise ${token}`)
  }
  assert.match(runner, /resolveAgeTools/)
  assert.match(runner, /assert\.notEqual\(.*status, 0, 'the export accepted a private identity'\)/s)

  const quickstart = read('scripts', 'selfhost-quickstart.mjs')
  assert.match(quickstart, /--encrypted-backup/)
  assert.match(quickstart, /runEncryptedJourney/)
  assert.match(quickstart, /resolveAgeTools/)
  assert.match(quickstart, /composeDataDigest/)
  assert.match(quickstart, /volumeSet/)
})

test('required candidate CI builds pinned age outside the image and runs real-age encryption checks', () => {
  const workflow = read('.github', 'workflows', 'ci.yml')
  assert.match(workflow, /^permissions:\n  contents: read$/m)
  assert.match(workflow, /image: postgres:18-alpine@sha256:[0-9a-f]{64}/)
  const selfhost = jobBlock(workflow, 'selfhost')
  assert.match(selfhost, /actions\/setup-go@[0-9a-f]{40}/)
  assert.match(selfhost, /go install filippo\.io\/age\/cmd\/age@v1\.3\.2 filippo\.io\/age\/cmd\/age-keygen@v1\.3\.2/)
  assert.match(selfhost, /npm run selfhost:smoke -- --skip-console-build/)
  assert.match(selfhost, /npm run backup:contract:age/)
  assert.match(selfhost, /run: npm run backup:contract$/m)
  assert.match(selfhost, /SESAME_REQUIRE_AGE: ['"]?1['"]?/)
  assert.doesNotMatch(selfhost, /continue-on-error|if: .*age/)
  const quickstart = jobBlock(workflow, 'selfhost-quickstart')
  assert.match(quickstart, /needs: selfhost-image\n/)
  assert.match(quickstart, /actions\/setup-go@[0-9a-f]{40}/)
  assert.match(quickstart, /go install filippo\.io\/age\/cmd\/age@v1\.3\.2 filippo\.io\/age\/cmd\/age-keygen@v1\.3\.2/)
  assert.match(quickstart, /node scripts\/selfhost-export-check\.mjs --mode docker --image sesame-selfhost:ci-amd64/)
  assert.doesNotMatch(quickstart, /continue-on-error|if: .*age/)
  assert.doesNotMatch(read('Dockerfile.selfhost'), /filippo|age-keygen|age\s/)
})

test('the guided installation guide names three profiles and the limits of generation', () => {
  const guide = sectionBlocks(document, '## Guided installation')
  assert.equal(guide.length, 4, 'the guided installation must hold the generation, inspection, Caddy and Tailscale blocks')
  const generation = guide.find((block) => block.includes('install --profile local'))
  assert.ok(generation, 'the guided installation has no generation block')
  assert.match(generation, /^SESAME_IMAGE_TAG=0\.4\.1$/m)
  assert.match(generation, /^sesame-server install --profile local --version "\$SESAME_IMAGE_TAG" --dir \.\/sesame-local$/m)
  assert.match(generation, /^sesame-server install --profile caddy --host vault\.example\.com --version "\$SESAME_IMAGE_TAG" --dir \.\/sesame-caddy$/m)
  assert.match(generation, /^sesame-server install --profile tailscale --host sesame\.example-tailnet\.ts\.net --version "\$SESAME_IMAGE_TAG" --dir \.\/sesame-tailscale$/m)
  const caddy = guide.find((block) => block.includes('caddy run --config'))
  const tailscale = guide.find((block) => block.includes('tailscale serve --bg'))
  assert.match(caddy, /^cd sesame-caddy$/m)
  assert.match(caddy, /^docker compose up -d$/m)
  assert.match(caddy, /^sudo caddy run --config \.\/Caddyfile --envfile \.\/caddy\.env$/m)
  assert.match(tailscale, /^cd sesame-tailscale$/m)
  assert.match(tailscale, /^docker compose up -d$/m)
  assert.match(tailscale, /^sudo tailscale serve --bg --https=443 http:\/\/127\.0\.0\.1:8787$/m)
  assert.match(tailscale, /^sudo tailscale serve status$/m)
  const text = document.slice(document.indexOf('## Guided installation'), document.indexOf('## First run and pairing'))
  assert.match(text, /only generates files/)
  assert.match(text, /does not pull an image, start a container, configure Caddy or Tailscale, open the data directory, contact the network or test the host/)
  assert.match(text, /older binary, such as the 0\.4\.1 release, does not gain the command even if/)
  assert.match(text, /target must be a new path whose parent directory already exists/)
  assert.match(text, /refuses an existing file, directory or symbolic link/)
  assert.match(text, /does not create missing parent directories/)
  assert.match(text, /hard kill can leave an incomplete private directory/)
  assert.match(text, /mode 0700/)
  assert.match(text, /mode 0600/)
  assert.match(text, /On Windows/)
  assert.match(text, /does not set an access control list/)
  assert.match(text, /inherits its parent's permissions/)
  assert.match(text, /does not enforce them on Windows/)
  assert.match(text, /Linux is the only runtime tested/)
  assert.match(text, /Windows and macOS runtime support remains unvalidated/)
  assert.match(text, /final suffix contains only letters/)
  assert.match(text, /internationalized suffix beginning `xn--`/)
  assert.match(text, /docker compose config/)
  assert.match(text, /--envfile \.\/caddy\.env/)
  assert.match(text, /EnvironmentFile=\/absolute\/path\/to\/sesame-caddy\/caddy\.env/)
  assert.match(text, /sudo systemctl daemon-reload/)
  assert.match(text, /tailscale serve --bg --https=443 http:\/\/127\.0\.0\.1:8787/)
  assert.match(text, /not `tailscale funnel`/)
  assert.match(text, /MagicDNS and HTTPS certificates/)
  assert.match(text, /prerequisites remain operator work/)
  assert.match(text, /plan\.json/)
  assert.match(text, /does not validate the network/)
  assert.doesNotMatch(text, /[—–]/)
  const quickStart = sectionBlocks(document, '## Quick start')
  assert.match(quickStart[0], /releases\/latest/)
  assert.match(quickStart[1], /docker compose up -d/)
})

test('the ordinary CI command runs the native guided installation journey', () => {
  const packageJson = JSON.parse(read('package.json'))
  assert.equal(packageJson.scripts['selfhost:install'], 'node ./scripts/selfhost-install-check.mjs')
  assert.ok(packageJson.scripts.ci.includes('npm run selfhost:install'), 'the ordinary CI command does not run the guided installation journey')
  assert.equal(packageJson.dependencies, undefined)
  assert.equal(packageJson.devDependencies, undefined)

  const runner = read('scripts', 'selfhost-install-check.mjs')
  for (const token of ['sectionBlocks(document, \'## Guided installation\')', 'runGuideBlock', 'inspectTarget', 'assertExistingTargetsRefused', 'assertInvalidValuesRefused', 'snapshotTree', 'createServiceShims', 'concurrent', 'Not run by this native check']) {
    assert.ok(runner.includes(token), `the guided installation journey does not exercise ${token}`)
  }
  assert.match(runner, /dockerConfig = 'docker compose config'/)
  assert.doesNotMatch(runner, /docker compose up/)
  assert.doesNotMatch(runner, /continue-on-error/)
})
