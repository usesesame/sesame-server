import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { copyFile, mkdir, mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { parseEnvText, renderEnvFile, unmanagedEnvLines } from './setup-lib.mjs'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')

test('unmanaged environment lines survive a setup rerun', () => {
  const existing = [
    '# old generated header',
    'SESAME_DATABASE_PASSWORD=managed-value',
    'SESAME_SMTP_ADDR=smtp.example.test:587',
    'CUSTOM_FLAG=kept',
    '',
  ].join('\n')
  const preserved = unmanagedEnvLines(existing, ['SESAME_DATABASE_PASSWORD'])
  assert.deepEqual(preserved, ['SESAME_SMTP_ADDR=smtp.example.test:587', 'CUSTOM_FLAG=kept'])
  const rendered = renderEnvFile({ settings: new Map([['SESAME_DATABASE_PASSWORD', 'new-value']]), preservedLines: preserved })
  assert.ok(rendered.includes('SESAME_DATABASE_PASSWORD=new-value'))
  assert.ok(!rendered.includes('managed-value'))
  assert.ok(rendered.includes('SESAME_SMTP_ADDR=smtp.example.test:587'))
  assert.ok(rendered.includes('CUSTOM_FLAG=kept'))
  assert.equal(parseEnvText(rendered).get('SESAME_DATABASE_PASSWORD'), 'new-value')
})

test('setup keeps operator variables, secrets, and file mode across reruns', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'sesame-setup-'))
  try {
    await mkdir(join(directory, 'scripts'), { recursive: true })
    await copyFile(join(root, 'scripts', 'setup.mjs'), join(directory, 'scripts', 'setup.mjs'))
    await copyFile(join(root, 'scripts', 'setup-lib.mjs'), join(directory, 'scripts', 'setup-lib.mjs'))
    const envPath = join(directory, 'deploy', 'compose', '.env')

    const first = spawnSync('node', [join(directory, 'scripts', 'setup.mjs')], { encoding: 'utf8' })
    assert.equal(first.status, 0, first.stderr)
    const initial = await readFile(envPath, 'utf8')
    const secrets = parseEnvText(initial)
    assert.ok(secrets.get('SESAME_CAPABILITY_SIGNING_KEY'))
    assert.equal(secrets.get('SESAME_CAPABILITY_PUBLIC_KEY')?.length, 43)

    await writeFile(envPath, `${initial}SESAME_SMTP_ADDR=smtp.example.test:587\n`, { mode: 0o644 })
    const second = spawnSync('node', [join(directory, 'scripts', 'setup.mjs')], { encoding: 'utf8' })
    assert.equal(second.status, 0, second.stderr)
    const rerun = parseEnvText(await readFile(envPath, 'utf8'))
    assert.equal(rerun.get('SESAME_SMTP_ADDR'), 'smtp.example.test:587')
    assert.equal(rerun.get('SESAME_CAPABILITY_SIGNING_KEY'), secrets.get('SESAME_CAPABILITY_SIGNING_KEY'))
    assert.equal(rerun.get('SESAME_CAPABILITY_PUBLIC_KEY'), secrets.get('SESAME_CAPABILITY_PUBLIC_KEY'))
    assert.equal((await stat(envPath)).mode & 0o777, 0o600)
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
})

test('the dev command points at the self-hosting stack and its database override', () => {
  const source = readFileSync(join(root, 'scripts', 'dev.mjs'), 'utf8')
  assert.ok(source.includes("'deploy', 'compose', 'compose.yaml'"))
  assert.ok(source.includes("'deploy', 'compose', 'compose.dev.yaml'"))
  assert.ok(existsSync(join(root, 'deploy', 'compose', 'compose.dev.yaml')))
  assert.ok(existsSync(join(root, 'deploy', 'compose', 'compose.yaml')))
  assert.ok(!source.includes("resolve(backendRoot, '..', 'compose.yaml')"))
})

test('the first-account invite runs through the admin CLI and the setup wrapper', () => {
  const wrapper = readFileSync(join(root, 'scripts', 'admin-bootstrap.mjs'), 'utf8')
  assert.ok(wrapper.includes("'bootstrap', 'reset', 'invite'"), 'the wrapper does not accept invite')
  assert.ok(wrapper.includes('SESAME_WEB_ORIGIN'), 'the wrapper does not pass the portal origin')

  const cli = readFileSync(join(root, 'cmd', 'adminctl', 'main.go'), 'utf8')
  assert.ok(cli.includes('"invite"'), 'adminctl does not accept invite')
  assert.ok(cli.includes('CreateBetaInvite'), 'adminctl does not create an invite')

  const pkg = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'))
  assert.equal(pkg.scripts['account:invite'], 'node ./scripts/admin-bootstrap.mjs invite')
})
