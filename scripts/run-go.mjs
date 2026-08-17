import { mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { spawnSync } from 'node:child_process'

const backendRoot = dirname(dirname(fileURLToPath(import.meta.url)))
const cache = process.env.GOCACHE || join(backendRoot, '.gocache')
mkdirSync(cache, { recursive: true })

const command = process.platform === 'win32' ? 'go.exe' : 'go'
const result = spawnSync(command, process.argv.slice(2), {
  cwd: backendRoot,
  env: { ...process.env, GOCACHE: cache },
  stdio: 'inherit',
})

if (result.error) {
  console.error(`Could not start Go: ${result.error.message}`)
  process.exit(1)
}

process.exit(result.status ?? 1)
