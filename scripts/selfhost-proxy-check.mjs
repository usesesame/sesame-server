import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import {
  connect,
  createMember,
  desktopStatus,
  expectError,
  expectStatus,
  ownerSystem,
  pairDevice,
  request,
  runFirstSetup,
  setupTokenFrom,
} from './selfhost-client.mjs'
import { containerLogs, docker, dockerAllowFailure, mustRun, waitUntil } from './selfhost-docker.mjs'
import { repositoryRoot, replaceOnce } from './selfhost-doc.mjs'

const imageRepository = 'ghcr.io/usesesame/sesame-server-selfhost'
const localVersion = '0.0.0-ci'
const ownerName = 'proxy-owner'
const ownerPassword = 'correct horse battery staple proxy'
const publicUrl = 'https://localhost'
const composeNetwork = '172.31.88.0/24'
const traefikNetworkName = 'sesame-traefik-check'
const traefikNetwork = '172.31.90.0/24'
const deployDirectory = join(repositoryRoot, 'deploy', 'selfhost')
const options = parseArguments(process.argv.slice(2))
const workDir = mkdtempSync(join(options.tempRoot ?? tmpdir(), 'sesame-proxy-'))
const project = `sesame-proxy-${options.proxy}`
const proxyContainer = `sesame-proxy-${options.proxy}-front`
const proxyImages = readProxyImages()
let passed = 0

try {
  await main()
  console.log(`Proxy check passed for ${options.proxy}: ${passed} checks.`)
} catch (error) {
  console.error(`Proxy check failed for ${options.proxy}: ${error.stack ?? error}`)
  console.error(`\n--- server log ---\n${composeLogs().slice(-3000)}`)
  console.error(`\n--- ${options.proxy} log ---\n${containerLogs(proxyContainer).slice(-3000)}`)
  process.exitCode = 1
} finally {
  cleanUp()
}

function parseArguments(args) {
  const parsed = { image: '', proxy: '', tempRoot: null }
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index]
    if (arg === '--image' || arg === '--proxy') parsed[arg.slice(2)] = args[++index] ?? ''
    else if (arg === '--temp-root') parsed.tempRoot = resolve(args[++index] ?? '')
    else throw new Error(`Unknown argument ${arg}. Use --image <ref> --proxy <caddy|nginx|traefik>.`)
  }
  if (!parsed.image || !['caddy', 'nginx', 'traefik'].includes(parsed.proxy)) throw new Error('--image and --proxy caddy, nginx or traefik are required.')
  return parsed
}

function readProxyImages() {
  const dockerfile = readFileSync(join(repositoryRoot, 'scripts', 'selfhost-proxy-images', 'Dockerfile'), 'utf8')
  const images = Object.fromEntries([...dockerfile.matchAll(/^FROM (\S+) AS (\w+)$/gm)].map((match) => [match[2], match[1]]))
  for (const name of ['caddy', 'nginx', 'traefik']) assert.match(images[name] ?? '', /@sha256:[0-9a-f]{64}$/, `${name} must be pinned by digest in scripts/selfhost-proxy-images/Dockerfile`)
  return images
}

async function check(name, run) {
  try {
    const result = await run()
    passed += 1
    console.log(`ok - ${name}`)
    return result
  } catch (error) {
    error.message = `${name}: ${error.message}`
    throw error
  }
}

function composeFiles() {
  const files = ['-f', join(deployDirectory, 'compose.yaml')]
  if (options.proxy === 'traefik') files.push('-f', join(deployDirectory, 'compose.traefik.yaml'))
  return files
}

function composeEnvironment(trustedProxies) {
  return {
    ...process.env,
    SESAME_IMAGE_TAG: localVersion,
    SESAME_PUBLIC_URL: publicUrl,
    SESAME_TRUSTED_PROXIES: trustedProxies,
    SESAME_HOST: 'localhost',
    TRAEFIK_NETWORK: traefikNetworkName,
  }
}

function compose(args, trustedProxies = '') {
  return mustRun('docker', ['compose', '-p', project, ...composeFiles(), ...args], { cwd: workDir, env: composeEnvironment(trustedProxies) })
}

function composeLogs() {
  const result = dockerAllowFailure(['compose', '-p', project, ...composeFiles(), 'logs', '--no-color'], { cwd: workDir, env: composeEnvironment('') })
  return `${result.stdout}${result.stderr}`
}

function cleanUp() {
  dockerAllowFailure(['rm', '--force', proxyContainer])
  dockerAllowFailure(['compose', '-p', project, ...composeFiles(), 'down', '--volumes', '--remove-orphans'], { cwd: workDir, env: composeEnvironment('') })
  if (options.proxy === 'traefik') dockerAllowFailure(['network', 'rm', traefikNetworkName])
  rmSync(workDir, { recursive: true, force: true })
}

function makeCertificate() {
  const certs = join(workDir, 'certs')
  mkdirSync(certs, { recursive: true })
  mustRun('openssl', ['req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes', '-keyout', join(certs, 'key.pem'), '-out', join(certs, 'cert.pem'), '-days', '2', '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost'])
  mustRun('chmod', ['0644', join(certs, 'key.pem')])
  return certs
}

function startProxy() {
  if (options.proxy === 'caddy') {
    docker(['run', '--detach', '--name', proxyContainer, '--network', 'host', '--env', 'SESAME_HOST=localhost',
      '--volume', `${join(deployDirectory, 'Caddyfile')}:/etc/caddy/Caddyfile:ro`, proxyImages.caddy])
    return
  }
  if (options.proxy === 'nginx') {
    const certs = makeCertificate()
    let config = readFileSync(join(deployDirectory, 'nginx.conf'), 'utf8')
    config = replaceOnce(config, 'ssl_certificate /etc/letsencrypt/live/sesame.example.net/fullchain.pem;', 'ssl_certificate /certs/cert.pem;', 'the certificate path')
    config = replaceOnce(config, 'ssl_certificate_key /etc/letsencrypt/live/sesame.example.net/privkey.pem;', 'ssl_certificate_key /certs/key.pem;', 'the key path')
    assert.equal(config.split('server_name sesame.example.net;').length - 1, 2)
    config = config.replaceAll('server_name sesame.example.net;', 'server_name localhost;')
    const file = join(workDir, 'nginx-default.conf')
    writeFileSync(file, config)
    docker(['run', '--detach', '--name', proxyContainer, '--network', 'host',
      '--volume', `${file}:/etc/nginx/conf.d/default.conf:ro`, '--volume', `${certs}:/certs:ro`, proxyImages.nginx])
    return
  }
  const certs = makeCertificate()
  docker(['network', 'create', '--subnet', traefikNetwork, traefikNetworkName])
  const staticFile = join(workDir, 'traefik.yml')
  const dynamicDirectory = join(workDir, 'dynamic')
  mkdirSync(dynamicDirectory)
  writeFileSync(staticFile, [
    'entryPoints:',
    '  websecure:',
    '    address: ":443"',
    'providers:',
    '  docker:',
    '    exposedByDefault: false',
    `    network: ${traefikNetworkName}`,
    '  file:',
    '    directory: /etc/traefik/dynamic',
    'certificatesResolvers:',
    '  letsencrypt:',
    '    acme:',
    '      email: check@example.invalid',
    '      storage: /tmp/acme.json',
    '      caServer: https://127.0.0.1:1/directory',
    '      tlsChallenge: {}',
    'log:',
    '  level: INFO',
    '',
  ].join('\n'))
  writeFileSync(join(dynamicDirectory, 'tls.yml'), ['tls:', '  certificates:', '    - certFile: /certs/cert.pem', '      keyFile: /certs/key.pem', ''].join('\n'))
  docker(['run', '--detach', '--name', proxyContainer, '--network', traefikNetworkName, '--publish', '127.0.0.1:443:443',
    '--volume', '/var/run/docker.sock:/var/run/docker.sock:ro', '--volume', `${staticFile}:/etc/traefik/traefik.yml:ro`,
    '--volume', `${dynamicDirectory}:/etc/traefik/dynamic:ro`, '--volume', `${certs}:/certs:ro`, proxyImages.traefik])
}

async function through(instance, path, extra = {}) {
  return request(instance, 'GET', path, { timeout: 5000, ...extra })
}

async function main() {
  mkdirSync(workDir, { recursive: true })
  docker(['tag', options.image, `${imageRepository}:${localVersion}`])
  const instance = connect(publicUrl, { insecure: true })
  const trusted = options.proxy === 'traefik' ? traefikNetwork : composeNetwork

  await check(`${options.proxy} starts in front of an empty port`, async () => {
    startProxy()
  })

  await check('the server starts from the compose file in the repository', async () => {
    compose(['up', '-d', '--wait'])
    await waitUntil('the server behind the proxy', async () => (await through(instance, '/livez')).status === 200, { seconds: 90 })
  })

  await check('without SESAME_TRUSTED_PROXIES the log tells the owner what to set', async () => {
    await through(instance, '/v1/desktop/status')
    const logs = composeLogs()
    assert.match(logs, /not a trusted proxy/, 'the server did not warn about X-Forwarded-For from an untrusted proxy')
    assert.match(logs, /SESAME_TRUSTED_PROXIES/)
  })

  await check(`with SESAME_TRUSTED_PROXIES=${trusted} the server restarts and prints a new setup link`, async () => {
    compose(['up', '-d', '--wait'], trusted)
    await waitUntil('the restarted server', async () => (await through(instance, '/livez')).status === 200, { seconds: 90 })
  })

  await check('the public address answers /livez and the console with the security headers', async () => {
    const live = expectStatus(await through(instance, '/livez'), 200, '/livez through the proxy')
    assert.equal(live.json.service, 'sesame-server')
    assert.match(live.headers['strict-transport-security'] ?? '', /max-age=31536000/, 'the recipe sends no Strict-Transport-Security')
    const index = expectStatus(await through(instance, '/'), 200, 'console through the proxy')
    assert.match(index.headers['content-security-policy'] ?? '', /^default-src 'none'; script-src 'self'/)
  })

  const owner = await check('first-run setup, pairing and the system page work through the proxy over HTTPS', async () => {
    const token = setupTokenFrom(composeLogs(), publicUrl)
    const setup = await runFirstSetup(instance, token, { name: ownerName, password: ownerPassword })
    assert.match(setup.session.state.setCookie, /;\s*Secure/i, 'the session cookie is not marked Secure on an https address')
    const member = await createMember(setup.session, 'Ada Proxy')
    const device = await pairDevice(instance, setup.session, { memberId: member.id, deviceName: 'Proxy laptop' })
    expectStatus(await desktopStatus(instance, device.token), 200, 'desktop status through the proxy')
    const system = await ownerSystem(setup.session)
    assert.equal(system.auditChainOk, true)
    return setup
  })

  await check('a request for another Host is not served as the owner API', async () => {
    const wrong = await owner.session.call('GET', '/v1/owner/session', { host: 'evil.example' })
    assert.ok(wrong.json?.owner === undefined && wrong.json?.csrfToken === undefined, `the proxy served the owner session for Host evil.example: ${wrong.text.slice(0, 200)}`)
    if (options.proxy === 'nginx') expectError(wrong, 421, 'host_mismatch', 'owner route with another Host through nginx')
    else assert.notEqual(wrong.status, 200, `Host evil.example got ${wrong.status}`)
    const spoofed = await owner.session.call('GET', '/v1/owner/session', { host: 'evil.example', headers: { 'x-forwarded-host': 'localhost' } })
    assert.ok(spoofed.json?.owner === undefined, 'X-Forwarded-Host changed which host the server accepted')
  })

  await check('a client cannot choose its own address with X-Forwarded-For', async () => {
    const statuses = []
    for (let attempt = 0; attempt < 8; attempt += 1) {
      const response = await request(instance, 'POST', '/v1/owner/login', {
        headers: { origin: instance.origin, 'x-forwarded-for': `203.0.113.${attempt + 10}` },
        json: { name: 'nobody-here', password: ownerPassword, code: '000000' },
      })
      statuses.push(response.status)
    }
    assert.ok(statuses.includes(429), `eight attempts with changing X-Forwarded-For values never hit the sign-in limit: ${statuses.join(', ')}`)
    assert.ok(statuses.every((status) => status === 401 || status === 429), `unexpected statuses: ${statuses.join(', ')}`)
    const malformed = await request(instance, 'GET', '/v1/desktop/status', { headers: { 'x-forwarded-for': 'not-an-address, also-not' } })
    assert.equal(malformed.status, 401)
  })

  if (options.proxy === 'nginx') {
    await check('nginx refuses a request body above 64 KiB before the server sees it', async () => {
      const response = await request(instance, 'POST', '/v1/owner/login', { headers: { origin: instance.origin }, json: { name: 'x'.repeat(70 * 1024), password: 'x', code: '000000' } })
      assert.equal(response.status, 413)
    })
  }

}
