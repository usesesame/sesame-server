import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import test from 'node:test'
import { readSelfHostingDocument, repositoryRoot, replaceOnce, sectionBlocks } from './selfhost-doc.mjs'

const read = (...parts) => readFileSync(join(repositoryRoot, ...parts), 'utf8')
const document = readSelfHostingDocument()

const jobBlock = (body, job) => {
  const start = body.indexOf(`\n  ${job}:\n`)
  assert.ok(start >= 0, `the ${job} job is missing from the workflow`)
  const rest = body.slice(start + 1)
  const afterFirst = rest.indexOf('\n') + 1
  const next = rest.slice(afterFirst).match(/^ {2}[a-z0-9_-]+:$/m)
  return next ? rest.slice(0, afterFirst + next.index) : rest
}

test('sectionBlocks returns the shell blocks of a section and stops at the next heading of the same level', () => {
  const sample = ['## One', '', '1. Step', '', '   ```sh', '   echo a', '   ```', '', '   ```text', '   not a command', '   ```', '', '### Inner', '```sh', 'echo b', '```', '## Two', '```sh', 'echo c', '```'].join('\n')
  assert.deepEqual(sectionBlocks(sample, '## One'), ['echo a', 'echo b'])
  assert.deepEqual(sectionBlocks(sample, '### Inner'), ['echo b'])
  assert.deepEqual(sectionBlocks(sample, '## Two'), ['echo c'])
  assert.throws(() => sectionBlocks(sample, '## Missing'))
})

test('replaceOnce refuses a block that no longer holds the text it rewrites', () => {
  assert.equal(replaceOnce('a b c', 'b', 'x', 'letter'), 'a x c')
  assert.throws(() => replaceOnce('a b c', 'z', 'x', 'letter'), /exactly once/)
  assert.throws(() => replaceOnce('b b', 'b', 'x', 'letter'), /exactly once/)
})

test('the sections the quick start check runs still hold the commands it expects', () => {
  const quickStart = sectionBlocks(document, '## Quick start')
  assert.equal(quickStart.length, 2)
  replaceOnce(quickStart[0], 'https://api.github.com/repos/usesesame/sesame-server/releases/latest', 'x', 'the release lookup address')
  replaceOnce(quickStart[0], 'https://raw.githubusercontent.com/usesesame/sesame-server/v${SESAME_IMAGE_TAG}/deploy/selfhost/compose.yaml', 'x', 'the compose file address')
  assert.match(quickStart[1], /docker compose up -d/)
  assert.match(quickStart[1], /\/livez/)
  assert.match(quickStart[1], /grep 'setup#token'/)
  const backup = sectionBlocks(document, '### Back up')
  assert.ok(backup.length >= 2)
  assert.match(backup[0], /backup \/data\/backups\/before-trip\.tar/)
  assert.match(backup[1], /check \/data\/backups\/before-trip\.tar/)
  const restore = sectionBlocks(document, '### Restore')
  assert.equal(restore.length, 2)
  assert.match(restore[0], /restore \/data\/backups\/before-trip\.tar/)
  assert.match(restore[1], /-v "\$PWD\/before-trip\.tar:\/restore\.tar:ro"/)
  assert.match(sectionBlocks(document, '## Verify and build the image')[0], /gh attestation verify/)
})

test('the reverse proxy recipes still hold the lines the proxy check rewrites', () => {
  const nginx = read('deploy', 'selfhost', 'nginx.conf')
  replaceOnce(nginx, 'ssl_certificate /etc/letsencrypt/live/sesame.example.net/fullchain.pem;', 'x', 'the certificate path')
  replaceOnce(nginx, 'ssl_certificate_key /etc/letsencrypt/live/sesame.example.net/privkey.pem;', 'x', 'the key path')
  assert.equal(nginx.split('server_name sesame.example.net;').length - 1, 2)
  assert.match(read('deploy', 'selfhost', 'Caddyfile'), /^\{\$SESAME_HOST\} \{/)
  assert.match(read('deploy', 'selfhost', 'compose.traefik.yaml'), /traefik\.http\.routers\.sesame\.entrypoints: websecure/)
})

test('the proxy check images are pinned by digest', () => {
  const dockerfile = read('scripts', 'selfhost-proxy-images', 'Dockerfile')
  const bases = [...dockerfile.matchAll(/^FROM (\S+) AS (\w+)$/gm)]
  assert.deepEqual(bases.map((match) => match[2]).sort(), ['caddy', 'nginx', 'traefik'])
  for (const [, image] of bases) assert.match(image, /@sha256:[0-9a-f]{64}$/)
})

test('pull requests build and run the release image, the quick start, the upgrade and every proxy recipe', () => {
  const workflow = read('.github', 'workflows', 'ci.yml')
  const image = jobBlock(workflow, 'selfhost-image')
  assert.match(image, /run: node scripts\/build-selfhost-images\.mjs\n/)
  assert.match(image, /docker\/setup-qemu-action@[0-9a-f]{40}/)
  assert.match(image, /--arch arm64 .* --run\n/)
  for (const [job, command] of [
    ['selfhost-quickstart', 'node scripts/selfhost-quickstart.mjs --mode local'],
    ['selfhost-upgrade', 'node scripts/selfhost-upgrade-check.mjs --from-published'],
    ['selfhost-proxy', 'node scripts/selfhost-proxy-check.mjs'],
  ]) {
    const block = jobBlock(workflow, job)
    assert.match(block, /needs: selfhost-image\n/, `${job} must reuse the image the selfhost-image job built`)
    assert.ok(block.includes(command), `${job} must run ${command}`)
  }
  assert.match(jobBlock(workflow, 'selfhost-proxy'), /proxy: \[caddy, nginx, traefik\]/)
  assert.match(workflow, /schedule:\n\s+- cron: /, 'a scheduled run must notice a pinned package that drifted from its base image')
})

test('a release is not finished until the published image passes the quick start without credentials', () => {
  const release = read('.github', 'workflows', 'release.yml')
  const published = jobBlock(release, 'published')
  assert.match(published, /needs: \[verify, publish, selfhost\]\n/)
  assert.match(published, /uses: \.\/\.github\/workflows\/selfhost-published\.yml\n/)
  const reusable = read('.github', 'workflows', 'selfhost-published.yml')
  assert.match(reusable, /docker logout ghcr\.io/)
  assert.match(reusable, /selfhost-quickstart\.mjs --mode published .*--verify-attestations/)
  assert.match(reusable, /--arch arm64 .* --run/)
  assert.match(reusable, /selfhost-upgrade-check\.mjs --from-published/)
  assert.doesNotMatch(reusable, /docker\/login-action/)
  assert.doesNotMatch(reusable, /packages: write/)
})
