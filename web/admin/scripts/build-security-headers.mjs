import { readFile, writeFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { loadEnv } from 'vite'

const adminRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
// `vite build` (see the admin:build script) runs in the default "production"
// mode and reads its env files from this same root, so this must match or
// the two would silently disagree about `.env.local`.
const fileEnv = loadEnv('production', adminRoot, '')

const configured = (process.env.VITE_SESAME_API_URL ?? fileEnv.VITE_SESAME_API_URL)?.trim()
if (!configured) throw new Error('VITE_SESAME_API_URL is required for an admin build.')
const api = new URL(configured)
const localHttp = api.protocol === 'http:' && ['localhost', '127.0.0.1', '[::1]'].includes(api.hostname)
if ((api.protocol !== 'https:' && !localHttp) || api.username || api.password || api.pathname !== '/' || api.search || api.hash) {
  throw new Error('VITE_SESAME_API_URL must be an HTTPS origin, except for loopback development.')
}

const headersPath = resolve(adminRoot, 'dist', '_headers')
const headers = await readFile(headersPath, 'utf8')
if (!headers.includes('__SESAME_API_ORIGIN__')) throw new Error('Admin CSP placeholder is missing.')
await writeFile(headersPath, headers.replace('__SESAME_API_ORIGIN__', api.origin), 'utf8')
