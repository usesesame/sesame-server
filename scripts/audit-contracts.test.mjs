import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const workflow = readFileSync(join(root, '.github', 'workflows', 'ci.yml'), 'utf8')

function jobBlock(body, job) {
  const start = body.indexOf(`\n  ${job}:\n`)
  assert.ok(start >= 0, `the ${job} job is missing from the workflow`)
  const rest = body.slice(start + 1)
  const afterFirst = rest.indexOf('\n') + 1
  const next = rest.slice(afterFirst).match(/^ {2}[a-z0-9_-]+:$/m)
  return next ? rest.slice(0, afterFirst + next.index) : rest
}

const auditCommand = 'npm audit --audit-level=high'

for (const job of ['server', 'account', 'admin']) {
  test(`the ${job} job audits npm dependencies at high`, () => {
    const commands = [...jobBlock(workflow, job).matchAll(/^\s*(?:- )?run: (.+)$/gm)].map(
      ([, command]) => command.trim(),
    )
    assert.ok(
      commands.includes(auditCommand),
      `the ${job} job must run \`${auditCommand}\` as a step, so a high advisory fails CI`,
    )
  })
}
