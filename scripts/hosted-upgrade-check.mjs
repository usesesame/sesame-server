import assert from 'node:assert/strict'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { describeFailure, exportSource, legalVersions, migrationNames, newestReleaseTag, redact, reporter, repositoryRoot, startStack } from './hosted-stack.mjs'
import { provisionAccount, pushAndPull, pushNextRevision, readBack, turnSyncOn } from './hosted-journey.mjs'

function parseArguments(args) {
  const parsed = { from: '' }
  for (let index = 0; index < args.length; index += 1) {
    if (args[index] === '--from') parsed.from = args[++index] ?? ''
    else throw new Error(`Unknown argument ${args[index]}. Use --from <tag> to name the release to upgrade from, which defaults to the newest release tag.`)
  }
  return parsed
}

async function main(report, stack, options) {
  const { check } = report
  const tag = options.from || newestReleaseTag(repositoryRoot)
  const previousRoot = join(stack.root, 'previous')

  const previous = await check(`${tag} builds cmd/api, cmd/migrate and cmd/adminctl from its own source`, async () => {
    exportSource(repositoryRoot, tag, previousRoot)
    return stack.build(previousRoot, 'previous')
  })
  const candidate = await check('this checkout builds the same three commands', async () => {
    stack.buildDatabaseHelper(repositoryRoot)
    return stack.build(repositoryRoot, 'candidate')
  })

  await check(`${tag} migrates a fresh PostgreSQL database`, async () => {
    stack.createDatabase()
    stack.migrate(previous)
  })
  const oldApi = await check(`the ${tag} API starts on that database`, async () => stack.startApi(previous))
  const context = await provisionAccount(stack, { binaries: previous, legal: legalVersions(previousRoot), report })
  await turnSyncOn(stack, context, { report })
  await pushAndPull(stack, context, { report })
  await check(`the ${tag} API stops`, async () => {
    const outcome = await oldApi.stop()
    if (outcome.code !== 0) throw new Error(`the ${tag} API exited with ${outcome.code ?? outcome.signal}`)
  })

  const added = migrationNames(repositoryRoot).filter((name) => !migrationNames(previousRoot).includes(name))
  if (added.length > 0) {
    await check('the new API refuses to start until the new migrations have run', async () => {
      const refused = await stack.startApi(candidate, { expectExit: true })
      const outcome = await refused.exited
      assert.notEqual(outcome.code, 0, 'the new API started on an old schema')
      assert.match(refused.log(), /schema is not current/)
      for (const name of added) assert.ok(refused.log().includes(name.replace(/\.sql$/, '')), `the refusal does not name ${name}`)
    })
  }

  await check('the new migrations run over the data the old release wrote', async () => stack.migrate(candidate))
  await check('the new API starts on the upgraded database and reports ready', async () => stack.startApi(candidate))
  await readBack(stack, context, { report, label: 'after the upgrade' })
  await pushNextRevision(stack, context, { report, label: 'after the upgrade' })
  console.log(`Upgraded from ${tag} across ${added.length} new migration${added.length === 1 ? '' : 's'}.`)
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const report = reporter()
  const started = Date.now()
  let stack
  try {
    stack = await startStack({ label: 'upgrade' })
    await main(report, stack, parseArguments(process.argv.slice(2)))
    console.log(`Hosted upgrade check passed: ${report.passed} checks in ${Math.round((Date.now() - started) / 1000)} seconds.`)
  } catch (error) {
    console.error(`Hosted upgrade check failed: ${describeFailure(error)}`)
    for (const [index, server] of (stack?.servers ?? []).entries()) console.error(`\n--- API log ${index + 1} ---\n${redact(server.log().slice(-4000))}`)
    process.exitCode = 1
  } finally {
    await stack?.close()
  }
}
