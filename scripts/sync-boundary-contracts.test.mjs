import assert from 'node:assert/strict'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

// Split from the former monorepo cross-repo suite. The desktop-side assertions
// live in the desktop repository's tools/sync-boundary-contracts.test.mjs.

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const read = (...parts) => readFileSync(join(root, ...parts), 'utf8')

function sourceFiles(dir, pattern) {
  return readdirSync(join(root, dir)).flatMap((name) => {
    const relative = join(dir, name)
    if (statSync(join(root, relative)).isDirectory()) return sourceFiles(relative, pattern)
    return pattern.test(name) ? [relative] : []
  })
}

test('Sync stays disabled: the capability flag is never defaulted on', () => {
  const routes = read('internal', 'httpapi', 'sync_routes.go')
  assert.match(
    routes,
    /if !a\.syncEnabled\(request\.Context\(\)\) \{/,
    'requireSync must gate on syncEnabled, so the route gate and the reported capability can never disagree',
  )

  const server = read('internal', 'httpapi', 'public_routes.go')
  const definition = server.match(/func \(a \*api\) syncEnabled\(ctx context\.Context\) bool \{([^}]*)\}/)
  assert.ok(definition, 'syncEnabled is no longer a single function this test can read')
  assert.match(
    definition[1],
    /runtimeFlagBool\(ctx, "cloud_sync_available", false\)/,
    'syncEnabled must read the flag with an explicit false fallback',
  )
  assert.match(
    definition[1],
    /&&\s*a\.config\.Sync != nil/,
    'syncEnabled must also require a configured store, or the flag alone would enable Sync',
  )

  for (const [name, source] of [['sync_routes.go', routes], ['syncEnabled', definition[1]]]) {
    assert.doesNotMatch(
      source,
      /capabilityEnabled\(/,
      `${name}: capabilityEnabled falls back to true when no admin flag store is configured, which is every test and every local run. Sync must fail closed.`,
    )
  }
})

test('Sync stays disabled: nothing reports it as available on the flag alone', () => {
  for (const parts of [
    ['internal', 'httpapi', 'capabilities.go'],
    ['internal', 'httpapi', 'public_routes.go'],
  ]) {
    const source = read(...parts)
    for (const line of source.split('\n')) {
      if (!/"sync"|cloudSyncAvailable/.test(line)) continue
      if (!/cloud_sync_available/.test(line)) continue
      assert.fail(
        `${parts.join('/')} reports Sync from the flag directly. Use a.syncEnabled:\n  ${line.trim()}`,
      )
    }
  }
})

test('Sync stays disabled: the store is not wired into the running API', () => {
  const main = read('cmd', 'api', 'main.go')
  assert.doesNotMatch(
    main,
    /Sync:\s*syncstore\./,
    'configuring Config.Sync is a deliberate Phase 5 step. Enabling Sync should take a code change and a flag change, not a flag change alone.',
  )
})

test('Sync stays disabled: only a development binary wires the store', () => {
  const preview = read('cmd', 'api-sync-preview', 'main.go')
  assert.match(
    preview,
    /os\.Getenv\("SESAME_ENV"\)\) != "development"/,
    'the Sync preview API must refuse to start outside development',
  )
  assert.match(preview, /Sync:\s+syncstore\.New\(/, 'the preview API no longer wires the Sync store')

  const dockerfile = read('Dockerfile')
  assert.doesNotMatch(
    dockerfile,
    /api-sync-preview/,
    'the container image builds the Sync preview binary, which would ship a Sync-wired API',
  )

  const wiring = []
  for (const file of sourceFiles(join('cmd'), /\.go$/)) {
    if (/Sync:\s+syncstore\./.test(read(file))) wiring.push(file)
  }
  assert.deepEqual(
    wiring.map((file) => file.split(sep).join('/')),
    ['cmd/api-sync-preview/main.go'],
    'a binary other than the development preview wires the Sync store',
  )
})

test('the Sync service stores bytes it cannot read', () => {
  const store = read('internal', 'syncstore', 'envelopes.go')
  assert.doesNotMatch(store, /string\(envelope\.Ciphertext\)/)
  assert.doesNotMatch(store, /json\.Unmarshal\(envelope\.Ciphertext/)
  assert.match(
    store,
    /Ciphertext\s+\[\]byte/,
    'envelope ciphertext must stay []byte in the store',
  )
})

test('signing and key agreement use separate keys', () => {
  const migration = read(
    'internal',
    'accounts',
    'migrations',
    '0024_sync_control_plane.sql',
  )
  assert.match(migration, /signing_public_key\s+BYTEA/)
  assert.match(migration, /encryption_public_key\s+BYTEA/)
})

test('the append-only Sync audit records no vault content', () => {
  const migration = read(
    'internal',
    'accounts',
    'migrations',
    '0024_sync_control_plane.sql',
  )
  const auditStart = migration.indexOf('CREATE TABLE IF NOT EXISTS sesame_sync_audit')
  assert.ok(auditStart >= 0, 'the sesame_sync_audit table is missing from the Sync control-plane migration')
  const audit = migration.slice(auditStart)
  const tableEnd = audit.indexOf(');')
  assert.ok(tableEnd >= 0, 'the sesame_sync_audit table definition never closes')
  const table = audit.slice(0, tableEnd)
  for (const forbidden of ['ciphertext', 'nonce', 'signature', 'label', 'size', 'bytes']) {
    assert.ok(
      !table.includes(forbidden),
      `sesame_sync_audit must not record ${forbidden}: an audit row must not describe the vault`,
    )
  }
  assert.match(migration, /sesame_sync_audit is append-only/)
})

test('nothing is persisted without verifying who signed it', () => {
  const envelopes = read('internal', 'syncstore', 'envelopes.go')
  assert.match(
    envelopes,
    /VerifySignature\(/,
    'AppendEnvelope persists without verifying the envelope signature',
  )
  const devices = read('internal', 'syncstore', 'devices.go')
  assert.match(
    devices,
    /VerifySignature\(/,
    'ApproveDevice persists a key package without verifying who signed it',
  )

  for (const [name, source] of [['envelopes.go', envelopes], ['devices.go', devices]]) {
    assert.match(
      source,
      /tx\.QueryRowContext\(ctx, `[\s\S]*?signing_public_key/,
      `${name} reads the signing key outside the transaction that persists`,
    )
  }

  const control = read('internal', 'syncproto', 'control_plane.go')
  const payload = control.match(
    /func \(key EncryptedKeyPackage\) signingPayload\(\) \(\[\]byte, error\) \{[\s\S]*?\n\}/,
  )
  assert.ok(payload, 'the key package signing payload is no longer a single function')
  assert.doesNotMatch(
    payload[0],
    /CreatedAt/,
    'the key package payload carries a server-stamped timestamp again, which no signer can know',
  )
})

test('per-vault quotas are enforced inside the transaction', () => {
  const devices = read('internal', 'syncstore', 'devices.go')
  const enroll = devices.match(/func \(s \*Store\) EnrollDevice\([\s\S]*?\n\}\n/)
  assert.ok(enroll, 'EnrollDevice is gone')
  assert.match(
    enroll[0],
    /syncproto\.MaxDevicesPerVault/,
    'enrollment no longer checks the device limit',
  )
  assert.match(
    enroll[0],
    /tx\.QueryRowContext\(ctx, `[\s\S]*?COUNT\(\*\) FROM sesame_sync_devices/,
    'the device count is read outside the transaction, so two concurrent enrollments can both pass it',
  )

  const envelopes = read('internal', 'syncstore', 'envelopes.go')
  assert.match(
    envelopes,
    /DELETE FROM sesame_sync_envelopes[\s\S]*?syncproto\.MaxSnapshotsPerVault/,
    'snapshots are no longer pruned to the retention limit, so a vault grows without bound',
  )
})

test('a serialisation failure is retried, not returned as an outage', () => {
  const store = read('internal', 'syncstore', 'store.go')
  assert.match(
    store,
    /pgErr\.Code == "40001"/,
    'nothing recognises a serialisation failure any more',
  )
  assert.match(
    store,
    /func \(s \*Store\) inTx\([\s\S]*?isSerializationFailure\(err\)/,
    'inTx no longer retries the transaction, so a lost serialisation looks like an outage',
  )
  assert.doesNotMatch(
    store,
    /isSerializationFailure\(ErrConflict\)|errors\.Is\(err, ErrConflict\)[\s\S]{0,40}retry/,
    'a conflict is being retried, which discards the other device changes',
  )
})

test('Sync entitlement and rate limiting are decided in one place', () => {
  const routes = read('internal', 'httpapi', 'sync_routes.go')
  const gate = routes.match(/func \(a \*api\) requireSyncCaller\([\s\S]*?\n\}\n/)
  assert.ok(gate, 'requireSyncCaller is gone, so entitlement is decided per handler again')
  for (const keyed of ['":account:"', '":device:"']) {
    assert.ok(
      gate[0].includes(keyed),
      `the gate no longer limits by ${keyed}, so one account across many addresses is unbounded`,
    )
  }
  const outside = routes.replace(gate[0], '')
  assert.doesNotMatch(
    outside,
    /a\.allowRequest\(/,
    'a sync handler limits by IP on its own again, so the account and device limits do not apply to it',
  )
  assert.doesNotMatch(
    outside,
    /a\.desktopConnectionForRequest\(/,
    'a sync handler resolves the desktop token outside the gate, which is how handlers drifted apart',
  )
})

test('the first device can be approved and a second can fetch its key', () => {
  const devices = read('internal', 'syncstore', 'devices.go')
  assert.doesNotMatch(
    devices,
    /func \(s \*Store\) BootstrapFirstDevice\(/,
    'first-device approval is a separate transaction again, which two concurrent enrollments can both lose',
  )
  assert.match(
    devices,
    /SELECT id FROM sesame_sync_vaults WHERE id = \$1 FOR UPDATE/,
    'enrollment no longer serialises on the vault row, so concurrent first enrollments race',
  )
  assert.match(
    devices,
    /state = "approved"/,
    'enrollment no longer approves the first device in a vault, so Sync cannot be turned on at all',
  )
})

test('removing a device rotates the vault key', () => {
  const rekey = read('internal', 'syncstore', 'rekey.go')
  assert.match(
    rekey,
    /func \(s \*Store\) RevokeAndRekey\(/,
    'RevokeAndRekey is gone, so removing a device no longer rotates the vault key',
  )
  const body = rekey.match(/func \(s \*Store\) RevokeAndRekey\([\s\S]*?\n\}\n/)[0]
  for (const [required, why] of [
    ['s.inTx(', 'the rekey is no longer one transaction'],
    ['appendEnvelopeTx', 'the re-encrypted head is not committed with the revocation'],
    ['sesame_sync_key_packages', 'survivors are not rewrapped to the new key'],
    ['FOR UPDATE', 'two concurrent rekeys can both claim the new epoch'],
  ]) {
    assert.ok(body.includes(required), why)
  }
})

test('removing a device is signed by the device asking', () => {
  const routes = read('internal', 'httpapi', 'sync_routes.go')
  assert.match(
    routes,
    /syncproto\.VerifyRevocationIntent\(/,
    'removal no longer requires the calling device to have signed it',
  )
})

test('revisions form a chain and the service signs what it accepted', () => {
  const envelope = read('internal', 'syncproto', 'envelope.go')
  const payload = envelope.match(/func \(envelope Envelope\) signingPayload\([\s\S]*?\n\}/)[0]
  assert.ok(
    payload.includes('PreviousDigest'),
    'the predecessor digest is outside the signed payload, so the service can rewrite the chain',
  )
  const store = read('internal', 'syncstore', 'envelopes.go')
  assert.match(
    store,
    /envelope\.PreviousDigest != expectedPrevious/,
    'the store no longer checks that a revision chains to what it actually holds',
  )
  assert.match(
    store,
    /syncproto\.SignReceipt\(/,
    'the service no longer signs what it accepted',
  )
})

test('one canonical AEAD context, shared by both languages', () => {
  assert.match(
    read('internal', 'syncproto', 'chain.go'),
    /func SnapshotAAD\(/,
    'the Go side no longer defines the canonical context, so nothing can check the desktop against it',
  )
  const fixture = JSON.parse(
    read('internal', 'syncproto', 'testdata', 'snapshot-aad.json'),
  )
  assert.ok(fixture.contextBase64, 'the AEAD context fixture carries no expected value')
})

test('stored ciphertext has a byte ceiling, not just a count', () => {
  assert.match(
    read('internal', 'syncproto', 'control_plane.go'),
    /MaxStoredBytesPerVault/,
    'the per-vault byte ceiling is gone',
  )
  assert.match(
    read('internal', 'syncstore', 'envelopes.go'),
    /SUM\(LENGTH\(ciphertext\)\)[\s\S]{0,400}?s\.byteBudget\(\)/,
    'the byte ceiling is no longer enforced before an envelope is stored',
  )
})

test('the cross-language signing fixture is asserted from Go', () => {
  const goTest = read('internal', 'syncproto', 'envelope_fixture_test.go')
  assert.match(goTest, /envelope-signing-payload\.json/)
  assert.match(goTest, /VerifySignature/)
  assert.match(
    goTest,
    /tampered\.Revision\s*=/,
    'the tampered envelope must change the signed Revision field',
  )
  assert.match(
    goTest,
    /tampered\.PreviousRevision\s*=/,
    'the tampered envelope must change the signed PreviousRevision field',
  )
  assert.match(
    goTest,
    /VerifySignature\([^)]*\);\s*err == nil/,
    'the Go fixture must prove a tampered envelope fails verification',
  )
  assert.match(
    goTest,
    /filepath\.Join\("testdata"/,
    'the Go fixture path must not escape the module, or the containerised build cannot see it',
  )
})

test('the Sync fuzz targets run inside the ci script', () => {
  const scripts = JSON.parse(read('package.json')).scripts
  assert.match(scripts.ci, /npm run test:fuzz/, 'ci no longer runs the Sync fuzz targets')
  const fuzzTargets = [...read('internal', 'syncproto', 'fuzz_test.go').matchAll(/^func (Fuzz\w+)\(/gm)].map((match) => match[1])
  assert.ok(fuzzTargets.length >= 4, 'the Sync fuzz targets are gone')
  for (const target of fuzzTargets) {
    assert.ok(
      scripts['test:fuzz'].includes(`-fuzz ${target} `),
      `test:fuzz does not run ${target}, so ci never explores it`,
    )
  }
})

test('a scheduled workflow runs the Sync fuzz targets', () => {
  const workflow = read('.github', 'workflows', 'sync-fuzz.yml')
  assert.match(workflow, /schedule:\s*\n\s+- cron:/, 'the fuzz workflow has no schedule')
  assert.match(workflow, /workflow_dispatch:/, 'the fuzz workflow cannot be started by hand')
  assert.match(workflow, /npm run test:fuzz/, 'the fuzz workflow does not run the fuzz targets')
  assert.match(workflow, /permissions:\s*\n\s+contents: read/, 'the fuzz workflow asks for more than read access')
  for (const action of workflow.matchAll(/uses:\s*(\S+)/g)) {
    assert.match(action[1], /@[0-9a-f]{40}$/, `${action[1]} is not pinned to a commit`)
  }
})

test('only the development preview runs Sync maintenance, and it honours the flag', () => {
  const preview = read('cmd', 'api-sync-preview', 'main.go')
  assert.match(preview, /syncstore\.RunMaintenance\(/, 'the preview binary no longer purges expired challenges and revoked devices')
  assert.match(
    preview,
    /maintenance\.Enabled = func[\s\S]*?cloud_sync_available[\s\S]*?err == nil && value == "true"/,
    'Sync maintenance must fail closed unless the cloud_sync_available flag reads true',
  )
  for (const file of sourceFiles('cmd', /\.go$/)) {
    if (file.split(sep).join('/') === 'cmd/api-sync-preview/main.go') continue
    assert.doesNotMatch(read(file), /syncstore\./, `${file} uses the Sync store outside the development preview`)
  }
})

test('the revoked device purge never deletes a device that a stored envelope names', () => {
  const store = read('internal', 'syncstore', 'store.go')
  const purge = store.match(/func \(s \*Store\) PurgeRevokedDevices\([\s\S]*?\n\}\n/)
  assert.ok(purge, 'PurgeRevokedDevices is gone')
  assert.match(
    purge[0],
    /NOT EXISTS \(SELECT 1 FROM sesame_sync_envelopes e WHERE e\.device_id = d\.id\)/,
    'the purge deletes devices without checking the envelopes that reference them, and the foreign key then fails the whole run',
  )
})
