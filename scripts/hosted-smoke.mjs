import { pathToFileURL } from 'node:url'
import { describeFailure, legalVersions, redact, reporter, repositoryRoot, startStack } from './hosted-stack.mjs'
import { accountSeesVault, provisionAccount, pushAndPull, turnSyncOn } from './hosted-journey.mjs'

async function main(report, stack) {
  const { check } = report
  const binaries = await check('cmd/api, cmd/migrate and cmd/adminctl build from this checkout', async () => {
    stack.buildDatabaseHelper(repositoryRoot)
    return stack.build(repositoryRoot, 'candidate')
  })
  await check('a fresh PostgreSQL database migrates and a second run changes nothing', async () => {
    stack.createDatabase()
    stack.migrate(binaries)
    stack.migrate(binaries)
  })
  const api = await check('the API starts on a loopback port with the hosted settings and reports ready', async () => stack.startApi(binaries))
  const context = await provisionAccount(stack, { binaries, legal: legalVersions(repositoryRoot), report })
  await turnSyncOn(stack, context, { report })
  await pushAndPull(stack, context, { report })
  await accountSeesVault(context, { report })
  await check('the API stops on SIGTERM and exits cleanly', async () => {
    const outcome = await api.stop()
    if (outcome.code !== 0) throw new Error(`the API exited with ${outcome.code ?? outcome.signal}`)
  })
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const report = reporter()
  const started = Date.now()
  let stack
  try {
    stack = await startStack({ label: 'smoke' })
    await main(report, stack)
    console.log(`Hosted smoke passed: ${report.passed} checks in ${Math.round((Date.now() - started) / 1000)} seconds.`)
  } catch (error) {
    console.error(`Hosted smoke failed: ${describeFailure(error)}`)
    for (const [index, server] of (stack?.servers ?? []).entries()) console.error(`\n--- API log ${index + 1} ---\n${redact(server.log().slice(-4000))}`)
    process.exitCode = 1
  } finally {
    await stack?.close()
  }
}
