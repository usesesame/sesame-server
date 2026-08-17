import { apiRequest, responseError } from './api'
import type { Account } from './auth'

// Passkey sign-in for the website account only. It never touches the local
// desktop vault. The desktop app has its own separate unlock methods.

export interface PasskeyInfo {
  id: string
  name: string
  createdAt: string
}

export function passkeysSupported(): boolean {
  return typeof window !== 'undefined'
    && typeof window.PublicKeyCredential === 'function'
    && typeof navigator !== 'undefined'
    && typeof navigator.credentials?.create === 'function'
}

function base64urlToBuffer(value: string): ArrayBuffer {
  const padded = value.replace(/-/g, '+').replace(/_/g, '/').padEnd(Math.ceil(value.length / 4) * 4, '=')
  const binary = atob(padded)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i)
  return bytes.buffer
}

function bufferToBase64url(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer)
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

async function beginCeremony(path: string): Promise<{ publicKey: Record<string, unknown> }> {
  const response = await apiRequest(path, { method: 'POST' })
  if (!response.ok) throw await responseError(response)
  return await response.json() as { publicKey: Record<string, unknown> }
}

type CredentialRef = { id: string; type: 'public-key'; transports?: AuthenticatorTransport[] }

export async function registerPasskey(name: string): Promise<void> {
  const options = await beginCeremony('/v1/account/passkey/register/begin')
  const publicKey = options.publicKey as unknown as Record<string, unknown> & {
    challenge: string
    user: { id: string; name: string; displayName: string }
    excludeCredentials?: CredentialRef[]
  }
  const request = {
    ...publicKey,
    challenge: base64urlToBuffer(publicKey.challenge),
    user: { ...publicKey.user, id: base64urlToBuffer(publicKey.user.id) },
    excludeCredentials: publicKey.excludeCredentials?.map((credential) => ({
      ...credential,
      id: base64urlToBuffer(credential.id),
    })),
  } as unknown as PublicKeyCredentialCreationOptions
  const credential = await navigator.credentials.create({ publicKey: request }) as PublicKeyCredential | null
  if (!credential) throw new Error('Passkey creation was cancelled.')
  const attestation = credential.response as AuthenticatorAttestationResponse
  const finish = await apiRequest(`/v1/account/passkey/register/finish?name=${encodeURIComponent(name || 'Passkey')}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      id: credential.id,
      rawId: bufferToBase64url(credential.rawId),
      type: credential.type,
      response: {
        attestationObject: bufferToBase64url(attestation.attestationObject),
        clientDataJSON: bufferToBase64url(attestation.clientDataJSON),
      },
      clientExtensionResults: credential.getClientExtensionResults(),
    }),
  })
  if (!finish.ok) throw await responseError(finish)
}

export async function signInWithPasskey(): Promise<Account> {
  const options = await beginCeremony('/v1/auth/passkey/login/begin')
  const publicKey = options.publicKey as unknown as Record<string, unknown> & {
    challenge: string
    allowCredentials?: CredentialRef[]
  }
  const request = {
    ...publicKey,
    challenge: base64urlToBuffer(publicKey.challenge),
    allowCredentials: publicKey.allowCredentials?.map((credential) => ({
      ...credential,
      id: base64urlToBuffer(credential.id),
    })),
  } as unknown as PublicKeyCredentialRequestOptions
  const credential = await navigator.credentials.get({ publicKey: request }) as PublicKeyCredential | null
  if (!credential) throw new Error('Passkey sign-in was cancelled.')
  const assertion = credential.response as AuthenticatorAssertionResponse
  const finish = await apiRequest('/v1/auth/passkey/login/finish', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      id: credential.id,
      rawId: bufferToBase64url(credential.rawId),
      type: credential.type,
      response: {
        authenticatorData: bufferToBase64url(assertion.authenticatorData),
        clientDataJSON: bufferToBase64url(assertion.clientDataJSON),
        signature: bufferToBase64url(assertion.signature),
        userHandle: assertion.userHandle ? bufferToBase64url(assertion.userHandle) : undefined,
      },
      clientExtensionResults: credential.getClientExtensionResults(),
    }),
  })
  if (!finish.ok) throw await responseError(finish)
  return (await finish.json() as { user: Account }).user
}

export async function listPasskeys(): Promise<PasskeyInfo[]> {
  const response = await apiRequest('/v1/account/passkeys')
  if (!response.ok) throw await responseError(response)
  return (await response.json() as { passkeys: PasskeyInfo[] }).passkeys
}

export async function deletePasskey(id: string): Promise<void> {
  const response = await apiRequest(`/v1/account/passkeys?id=${encodeURIComponent(id)}`, { method: 'DELETE' })
  if (!response.ok && response.status !== 404) throw await responseError(response)
}
