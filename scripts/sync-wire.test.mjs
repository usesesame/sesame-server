import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { describe, it } from 'node:test'
import { fileURLToPath } from 'node:url'
import {
  deviceListDigest,
  deviceListSigningPayload,
  digestOf,
  headerDigest,
  headerSigningPayload,
  keyPackageDigest,
  keyPackageSigningPayload,
  labels,
  number,
  publicKeyFromSeed,
  receiptSigningPayload,
  requestSigningPayload,
  signEd25519,
  snapshotAad,
  verifyEd25519,
} from './sync-wire.mjs'

const fixture = JSON.parse(readFileSync(fileURLToPath(new URL('../internal/syncproto/testdata/v3-fixture.json', import.meta.url)), 'utf8'))
const seedFor = (deviceId) => Buffer.from(fixture.seedHexByDevice[deviceId], 'hex')

describe('sync v3 canonical encoding', () => {
  it('encodes every device list entry to the fixture bytes and digest', () => {
    const entries = [...fixture.chain, ...fixture.removalChain]
    assert.equal(entries.length, 7)
    for (const vector of entries) {
      assert.equal(deviceListSigningPayload(vector.entry).toString('hex'), vector.signingPayloadHex, `entry ${vector.entry.index} payload`)
      assert.equal(deviceListDigest(vector.entry), vector.digest, `entry ${vector.entry.index} digest`)
    }
  })

  it('signs device list entries with the fixture seeds to the fixture signatures', () => {
    for (const { entry } of [...fixture.chain, ...fixture.removalChain]) {
      const payload = deviceListSigningPayload(entry)
      assert.equal(signEd25519(seedFor(entry.signerDeviceId), payload), entry.signature, `entry ${entry.index} signature`)
    }
  })

  it('encodes every header, its AAD and its digest to the fixture bytes', () => {
    assert.equal(fixture.headers.length, 3)
    for (const vector of fixture.headers) {
      assert.equal(headerSigningPayload(vector.header).toString('hex'), vector.signingPayloadHex, `revision ${vector.header.revision} payload`)
      assert.equal(snapshotAad(vector.header).toString('hex'), vector.aadHex, `revision ${vector.header.revision} AAD`)
      assert.equal(headerDigest(vector.header), vector.digest, `revision ${vector.header.revision} digest`)
    }
  })

  it('signs headers with the fixture seeds to the fixture signatures', () => {
    for (const { header } of fixture.headers) {
      assert.equal(signEd25519(seedFor(header.deviceId), headerSigningPayload(header)), header.signature, `revision ${header.revision} signature`)
    }
  })

  it('encodes the request signing payload and checks its signature', () => {
    const { request, receipt, keyPackage } = fixture
    const payload = requestSigningPayload({
      method: request.method, target: request.target, body: Buffer.from(request.body, 'utf8'),
      token: request.token, timestamp: request.timestamp, nonce: request.nonce,
    })
    assert.equal(payload.toString('hex'), request.signingPayloadHex)
    assert.equal(publicKeyFromSeed(seedFor('deviceLaptop00000001')), request.signerKey)
    assert.equal(signEd25519(seedFor('deviceLaptop00000001'), payload), request.signature)
    assert.equal(verifyEd25519(request.signerKey, payload, request.signature), true)
    assert.equal(keyPackage.signerKey, request.signerKey)
  })

  it('encodes the receipt signing payload and checks its signature', () => {
    const { receipt } = fixture
    const payload = receiptSigningPayload(receipt)
    assert.equal(payload.toString('hex'), receipt.signingPayloadHex)
    assert.equal(signEd25519(seedFor('deviceLaptop00000001'), payload), receipt.signature)
    assert.equal(verifyEd25519(receipt.signerKey, payload, receipt.signature), true)
  })

  it('encodes the key package payload, its digest and its signature', () => {
    const { keyPackage } = fixture
    assert.equal(keyPackageSigningPayload(keyPackage).toString('hex'), keyPackage.signingPayloadHex)
    assert.equal(keyPackageDigest(keyPackage), keyPackage.digest)
    assert.equal(signEd25519(seedFor(keyPackage.senderDeviceId), keyPackageSigningPayload(keyPackage)), keyPackage.signature)
  })

  it('derives the fixture public keys from the fixture seeds', () => {
    assert.equal(publicKeyFromSeed(seedFor('deviceLaptop00000001')), fixture.chain[0].entry.subjectSigningKey)
    assert.equal(publicKeyFromSeed(seedFor('deviceDesktop0000001')), fixture.chain[1].entry.subjectSigningKey)
    assert.equal(publicKeyFromSeed(seedFor('devicePhone000000001')), fixture.chain[2].entry.subjectSigningKey)
  })

  it('keeps labels apart so a signature over one object never verifies under another', () => {
    const header = fixture.headers[0].header
    const signature = signEd25519(seedFor(header.deviceId), headerSigningPayload(header))
    const key = publicKeyFromSeed(seedFor(header.deviceId))
    assert.equal(verifyEd25519(key, headerSigningPayload(header), signature), true)
    assert.equal(verifyEd25519(key, deviceListSigningPayload(fixture.chain[0].entry), signature), false)
    assert.notEqual(labels.header, labels.headerDigest)
  })

  it('refuses numbers outside the unsigned 64 bit range', () => {
    assert.equal(number(0).toString('ascii'), '0')
    assert.equal(number(18446744073709551615n).toString('ascii'), '18446744073709551615')
    assert.throws(() => number(-1), RangeError)
    assert.throws(() => number(18446744073709551616n), RangeError)
  })

  it('writes digests as unpadded base64url of SHA-256', () => {
    assert.equal(digestOf(Buffer.alloc(0)), '47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU')
  })
})
