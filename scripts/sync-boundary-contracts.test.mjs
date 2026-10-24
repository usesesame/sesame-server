import assert from 'node:assert/strict'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const read = (...parts) => readFileSync(join(root, ...parts), 'utf8')

function sourceFiles(dir, pattern) {
  return readdirSync(join(root, dir)).flatMap((name) => {
    const relative = join(dir, name)
    if (statSync(join(root, relative)).isDirectory()) return sourceFiles(relative, pattern)
    return pattern.test(name) ? [relative] : []
  })
}

const goFiles = (dir) => sourceFiles(dir, /\.go$/).filter((file) => !file.endsWith('_test.go'))

test('Sync is off by default: the operator flag falls back to false and gates every route', () => {
  const server = read('internal', 'httpapi', 'public_routes.go')
  const definition = server.match(/func \(a \*api\) syncEnabled\(ctx context\.Context\) bool \{([^}]*)\}/)
  assert.ok(definition, 'syncEnabled is no longer a single function this test can read')
  assert.match(definition[1], /runtimeFlagBool\(ctx, "cloud_sync_available", false\)/, 'the flag must default to false')
  assert.match(definition[1], /a\.config\.Sync != nil/, 'a configured service is required as well as the flag')

  const routes = read('internal', 'httpapi', 'sync_routes.go')
  assert.match(routes, /handler == nil \|\| !a\.syncEnabled\(request\.Context\(\)\)/, 'every Sync route must check the flag before it reaches a handler')
  const registered = [...routes.matchAll(/a\.route\(mux, sync, "/g)].length
  assert.ok(registered > 0, 'no Sync routes were found')
  assert.equal([...routes.matchAll(/a\.withSync\(response, request,/g)].length, registered, 'every registered Sync route goes through withSync')

  const capabilities = read('internal', 'httpapi', 'public_routes.go')
  assert.doesNotMatch(capabilities, /"cloudSyncAvailable":\s+true/, 'nothing may report Sync as available by a literal')
})

test('the rules live in one service and the storage packages hold none', () => {
  for (const file of goFiles(join('internal', 'syncv3http'))) {
    const source = read(file)
    assert.doesNotMatch(source, /database\/sql|pgx|modernc/, `${file} must not reach a database`)
  }
  for (const file of goFiles(join('internal', 'syncsvc', 'pgstore'))) {
    assert.doesNotMatch(read(file), /syncproto\.(?:VerifyHeader|VerifyDeviceList)|\.Verify\(/, `${file} must store what the service verified and verify nothing itself`)
  }
  const service = read('internal', 'syncsvc', 'service.go')
  for (const rule of ['VerifyHeader', 'Extend(', 'RecordNonce', 'signed.Verify']) {
    assert.ok(service.includes(rule), `the service no longer calls ${rule}`)
  }
})

test('the Sync service stores bytes it cannot read', () => {
  for (const dir of ['syncproto', 'syncsvc', 'syncv3http']) {
    for (const file of goFiles(join('internal', dir))) {
      assert.doesNotMatch(
        read(file),
        /chacha20poly1305|crypto\/aes|crypto\/cipher|secretbox|nacl/,
        `${file} must not decrypt anything; the service holds ciphertext only`,
      )
    }
  }
})

test('every route that changes state needs a device signature', () => {
  const handler = read('internal', 'syncv3http', 'handler.go')
  const changing = ['vault', 'pairing-begin', 'pairing-answer', 'pairing-confirm', 'pairing-cancel', 'approve', 'remove', 'activate', 'upload', 'reset']
  for (const operation of changing) {
    const match = handler.match(new RegExp(`Authenticate\\(response, request, "${operation}"\\)[\\s\\S]*?\\n}\\n`))
    assert.ok(match, `no handler authenticates ${operation}`)
    assert.match(match[0], /readSigned(?:JSON|Empty)/, `${operation} must read a signed request`)
  }
  const reads = ['state', 'device-list', 'devices', 'pairing-mine', 'key-package', 'headers', 'download']
  for (const operation of reads) {
    const match = handler.match(new RegExp(`Authenticate\\(response, request, "${operation}"\\)[\\s\\S]*?\\n}\\n`))
    assert.ok(match, `no handler authenticates ${operation}`)
    assert.doesNotMatch(match[0], /readSigned/, `${operation} reads only and must not be a signed route`)
  }
})

test('a request signature binds the token and is used once', () => {
  const request = read('internal', 'syncproto', 'request.go')
  for (const part of ['body', 'token', 'request.Timestamp', 'request.Nonce', 'request.Method', 'request.Target']) {
    assert.ok(request.includes(part), `the signed request payload no longer covers ${part}`)
  }
  const service = read('internal', 'syncsvc', 'service.go')
  assert.match(service, /tx\.RecordNonce\(scope, request\.Nonce, s\.now\(\), expires\)/, 'the nonce is recorded in the same transaction as the action')
  assert.match(service, /return ErrReplay/, 'a reused nonce must be refused')
})

test('receipts are signed with the key the desktop already pins', () => {
  const main = read('cmd', 'api', 'main.go')
  assert.match(main, /syncsvc\.Config\{ReceiptKey: capabilityKey\}/, 'the receipt key must be the capability signing key')
  assert.doesNotMatch(main, /GenerateKey/, 'no entry point may invent its own receipt key')
})

test('signing and key agreement use separate keys', () => {
  const entry = read('internal', 'syncproto', 'devicelist.go')
  assert.match(entry, /SubjectSigningKey\s+string/)
  assert.match(entry, /SubjectPackageKey\s+string/)
  const migration = read('internal', 'accounts', 'migrations', '0046_sync_v3.sql')
  assert.match(migration, /signing_key/)
  assert.match(migration, /package_key/)
})

test('the Sync tables hold no plaintext column', () => {
  const migration = ['0046_sync_v3.sql', '0047_sync_v3_checkpoints.sql'].map((name) => read('internal', 'accounts', 'migrations', name)).join('\n')
  assert.doesNotMatch(migration, /\b(?:plaintext|password|secret|vault_key|label)\b/i, 'a Sync table gained a column that could hold readable data')
  assert.match(migration, /sealed_label TEXT NOT NULL CHECK \(char_length\(sealed_label\) <= 400\)/, 'a sealed label needs a length check')
})

test('only the Sync packages import the Sync packages', () => {
  const allowed = new Set([
    join('cmd', 'api', 'main.go'),
    join('cmd', 'sesame-server', 'application.go'),
    join('cmd', 'synctestserver', 'main.go'),
    join('internal', 'httpapi', 'sync_routes.go'),
    join('internal', 'httpapi', 'server.go'),
    join('internal', 'selfhost', 'server', 'server.go'),
    join('internal', 'selfhost', 'server', 'sync.go'),
    join('internal', 'selfhost', 'sqlitestore', 'sync.go'),
  ])
  const scanned = [...goFiles('cmd'), ...goFiles(join('internal', 'httpapi'))]
  if (existsSync(join(root, 'internal', 'selfhost'))) scanned.push(...goFiles(join('internal', 'selfhost')))
  for (const file of scanned) {
    if (!/internal\/sync(?:svc|proto|v3http)/.test(read(file))) continue
    assert.ok(allowed.has(file), `${file} reaches into the Sync packages`)
  }
})

test('the Sync test server is built only with its tag and no image or release script names it', () => {
  const source = read('cmd', 'synctestserver', 'main.go')
  assert.match(source.split('\n')[0], /^\/\/go:build synctestserver$/, 'the first line must be the build constraint')
  assert.match(source, /memstore\.New\(\)/, 'it keeps its data in memory and never opens a database')
  assert.doesNotMatch(source, /database\/sql|pgx|modernc|os\.Getenv/, 'it reads no database and no configuration')
  assert.match(source, /127\.0\.0\.1:0/, 'it listens on the loopback address only')
  for (const file of ['Dockerfile', 'Dockerfile.selfhost', 'package.json']) {
    assert.doesNotMatch(read(file), /synctestserver/, `${file} must not name the Sync test server`)
  }
  for (const file of sourceFiles('scripts', /\.mjs$/).filter((name) => !name.endsWith('sync-boundary-contracts.test.mjs'))) {
    assert.doesNotMatch(read(file), /synctestserver/, `${file} must not name the Sync test server`)
  }
})
