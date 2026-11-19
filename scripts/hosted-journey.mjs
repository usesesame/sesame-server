import assert from 'node:assert/strict'
import { createPublicKey, randomBytes, verify } from 'node:crypto'
import { authenticator, expectError, expectStatus, request } from './selfhost-client.mjs'
import { headerFor, randomBase64url, signDeviceListEntry, signHeader, signKeyPackage, signedCall, syncDevice, tokenHeaders } from './sync-client.mjs'
import { deviceListDigest, headerDigest, keyPackageDigest, publicKeyFromSeed, receiptSigningPayload } from './sync-wire.mjs'
import { webBrowser } from './hosted-stack.mjs'

const adminEmail = 'operator.hosted@example.com'
const adminPassword = 'correct horse battery staple operator'
const accountEmail = 'ada.hosted@example.com'
const accountPassword = 'correct horse battery staple hosted'
const vaultId = 'vaultHostedAlpha0001'

function websiteBrowser(stack) {
  return webBrowser(stack.instance, stack.origins.web, '/v1/auth/csrf')
}

function adminBrowser(stack) {
  return webBrowser(stack.instance, stack.origins.admin, '/v1/admin/auth/csrf')
}

function linkFrom(output, label) {
  const match = output.match(/https?:\/\/\S+/)
  assert.ok(match, `adminctl ${label} printed no link`)
  return new URL(match[0])
}

function actionToken(message) {
  const match = message.text.match(/#token=([A-Za-z0-9_%-]+)/)
  assert.ok(match, 'the email carried no action link')
  return decodeURIComponent(match[1])
}

export async function provisionAccount(stack, { binaries, legal, report }) {
  const { check } = report
  const context = { accountEmail, accountPassword, adminEmail, adminPassword, vaultId }

  await check('the API answers liveness and readiness', async () => {
    const live = expectStatus(await request(stack.instance, 'GET', '/livez'), 200, '/livez')
    assert.equal(live.json.status, 'ok')
    const ready = expectStatus(await request(stack.instance, 'GET', '/readyz'), 200, '/readyz')
    assert.equal(ready.json.status, 'ok')
  })

  await check('registration is invite only and mail delivery is available', async () => {
    const response = expectStatus(await request(stack.instance, 'GET', '/v1/auth/registration'), 200, 'registration status')
    assert.equal(response.json.mode, 'invite')
    assert.equal(response.json.requiresInvite, true)
    assert.equal(response.json.emailDeliveryAvailable, true)
  })

  const admin = adminBrowser(stack)
  const setupToken = await check('the operator command prepares the first administrator', async () => {
    const link = linkFrom(stack.adminctl(binaries, ['bootstrap', adminEmail]), 'bootstrap')
    assert.equal(link.origin, stack.origins.admin)
    assert.equal(link.pathname, '/setup')
    return link.searchParams.get('token')
  })

  const inviteCode = await check('the operator command issues an invitation for the account address', async () => {
    const link = linkFrom(stack.adminctl(binaries, ['invite', accountEmail]), 'invite')
    assert.equal(link.origin, stack.origins.web)
    return link.searchParams.get('invite')
  })

  const website = websiteBrowser(stack)
  await check('registration needs the current legal acceptance and an invitation that matches the address', async () => {
    await website.protect()
    const body = {
      email: accountEmail, password: accountPassword, inviteCode,
      termsAccepted: true, termsVersion: legal.terms, privacyAcknowledged: true, privacyVersion: legal.privacy,
    }
    expectError(await website.call('POST', '/v1/auth/register', { json: { ...body, termsAccepted: false } }), 400, 'legal_documents_not_accepted', 'registration without acceptance')
    expectError(await website.call('POST', '/v1/auth/register', { json: { ...body, email: 'someone.else@example.com' } }), 403, 'registration_not_eligible', 'registration with another address')
    expectStatus(await website.call('POST', '/v1/auth/register', { json: body }), 202, 'registration')
  })

  const codes = await check('the administrator finishes setup with a password and a one-time code', async () => {
    await admin.protect()
    const details = expectStatus(await admin.call('POST', '/v1/admin/auth/setup/begin', { json: { token: setupToken } }), 200, 'admin setup details')
    assert.equal(details.json.email, adminEmail)
    const authenticatorForAdmin = authenticator(details.json.secret)
    const finished = expectStatus(await admin.call('POST', '/v1/admin/auth/setup/complete', { json: { token: setupToken, password: adminPassword, code: await authenticatorForAdmin.next() } }), 200, 'admin setup')
    assert.equal(finished.json.admin.email, adminEmail)
    assert.ok(admin.cookieNames().includes('sesame_admin_session'), 'setup set no admin session cookie')
    return authenticatorForAdmin
  })
  context.adminCodes = codes

  await check('the verification email arrives through the SMTP relay and verifies the address', async () => {
    const message = await stack.mailbox.waitFor(accountEmail, 'Verify your Sesame account email', 90)
    const confirmed = expectStatus(await website.call('POST', '/v1/auth/email/verification/confirm', { json: { token: actionToken(message), password: accountPassword } }), 200, 'verification')
    assert.equal(confirmed.json.user.emailVerified, true)
    assert.equal(confirmed.json.user.betaAccess, true)
    assert.equal(confirmed.json.credentialSetupRequired, false)
  })

  context.website = await check('the account signs in with its password and a wrong password is refused', async () => {
    const wrong = websiteBrowser(stack)
    await wrong.protect()
    expectError(await wrong.call('POST', '/v1/auth/login', { json: { email: accountEmail, password: `${accountPassword}x` } }), 401, 'invalid_credentials', 'sign-in with a wrong password')
    const session = websiteBrowser(stack)
    await session.protect()
    const response = expectStatus(await session.call('POST', '/v1/auth/login', { json: { email: accountEmail, password: accountPassword } }), 200, 'sign-in')
    assert.equal(response.json.user.email, accountEmail)
    const me = expectStatus(await session.call('GET', '/v1/auth/me'), 200, 'session')
    assert.equal(me.json.user.email, accountEmail)
    return session
  })

  context.devices = await check('two desktops link to the account with one-time codes', async () => {
    const linked = []
    for (const deviceName of ['Hosted laptop A', 'Hosted laptop B']) {
      const created = expectStatus(await context.website.call('POST', '/v1/account/desktop-link'), 201, 'desktop link code')
      const redeemed = expectStatus(await request(stack.instance, 'POST', '/v1/desktop/link', { json: { code: created.json.code, deviceName } }), 201, 'desktop link')
      expectError(await request(stack.instance, 'POST', '/v1/desktop/link', { json: { code: created.json.code, deviceName } }), 401, 'invalid_desktop_link', 'desktop link code reuse')
      linked.push({ token: redeemed.json.accessToken, deviceId: redeemed.json.device.deviceId })
    }
    const listed = expectStatus(await context.website.call('GET', '/v1/account/devices'), 200, 'account devices')
    assert.equal(listed.json.devices.length, 2)
    return linked
  })

  return context
}

export async function turnSyncOn(stack, context, { report }) {
  const { check } = report
  const [desktopA] = context.devices
  const probe = syncDevice('syncHostedProbe0001', desktopA.token)

  await check('Sync answers sync_unavailable while the flag is off', async () => {
    expectError(await request(stack.instance, 'GET', '/v1/sync/state', tokenHeaders(probe)), 403, 'sync_unavailable', 'sync state before the flag')
  })

  await check('the administrator turns Sync on after a fresh password confirmation', async () => {
    const admin = adminBrowser(stack)
    await admin.protect()
    expectStatus(await admin.call('POST', '/v1/admin/auth/login', { json: { email: context.adminEmail, password: context.adminPassword, code: await context.adminCodes.next() } }), 200, 'admin sign-in')
    expectStatus(await admin.call('POST', '/v1/admin/auth/step-up', { json: { password: context.adminPassword } }), 200, 'admin step-up')
    expectStatus(await admin.call('PATCH', '/v1/admin/flags/cloud_sync_available', { json: { value: 'true' } }), 204, 'enable Sync')
    const flags = expectStatus(await admin.call('GET', '/v1/admin/flags'), 200, 'flags')
    assert.equal(flags.json.flags.find((flag) => flag.key === 'cloud_sync_available')?.value, 'true')
  })

  await check('a device with a token but no vault gets sync_vault_not_found, not a refusal', async () => {
    expectError(await request(stack.instance, 'GET', '/v1/sync/state', tokenHeaders(probe)), 404, 'sync_vault_not_found', 'sync state with Sync on')
  })
}

export async function pushAndPull(stack, context, { report }) {
  const { check } = report
  const capabilityKey = publicKeyFromSeed(Buffer.from(stack.secrets.capabilitySeed, 'base64url'))
  const [desktopA, desktopB] = context.devices
  const deviceA = syncDevice('syncHostedAlpha0001', desktopA.token)
  const deviceB = syncDevice('syncHostedBravo0002', desktopB.token)
  const ciphertextOne = randomBytes(256)
  const ciphertextTwo = randomBytes(256)
  const packageKeyB = randomBase64url(32)
  const { vaultId } = context
  const entryA = signDeviceListEntry(deviceA, {
    vaultId, index: 1, previousDigest: '', kind: 'create', signerDeviceId: deviceA.id, subjectDeviceId: deviceA.id,
    subjectSigningKey: deviceA.signingKey, subjectPackageKey: randomBase64url(32), sealedLabel: randomBase64url(48), epochAfter: 1, snapshotFloor: 0,
  })
  const entryB = signDeviceListEntry(deviceA, {
    vaultId, index: 2, previousDigest: deviceListDigest(entryA), kind: 'add', signerDeviceId: deviceA.id, subjectDeviceId: deviceB.id,
    subjectSigningKey: deviceB.signingKey, subjectPackageKey: packageKeyB, sealedLabel: randomBase64url(48), epochAfter: 1, snapshotFloor: 1,
  })
  const headerOne = signHeader(deviceA, headerFor({ vaultId, signer: deviceA, revision: 1, previousDigest: '', deviceListIndex: 1, deviceListDigest: deviceListDigest(entryA), ciphertext: ciphertextOne }))
  const revisionOneDigest = headerDigest(headerOne)

  const receiptIsSigned = (upload, revision, digest) => {
    const key = createPublicKey({ key: { kty: 'OKP', crv: 'Ed25519', x: capabilityKey }, format: 'jwk' })
    const payload = receiptSigningPayload({ vaultId, revision, headerDigest: digest, vaultEpoch: 1, acceptedAt: Math.floor(Date.parse(upload.acceptedAt) / 1000) })
    return verify(null, payload, key, Buffer.from(upload.receipt, 'base64url'))
  }

  await check('device A creates the vault and pushes revision 1 with a receipt the service key signed', async () => {
    const created = expectStatus(await request(stack.instance, 'POST', '/v1/sync/vault', signedCall(deviceA, 'POST', '/v1/sync/vault', { entry: entryA })), 201, 'create vault')
    assert.deepEqual(created.json, { vaultId, vaultEpoch: 1, headIndex: 1 })
    const body = { header: headerOne, ciphertext: ciphertextOne.toString('base64url') }
    const upload = expectStatus(await request(stack.instance, 'POST', '/v1/sync/snapshots', signedCall(deviceA, 'POST', '/v1/sync/snapshots', body)), 201, 'upload revision 1')
    assert.equal(upload.json.revision, 1)
    assert.equal(upload.json.digest, revisionOneDigest)
    assert.equal(receiptIsSigned(upload.json, 1, revisionOneDigest), true, 'the receipt does not verify against the capability key')
  })

  let approvedPackage
  await check('device B pairs and device A approves it with a signed add entry and key package', async () => {
    const offer = { deviceId: deviceB.id, signingKey: deviceB.signingKey, packageKey: packageKeyB, message: randomBase64url(32) }
    expectStatus(await request(stack.instance, 'POST', '/v1/sync/pairings', signedCall(deviceB, 'POST', '/v1/sync/pairings', offer)), 201, 'begin pairing')
    const answer = randomBase64url(32)
    expectStatus(await request(stack.instance, 'POST', `/v1/sync/pairings/${deviceB.id}/answer`, signedCall(deviceA, 'POST', `/v1/sync/pairings/${deviceB.id}/answer`, { message: answer })), 204, 'answer pairing')
    const status = expectStatus(await request(stack.instance, 'GET', '/v1/sync/pairings/mine', tokenHeaders(deviceB)), 200, 'pairing status')
    assert.equal(status.json.answer, answer)
    expectStatus(await request(stack.instance, 'POST', '/v1/sync/pairings/mine/confirm', signedCall(deviceB, 'POST', '/v1/sync/pairings/mine/confirm', { message: randomBase64url(32) })), 204, 'confirm pairing')
    approvedPackage = signKeyPackage(deviceA, { vaultId, senderDeviceId: deviceA.id, recipientDeviceId: deviceB.id, vaultEpoch: 1, entryDigest: deviceListDigest(entryB), ciphertext: randomBase64url(64) })
    expectStatus(await request(stack.instance, 'POST', '/v1/sync/devices', signedCall(deviceA, 'POST', '/v1/sync/devices', { entry: entryB, package: approvedPackage })), 201, 'approve device')
    const served = expectStatus(await request(stack.instance, 'GET', '/v1/sync/key-package', tokenHeaders(deviceB)), 200, 'key package').json.package
    const digest = keyPackageDigest(served)
    assert.equal(digest, keyPackageDigest(approvedPackage), 'the served key package is not the one device A approved')
    expectStatus(await request(stack.instance, 'POST', '/v1/sync/activate', signedCall(deviceB, 'POST', '/v1/sync/activate', { packageDigest: digest })), 204, 'activate device')
  })

  await check('device B pulls the vault device A pushed, byte for byte', async () => {
    const state = expectStatus(await request(stack.instance, 'GET', '/v1/sync/state', tokenHeaders(deviceB)), 200, 'device B state')
    assert.equal(state.json.deviceState, 'approved')
    assert.equal(state.json.currentRevision, 1)
    const headers = expectStatus(await request(stack.instance, 'GET', '/v1/sync/headers?after=0', tokenHeaders(deviceB)), 200, 'headers')
    assert.equal(headers.json.headers.length, 1)
    assert.equal(headerDigest(headers.json.headers[0]), revisionOneDigest, 'the served header does not hash to the digest device A uploaded')
    const snapshot = expectStatus(await request(stack.instance, 'GET', '/v1/sync/snapshot', tokenHeaders(deviceB)), 200, 'snapshot')
    assert.equal(snapshot.json.digest, revisionOneDigest)
    assert.equal(snapshot.json.ciphertext, ciphertextOne.toString('base64url'))
  })

  let uploadTwo
  await check('device B pushes revision 2 and device A pulls it', async () => {
    const headerTwo = signHeader(deviceB, headerFor({ vaultId, signer: deviceB, revision: 2, previousDigest: revisionOneDigest, deviceListIndex: 2, deviceListDigest: deviceListDigest(entryB), ciphertext: ciphertextTwo }))
    uploadTwo = signedCall(deviceB, 'POST', '/v1/sync/snapshots', { header: headerTwo, ciphertext: ciphertextTwo.toString('base64url') })
    const upload = expectStatus(await request(stack.instance, 'POST', '/v1/sync/snapshots', uploadTwo), 201, 'upload revision 2')
    assert.equal(upload.json.digest, headerDigest(headerTwo))
    const snapshot = expectStatus(await request(stack.instance, 'GET', '/v1/sync/snapshot', tokenHeaders(deviceA)), 200, 'device A snapshot')
    assert.equal(snapshot.json.digest, headerDigest(headerTwo))
    assert.equal(snapshot.json.ciphertext, ciphertextTwo.toString('base64url'))
    context.sync = { deviceA, deviceB, entryB, ciphertextTwo, revisionTwoDigest: headerDigest(headerTwo), revisionOneDigest }
  })

  await check('a replayed upload and a forged signature are both refused', async () => {
    expectError(await request(stack.instance, 'POST', '/v1/sync/snapshots', uploadTwo), 409, 'sync_replayed', 'replayed upload')
    const forged = signedCall({ ...deviceA, seed: deviceB.seed }, 'POST', '/v1/sync/reset', { vaultEpoch: 1 })
    expectError(await request(stack.instance, 'POST', '/v1/sync/reset', forged), 401, 'sync_signature_invalid', 'reset with a forged signature')
    const unsigned = { headers: { authorization: `Sesame ${deviceA.token}` }, json: { vaultEpoch: 1 } }
    expectError(await request(stack.instance, 'POST', '/v1/sync/reset', unsigned), 401, 'sync_signature_invalid', 'reset without a signature')
    const stranger = { ...tokenHeaders({ token: randomBase64url(32) }) }
    assert.equal((await request(stack.instance, 'GET', '/v1/sync/state', stranger)).status, 401, 'an unknown device token was accepted')
    const state = expectStatus(await request(stack.instance, 'GET', '/v1/sync/state', tokenHeaders(deviceA)), 200, 'state after the refusals')
    assert.equal(state.json.currentRevision, 2, 'a refused request changed the vault')
  })
}

export async function accountSeesVault(context, { report }) {
  await report.check('the account page counts the vault and its two devices', async () => {
    const vault = expectStatus(await context.website.call('GET', '/v1/account/sync-vault'), 200, 'account sync vault')
    assert.equal(vault.json.available, true)
    assert.equal(vault.json.hasVault, true)
    assert.equal(vault.json.approvedDevices, 2)
  })
}

export async function readBack(stack, context, { report, label }) {
  const { check } = report
  const { deviceA, deviceB, ciphertextTwo, revisionOneDigest, revisionTwoDigest, entryB } = context.sync
  const { vaultId } = context

  await check(`${label}: the administrator still signs in`, async () => {
    const admin = adminBrowser(stack)
    await admin.protect()
    const response = expectStatus(await admin.call('POST', '/v1/admin/auth/login', { json: { email: context.adminEmail, password: context.adminPassword, code: await context.adminCodes.next() } }), 200, 'admin sign-in')
    assert.equal(response.json.admin.email, context.adminEmail)
    const flags = expectStatus(await admin.call('GET', '/v1/admin/flags'), 200, 'flags')
    assert.equal(flags.json.flags.find((flag) => flag.key === 'cloud_sync_available')?.value, 'true')
  })

  await check(`${label}: the account still signs in with its password and its devices are still linked`, async () => {
    const session = websiteBrowser(stack)
    await session.protect()
    const response = expectStatus(await session.call('POST', '/v1/auth/login', { json: { email: context.accountEmail, password: context.accountPassword } }), 200, 'sign-in')
    assert.equal(response.json.user.emailVerified, true)
    const devices = expectStatus(await session.call('GET', '/v1/account/devices'), 200, 'devices')
    assert.equal(devices.json.devices.length, 2)
    const vault = expectStatus(await session.call('GET', '/v1/account/sync-vault'), 200, 'account sync vault')
    assert.equal(vault.json.hasVault, true)
    assert.equal(vault.json.approvedDevices, 2)
    context.website = session
  })

  await check(`${label}: both devices still read the synced vault and its signed history`, async () => {
    for (const device of [deviceA, deviceB]) {
      const state = expectStatus(await request(stack.instance, 'GET', '/v1/sync/state', tokenHeaders(device)), 200, `state for ${device.id}`)
      assert.equal(state.json.vaultId, vaultId)
      assert.equal(state.json.currentRevision, 2)
      assert.equal(state.json.deviceState, 'approved')
      const headers = expectStatus(await request(stack.instance, 'GET', '/v1/sync/headers?after=0', tokenHeaders(device)), 200, `headers for ${device.id}`)
      assert.deepEqual(headers.json.headers.map((header) => headerDigest(header)), [revisionOneDigest, revisionTwoDigest])
      const snapshot = expectStatus(await request(stack.instance, 'GET', '/v1/sync/snapshot', tokenHeaders(device)), 200, `snapshot for ${device.id}`)
      assert.equal(snapshot.json.digest, revisionTwoDigest)
      assert.equal(snapshot.json.ciphertext, ciphertextTwo.toString('base64url'))
      const list = expectStatus(await request(stack.instance, 'GET', '/v1/sync/device-list?after=0', tokenHeaders(device)), 200, `device list for ${device.id}`)
      assert.equal(list.json.entries.length, 2)
      assert.equal(deviceListDigest(list.json.entries[1]), deviceListDigest(entryB))
    }
  })
}

export async function pushNextRevision(stack, context, { report, label }) {
  const { check } = report
  const { deviceA, deviceB, entryB, revisionTwoDigest } = context.sync
  await check(`${label}: device B pushes revision 3 on top of the earlier history and device A pulls it`, async () => {
    const ciphertext = randomBytes(256)
    const header = signHeader(deviceB, headerFor({ vaultId: context.vaultId, signer: deviceB, revision: 3, previousDigest: revisionTwoDigest, deviceListIndex: 2, deviceListDigest: deviceListDigest(entryB), ciphertext }))
    const upload = expectStatus(await request(stack.instance, 'POST', '/v1/sync/snapshots', signedCall(deviceB, 'POST', '/v1/sync/snapshots', { header, ciphertext: ciphertext.toString('base64url') })), 201, 'upload revision 3')
    assert.equal(upload.json.revision, 3)
    const snapshot = expectStatus(await request(stack.instance, 'GET', '/v1/sync/snapshot', tokenHeaders(deviceA)), 200, 'device A snapshot')
    assert.equal(snapshot.json.digest, headerDigest(header))
    assert.equal(snapshot.json.ciphertext, ciphertext.toString('base64url'))
  })
}
