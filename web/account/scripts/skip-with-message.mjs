import { spawnSync } from 'node:child_process'
import process from 'node:process'

const [name, buildCommand] = process.argv.slice(2)
if (buildCommand) {
  const result = spawnSync(buildCommand, { shell: true, stdio: 'inherit' })
  if (result.status !== 0) process.exit(result.status ?? 1)
}
console.log(`no ${name} tests exist yet, skipping`)
