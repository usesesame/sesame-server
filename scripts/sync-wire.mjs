import { createHash, createPrivateKey, createPublicKey, sign, verify } from 'node:crypto'

export const labels = {
  deviceList: 'sesame-sync-v3-devicelist',
  deviceListDigest: 'sesame-sync-v3-devicelist-digest',
  header: 'sesame-sync-v3-header',
  headerDigest: 'sesame-sync-v3-header-digest',
  request: 'sesame-sync-v3-request',
  receipt: 'sesame-sync-v3-receipt',
  aad: 'sesame-sync-v3-aad',
  keyPackage: 'sesame-sync-v3-keypackage',
}

const pkcs8Ed25519Prefix = Buffer.from('302e020100300506032b657004220420', 'hex')
const maxUint64 = (1n << 64n) - 1n

function lengthPrefixed(bytes) {
  const length = Buffer.alloc(8)
  length.writeBigUInt64BE(BigInt(bytes.length))
  return Buffer.concat([length, bytes])
}

export function text(value) {
  return Buffer.from(value, 'utf8')
}

export function number(value) {
  const parsed = BigInt(value)
  if (parsed < 0n || parsed > maxUint64) throw new RangeError(`${value} is not an unsigned 64 bit number`)
  return Buffer.from(parsed.toString(10), 'ascii')
}

export function encodeCanonical(label, fields) {
  return Buffer.concat([lengthPrefixed(text(label)), ...fields.map(lengthPrefixed)])
}

export function digestOf(bytes) {
  return createHash('sha256').update(bytes).digest('base64url')
}

export function privateKeyFromSeed(seed) {
  if (!Buffer.isBuffer(seed) || seed.length !== 32) throw new RangeError('an Ed25519 seed is 32 bytes')
  return createPrivateKey({ key: Buffer.concat([pkcs8Ed25519Prefix, seed]), format: 'der', type: 'pkcs8' })
}

export function publicKeyFromSeed(seed) {
  return createPublicKey(privateKeyFromSeed(seed)).export({ format: 'jwk' }).x
}

export function signEd25519(seed, payload) {
  return sign(null, payload, privateKeyFromSeed(seed)).toString('base64url')
}

export function verifyEd25519(publicKey, payload, signature) {
  const key = createPublicKey({ key: { kty: 'OKP', crv: 'Ed25519', x: publicKey }, format: 'jwk' })
  return verify(null, payload, key, Buffer.from(signature, 'base64url'))
}

function deviceListFields(entry) {
  return [
    text(entry.vaultId), number(entry.index), text(entry.previousDigest), text(entry.kind),
    text(entry.signerDeviceId), text(entry.subjectDeviceId), text(entry.subjectSigningKey),
    text(entry.subjectPackageKey), text(entry.sealedLabel), number(entry.epochAfter), number(entry.snapshotFloor),
  ]
}

export function deviceListSigningPayload(entry) {
  return encodeCanonical(labels.deviceList, deviceListFields(entry))
}

export function deviceListDigest(entry) {
  return digestOf(encodeCanonical(labels.deviceListDigest, [...deviceListFields(entry), text(entry.signature)]))
}

function headerFields(header) {
  return [
    number(header.version), text(header.vaultId), text(header.deviceId), number(header.revision),
    number(header.vaultEpoch), number(header.deviceListIndex), text(header.deviceListDigest),
    text(header.previousDigest), text(header.ciphertextDigest), number(header.ciphertextLength), text(header.nonce),
  ]
}

export function headerSigningPayload(header) {
  return encodeCanonical(labels.header, headerFields(header))
}

export function headerDigest(header) {
  return digestOf(encodeCanonical(labels.headerDigest, [...headerFields(header), text(header.signature)]))
}

export function snapshotAad(header) {
  return encodeCanonical(labels.aad, headerFields(header).slice(0, 8))
}

export function requestSigningPayload({ method, target, body, token, timestamp, nonce }) {
  return encodeCanonical(labels.request, [
    text(method), text(target), digestBytes(body), digestBytes(text(token)), number(timestamp), text(nonce),
  ])
}

function digestBytes(bytes) {
  return createHash('sha256').update(bytes).digest()
}

export function receiptSigningPayload(receipt) {
  return encodeCanonical(labels.receipt, [
    text(receipt.vaultId), number(receipt.revision), text(receipt.headerDigest),
    number(receipt.vaultEpoch), number(receipt.acceptedAt),
  ])
}

function keyPackageFields(pkg) {
  return [text(pkg.vaultId), text(pkg.senderDeviceId), text(pkg.recipientDeviceId), number(pkg.vaultEpoch), text(pkg.entryDigest), text(pkg.ciphertext)]
}

export function keyPackageSigningPayload(pkg) {
  return encodeCanonical(labels.keyPackage, keyPackageFields(pkg))
}

export function keyPackageDigest(pkg) {
  return digestOf(encodeCanonical(`${labels.keyPackage}-digest`, [...keyPackageFields(pkg), text(pkg.signature)]))
}
