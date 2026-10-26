import { afterEach, beforeEach, expect, test, vi } from 'vitest'

const api = vi.hoisted(() => ({ apiRequest: vi.fn(), responseError: vi.fn() }))
vi.mock('../src/lib/api', () => api)
vi.mock('../src/lib/runtime-config', () => ({ siteOrigin: 'https://website.test.invalid' }))

import { conditionalPasskeysAvailable, signInWithPasskey } from '../src/lib/passkey'

const assertion = {
  id: 'credential-id',
  rawId: new Uint8Array([1, 2, 3]).buffer,
  type: 'public-key',
  response: { authenticatorData: new Uint8Array([4]).buffer, clientDataJSON: new Uint8Array([5]).buffer, signature: new Uint8Array([6]).buffer, userHandle: null },
  getClientExtensionResults: () => ({}),
}

function stubBrowser(conditional?: () => Promise<boolean>) {
  const get = vi.fn().mockResolvedValue(assertion)
  class FakeCredential {}
  Object.assign(FakeCredential, conditional ? { isConditionalMediationAvailable: conditional } : {})
  vi.stubGlobal('PublicKeyCredential', FakeCredential)
  vi.stubGlobal('navigator', { credentials: { create: vi.fn(), get } })
  return get
}

beforeEach(() => {
  api.apiRequest.mockReset()
  api.apiRequest
    .mockResolvedValueOnce({ ok: true, json: async () => ({ publicKey: { challenge: 'AQID' } }) })
    .mockResolvedValueOnce({ ok: true, json: async () => ({ user: { id: 'account-test' } }) })
})

afterEach(() => vi.unstubAllGlobals())

test('conditional autofill is reported only where the browser says it is available', async () => {
  stubBrowser()
  expect(await conditionalPasskeysAvailable()).toBe(false)
  stubBrowser(async () => true)
  expect(await conditionalPasskeysAvailable()).toBe(true)
  stubBrowser(async () => false)
  expect(await conditionalPasskeysAvailable()).toBe(false)
  stubBrowser(async () => { throw new Error('blocked') })
  expect(await conditionalPasskeysAvailable()).toBe(false)
})

test('a quiet request asks for conditional mediation and can be cancelled', async () => {
  const get = stubBrowser()
  const controller = new AbortController()
  await signInWithPasskey({ conditional: true, signal: controller.signal })
  expect(get.mock.calls[0][0]).toMatchObject({ mediation: 'conditional', signal: controller.signal })
})

test('an open request asks the ordinary way', async () => {
  const get = stubBrowser()
  await signInWithPasskey()
  expect(get.mock.calls[0][0]).not.toHaveProperty('mediation')
  expect(get.mock.calls[0][0]).not.toHaveProperty('signal')
})
