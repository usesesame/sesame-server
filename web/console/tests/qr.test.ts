import { createHash } from 'node:crypto'
import { expect, test } from 'vitest'
import { encodeQr, qrPath, qrViewSize } from '../src/lib/qr'

function sample(length: number): string {
  let state = 12345 + length
  let text = ''
  for (let index = 0; index < length; index += 1) {
    state = (Math.imul(state, 1103515245) + 12345) >>> 0
    text += String.fromCharCode(32 + ((state >>> 16) % 90))
  }
  return text
}

function digest(text: string): string {
  const bits = encodeQr(text).map((row) => row.map((cell) => (cell ? '1' : '0')).join('')).join('')
  return createHash('sha256').update(bits).digest('hex')
}

const SETUP_URI = 'otpauth://totp/Sesame:owner?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP&issuer=Sesame'

test.each([
  [1, 'v1', '8f7e91adcc6dc5754a044d34b0ca20d2adfc896584bd10546f205678e666ad9b'],
  [17, 'v2', '51a66aa6699edfdc056be1c0cf877ca451c75a4881bc31fe77ef38deef741d83'],
  [106, 'v6', 'ce2bd8f6fadd675ee72276744c65f8659d4cd41ca3b2e0a8ae71c5bab0f27f6c'],
  [271, 'v12', 'bb6758bb04b782931da131869a4cf89ec996b3ce03117d04ab23da60bc92fcb4'],
  [700, 'v21', '4127ec57d6bf0386187c582a8b277468a65764cac0d1ab25bee1d44ea19cfb6a'],
  [2331, 'v40', '20ae8d5eacbb24d22a3026448d762115a91ce3af7d57d2117007ebefce05f069'],
])('encodes %i bytes to the same modules as the reference encoder (%s)', (length, _version, expected) => {
  expect(digest(sample(length))).toBe(expected)
})

test('encodes an authenticator URI to the same modules as the reference encoder', () => {
  expect(digest(SETUP_URI)).toBe('949fea269d52d958f19e28814285fd5cb75bf7f9ecb84052a2e4093ca112ac45')
})

test('builds a path inside the view box with a four module quiet zone', () => {
  const matrix = encodeQr(SETUP_URI)
  expect(qrViewSize(matrix)).toBe(matrix.length + 8)
  const path = qrPath(matrix)
  expect(path.startsWith('M4 4h7')).toBe(true)
  expect(path).toMatch(/^(M\d+ \d+h\d+v1h-\d+z)+$/)
})

test('refuses text that does not fit in a QR code', () => {
  expect(() => encodeQr('x'.repeat(2400))).toThrow(RangeError)
})
