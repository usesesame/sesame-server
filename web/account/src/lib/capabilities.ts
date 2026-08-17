import { apiRequest } from './api'

export type CapabilityConfig = {
  schemaVersion: 1
  minimumDesktopVersion: string
  latestDesktopVersion: string
  features: Record<string, boolean>
  serviceStatus: Record<string, boolean>
  expiresAt: string
}

const closed: CapabilityConfig = { schemaVersion: 1, minimumDesktopVersion: '', latestDesktopVersion: '', features: {}, serviceStatus: {}, expiresAt: '' }
let cached: CapabilityConfig | null = null
let etag = ''

function bytes(value: string) {
  const padded = value.replace(/-/g, '+').replace(/_/g, '/') + '==='.slice((value.length + 3) % 4)
  const text = atob(padded)
  return Uint8Array.from(text, character => character.charCodeAt(0))
}

export async function capabilities(): Promise<CapabilityConfig> {
  const publicKey = import.meta.env.VITE_SESAME_CAPABILITY_PUBLIC_KEY?.trim()
  if (!publicKey || !globalThis.crypto?.subtle) return closed
  try {
    const response = await apiRequest('/v1/capabilities', { headers: etag ? { 'If-None-Match': etag } : {} })
    if (response.status === 304 && cached) return cached
    if (!response.ok) return closed
    const envelope = await response.json() as { payload?: string; signature?: string }
    if (!envelope.payload || !envelope.signature) return closed
    const payload = bytes(envelope.payload)
    const key = await crypto.subtle.importKey('raw', bytes(publicKey), { name: 'Ed25519' }, false, ['verify'])
    if (!await crypto.subtle.verify('Ed25519', key, bytes(envelope.signature), payload)) return closed
    const value = JSON.parse(new TextDecoder().decode(payload)) as CapabilityConfig
    if (value.schemaVersion !== 1 || !value.expiresAt || Date.parse(value.expiresAt) <= Date.now()) return closed
    etag = response.headers.get('ETag') || ''
    cached = value
    return value
  } catch { return closed }
}

export function capabilityEnabled(config: CapabilityConfig, feature: string) { return config.features[feature] === true }
