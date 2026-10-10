import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import net from 'node:net'

const options = parseArguments(process.argv.slice(2))
const container = `sesame-selfhost-check-${process.pid}`

try {
  inspect()
  if (options.run) await runContainer()
  console.log(`Self-host image check passed for ${options.image}${options.run ? ' including a running container' : ''}.`)
} catch (error) {
  console.error(`Self-host image check failed: ${error.stack ?? error}`)
  process.exitCode = 1
}

function parseArguments(args) {
  const parsed = { image: '', arch: '', version: '', commit: '', run: false }
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index]
    if (arg === '--run') parsed.run = true
    else if (['--image', '--arch', '--version', '--commit'].includes(arg)) parsed[arg.slice(2)] = args[++index] ?? ''
    else throw new Error(`Unknown argument ${arg}. Use --image <ref> --arch <amd64|arm64> [--version <v>] [--commit <sha>] [--run].`)
  }
  if (!parsed.image || !['amd64', 'arm64'].includes(parsed.arch)) throw new Error('--image and --arch amd64 or arm64 are required.')
  return parsed
}

function docker(args, { allowFailure = false } = {}) {
  const result = spawnSync('docker', args, { encoding: 'utf8' })
  if (result.error) throw result.error
  if (result.status !== 0 && !allowFailure) throw new Error(`docker ${args.join(' ')} exited ${result.status}\n${result.stdout}${result.stderr}`)
  return result
}

function inspect() {
  const [details] = JSON.parse(docker(['image', 'inspect', options.image]).stdout)
  assert.equal(details.Architecture, options.arch, 'the image architecture differs from the one requested')
  assert.equal(details.Os, 'linux')
  assert.equal(details.Config.User, 'nonroot:nonroot', 'the image does not run as the nonroot user')
  assert.deepEqual(details.Config.Entrypoint, ['/sesame-server'])
  assert.ok('8787/tcp' in (details.Config.ExposedPorts ?? {}), 'the image does not expose 8787')
  assert.ok('/data' in (details.Config.Volumes ?? {}), 'the image declares no /data volume')
  assert.deepEqual(details.Config.Healthcheck?.Test, ['CMD', '/sesame-server', 'healthcheck'], 'the image has no health check')
  assert.ok(details.Config.Env.includes('SESAME_ADDR=0.0.0.0:8787'), 'the image does not listen on all interfaces inside the container')
  assert.ok(details.Config.Env.includes('SESAME_DATA_DIR=/data'))
  if (options.version) assert.equal(details.Config.Labels['org.opencontainers.image.version'], options.version)
  if (options.commit) assert.equal(details.Config.Labels['org.opencontainers.image.revision'], options.commit)
}

function freePort() {
  return new Promise((resolvePort, reject) => {
    const probe = net.createServer()
    probe.once('error', reject)
    probe.listen(0, '127.0.0.1', () => {
      const { port } = probe.address()
      probe.close(() => resolvePort(port))
    })
  })
}

async function runContainer() {
  const port = await freePort()
  try {
    docker([
      'run', '--detach', '--name', container,
      '--health-interval', '2s', '--health-start-period', '1s',
      '--publish', `127.0.0.1:${port}:8787`,
      '--env', `SESAME_PUBLIC_URL=http://localhost:${port}`,
      options.image,
    ])
    await waitForHealthy()
    const base = `http://127.0.0.1:${port}`
    const live = await (await fetchOk(`${base}/livez`)).json()
    assert.equal(live.service, 'sesame-server')
    if (options.version) assert.equal(live.version, options.version)
    if (options.commit) assert.equal(live.commit, options.commit)
    assert.equal((await (await fetchOk(`${base}/config.json`)).json()).setupRequired, true)
    const index = await fetchOk(`${base}/`)
    assert.match(index.headers.get('content-security-policy') ?? '', /^default-src 'none'; script-src 'self'/)
    assert.match(await index.text(), /<script type="module"[^>]+src="\.\/assets\//, 'the image serves a placeholder instead of the built console')
    const backup = '/data/backups/image-check.tar'
    docker(['exec', container, '/sesame-server', 'backup', backup])
    assert.match(docker(['exec', container, '/sesame-server', 'check', backup]).stdout, /is intact/)
  } catch (error) {
    const logs = docker(['logs', container], { allowFailure: true })
    console.error(`--- container log ---\n${logs.stdout}${logs.stderr}`)
    throw error
  } finally {
    docker(['rm', '--force', container], { allowFailure: true })
  }
}

async function waitForHealthy() {
  const deadline = Date.now() + 90000
  while (Date.now() < deadline) {
    const status = docker(['inspect', '--format', '{{.State.Health.Status}}', container]).stdout.trim()
    if (status === 'healthy') return
    assert.notEqual(status, 'unhealthy', 'the container reported unhealthy')
    await new Promise((resolveWait) => setTimeout(resolveWait, 1000))
  }
  throw new Error('the container did not become healthy within 90 seconds')
}

async function fetchOk(url) {
  const response = await fetch(url, { signal: AbortSignal.timeout(5000) })
  assert.equal(response.status, 200, `${url} returned ${response.status}`)
  return response
}
