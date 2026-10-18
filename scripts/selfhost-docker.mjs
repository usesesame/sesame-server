import { spawnSync } from 'node:child_process'
import { sleep } from './selfhost-client.mjs'

export function run(command, args, options = {}) {
  const result = spawnSync(command, args, { encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, ...options })
  if (result.error) throw result.error
  return result
}

export function mustRun(command, args, options = {}) {
  const result = run(command, args, options)
  if (result.status !== 0) {
    throw new Error(`${command} ${args.join(' ')} exited ${result.status}\n${result.stdout}${result.stderr}`)
  }
  return result
}

export function docker(args, options = {}) {
  return mustRun('docker', args, options)
}

export function dockerAllowFailure(args, options = {}) {
  return run('docker', args, options)
}

export function runBlock(script, options = {}) {
  const result = run('bash', ['-euo', 'pipefail', '-c', script], options)
  const output = `${result.stdout}${result.stderr}`
  if (result.status !== 0) throw new Error(`The block exited ${result.status}\n--- block ---\n${script}\n--- output ---\n${output}`)
  return { stdout: result.stdout, stderr: result.stderr, output }
}

export async function waitUntil(label, probe, { seconds = 90, interval = 1000 } = {}) {
  const deadline = Date.now() + seconds * 1000
  let last = 'no attempt finished'
  while (Date.now() < deadline) {
    try {
      const value = await probe()
      if (value) return value
      last = 'the check returned false'
    } catch (error) {
      last = error.message
    }
    await sleep(interval)
  }
  throw new Error(`${label} did not succeed within ${seconds} seconds. Last result: ${last}`)
}

export function containerLogs(name) {
  const logs = dockerAllowFailure(['logs', name])
  return `${logs.stdout}${logs.stderr}`
}
