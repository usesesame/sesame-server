import { randomBytes } from 'node:crypto'
import {
  deviceListSigningPayload,
  digestOf,
  headerSigningPayload,
  keyPackageSigningPayload,
  publicKeyFromSeed,
  requestSigningPayload,
  signEd25519,
} from './sync-wire.mjs'

export function randomBase64url(length) {
  return randomBytes(length).toString('base64url')
}

export function syncDevice(id, token) {
  const seed = randomBytes(32)
  return { id, token, seed, signingKey: publicKeyFromSeed(seed) }
}

export function tokenHeaders(device) {
  return { headers: { authorization: `Sesame ${device.token}` } }
}

export function signedCall(device, method, path, json) {
  const body = Buffer.from(JSON.stringify(json), 'utf8')
  const timestamp = Math.floor(Date.now() / 1000)
  const nonce = randomBase64url(16)
  const signature = signEd25519(device.seed, requestSigningPayload({ method, target: path, body, token: device.token, timestamp, nonce }))
  return { headers: { authorization: `Sesame ${device.token}`, 'sesame-request-signature': `${timestamp}.${nonce}.${signature}` }, json }
}

export function signDeviceListEntry(signer, entry) {
  return { ...entry, signature: signEd25519(signer.seed, deviceListSigningPayload(entry)) }
}

export function signHeader(signer, header) {
  return { ...header, signature: signEd25519(signer.seed, headerSigningPayload(header)) }
}

export function signKeyPackage(signer, pkg) {
  return { ...pkg, signature: signEd25519(signer.seed, keyPackageSigningPayload(pkg)) }
}

export function headerFor({ vaultId, signer, revision, previousDigest, deviceListIndex, deviceListDigest, ciphertext }) {
  return {
    version: 3, vaultId, deviceId: signer.id, revision, vaultEpoch: 1, deviceListIndex, deviceListDigest, previousDigest,
    ciphertextDigest: digestOf(ciphertext), ciphertextLength: ciphertext.length, nonce: randomBase64url(24),
  }
}
