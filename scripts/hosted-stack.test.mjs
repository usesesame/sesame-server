import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, describe, it } from 'node:test'
import { exportSource, legalVersions, migrationNames, newestReleaseTag, redact, startMailbox, webBrowser } from './hosted-stack.mjs'

const scratch = []
const scratchDirectory = () => {
  const directory = mkdtempSync(join(tmpdir(), 'sesame-hosted-test-'))
  scratch.push(directory)
  return directory
}

afterEach(() => {
  for (const directory of scratch.splice(0)) rmSync(directory, { recursive: true, force: true })
})

function git(directory, ...args) {
  const result = spawnSync('git', ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', '-c', 'tag.gpgsign=false', ...args], { cwd: directory, encoding: 'utf8' })
  assert.equal(result.status, 0, `git ${args.join(' ')}: ${result.stderr}`)
  return result.stdout
}

function repositoryWithTags(tags) {
  const directory = scratchDirectory()
  git(directory, 'init', '--quiet')
  writeFileSync(join(directory, 'file.txt'), 'one\n')
  git(directory, 'add', 'file.txt')
  git(directory, 'commit', '--quiet', '-m', 'Add a file')
  for (const tag of tags) git(directory, 'tag', tag)
  return directory
}

describe('previous release lookup', () => {
  it('picks the highest release tag by version, not by name or date', () => {
    const directory = repositoryWithTags(['v0.9.0', 'v0.10.0', 'v0.10.1-rc1', 'nightly', 'v0.2.0'])
    assert.equal(newestReleaseTag(directory), 'v0.10.0')
  })

  it('fails when the checkout holds no release tag', () => {
    const directory = repositoryWithTags(['nightly', 'v1.0.0-beta'])
    assert.throws(() => newestReleaseTag(directory), /No release tag/)
  })

  it('exports exactly the tagged source and fails for an unknown ref', () => {
    const directory = repositoryWithTags(['v1.0.0'])
    writeFileSync(join(directory, 'file.txt'), 'two\n')
    git(directory, 'commit', '--quiet', '-a', '-m', 'Change the file')
    const destination = join(scratchDirectory(), 'source')
    exportSource(directory, 'v1.0.0', destination)
    assert.equal(readFileSync(join(destination, 'file.txt'), 'utf8'), 'one\n')
    assert.throws(() => exportSource(directory, 'v9.9.9', join(scratchDirectory(), 'missing')), /v9\.9\.9/)
  })
})

describe('source readers', () => {
  it('reads the legal versions the registration route requires', () => {
    const directory = scratchDirectory()
    mkdirSync(join(directory, 'internal', 'httpapi'), { recursive: true })
    writeFileSync(join(directory, 'internal', 'httpapi', 'auth_routes.go'), 'const (\n\ttermsVersion   = "2030-01-02"\n\tprivacyVersion = "2030-03-04"\n)\n')
    assert.deepEqual(legalVersions(directory), { terms: '2030-01-02', privacy: '2030-03-04' })
    writeFileSync(join(directory, 'internal', 'httpapi', 'auth_routes.go'), 'package httpapi\n')
    assert.throws(() => legalVersions(directory), /termsVersion is not defined/)
  })

  it('lists migrations in order', () => {
    const directory = scratchDirectory()
    const migrations = join(directory, 'internal', 'accounts', 'migrations')
    mkdirSync(migrations, { recursive: true })
    for (const name of ['0002_b.sql', '0001_a.sql', 'migrations_test.go']) writeFileSync(join(migrations, name), '')
    assert.deepEqual(migrationNames(directory), ['0001_a.sql', '0002_b.sql'])
  })

  it('redacts setup, invitation and verification tokens', () => {
    const text = 'a http://x/setup?token=abc123 b http://x/register?invite=def456 c http://x/verify-email#token=ghi789'
    assert.equal(redact(text).match(/\[redacted\]/g).length, 3)
    assert.doesNotMatch(redact(text), /abc123|def456|ghi789/)
  })
})

describe('mail capture', () => {
  it('records a message the way the API sends it and finds it by recipient and subject', async () => {
    const mailbox = await startMailbox()
    try {
      const [host, port] = mailbox.address.split(':')
      const socket = net.connect(Number(port), host)
      const lines = []
      socket.on('data', (chunk) => lines.push(chunk.toString('utf8')))
      const send = (line) => new Promise((resolveSend) => socket.write(`${line}\r\n`, resolveSend))
      await send('EHLO localhost')
      await send('MAIL FROM:<accounts@example.com>')
      await send('RCPT TO:<ada@example.com>')
      await send('DATA')
      await send('Subject: Verify your Sesame account email\r\n\r\nOpen http://localhost/verify-email#token=abc_DEF-123\r\n..dot line\r\n.')
      await send('QUIT')
      const message = await mailbox.waitFor('ada@example.com', 'Verify your Sesame account email', 5)
      assert.match(message.text, /#token=abc_DEF-123/)
      assert.match(message.text, /\r\n\.dot line/)
      await assert.rejects(mailbox.waitFor('nobody@example.com', 'Verify your Sesame account email', 1), /did not succeed/)
      socket.destroy()
    } finally {
      await mailbox.close()
    }
  })
})

describe('browser with a cookie jar', () => {
  it('keeps cookies, sends the anti forgery token on changes only and drops expired cookies', async () => {
    const seen = []
    const server = http.createServer((incoming, outgoing) => {
      seen.push({ method: incoming.method, url: incoming.url, cookie: incoming.headers.cookie ?? '', origin: incoming.headers.origin, csrf: incoming.headers['x-sesame-csrf'] ?? '' })
      outgoing.setHeader('content-type', 'application/json')
      if (incoming.url === '/csrf') outgoing.setHeader('set-cookie', ['csrf=token-one; Path=/; HttpOnly'])
      if (incoming.url === '/login') outgoing.setHeader('set-cookie', ['session=value-one; Path=/; HttpOnly'])
      if (incoming.url === '/logout') outgoing.setHeader('set-cookie', ['session=; Path=/; Max-Age=0'])
      outgoing.end(JSON.stringify({ token: 'token-one' }))
    })
    await new Promise((resolveListen) => server.listen(0, '127.0.0.1', resolveListen))
    try {
      const instance = { origin: `http://127.0.0.1:${server.address().port}`, host: `127.0.0.1:${server.address().port}`, hostname: '127.0.0.1', port: String(server.address().port), secure: false, address: '127.0.0.1' }
      const browser = webBrowser(instance, 'http://site.example', '/csrf')
      await browser.protect()
      await browser.call('POST', '/login', { json: {} })
      await browser.call('GET', '/me')
      await browser.call('POST', '/logout', { json: {} })
      await browser.call('GET', '/after')
      assert.deepEqual(seen.map((entry) => [entry.url, entry.csrf, entry.cookie]), [
        ['/csrf', '', ''],
        ['/login', 'token-one', 'csrf=token-one'],
        ['/me', '', 'csrf=token-one; session=value-one'],
        ['/logout', 'token-one', 'csrf=token-one; session=value-one'],
        ['/after', '', 'csrf=token-one'],
      ])
      assert.ok(seen.every((entry) => entry.origin === 'http://site.example'))
    } finally {
      server.closeAllConnections()
      await new Promise((resolveClose) => server.close(resolveClose))
    }
  })
})
