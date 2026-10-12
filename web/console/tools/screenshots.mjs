import { createRequire } from 'node:module'
import { mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { startMock } from './mock-server.mjs'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const outDir = process.env.SHOTS_DIR
const playwrightPath = process.env.PLAYWRIGHT_CORE
if (!outDir || !playwrightPath) {
  console.error('Set SHOTS_DIR to an output folder outside the repository and PLAYWRIGHT_CORE to the playwright-core package folder.')
  process.exit(2)
}
mkdirSync(outDir, { recursive: true })
const { chromium } = createRequire(import.meta.url)(playwrightPath)

const viewports = { desktop: { width: 1280, height: 820 }, phone: { width: 390, height: 844 } }
const schemes = ['light', 'dark']
const problems = []

async function shot(page, name, viewport, scheme) {
  await page.waitForTimeout(150)
  await page.screenshot({ path: join(outDir, `${name}-${viewport}-${scheme}.png`), fullPage: true })
}

async function run(viewport, scheme) {
  const browser = await chromium.launch()
  const context = await browser.newContext({ viewport: viewports[viewport], colorScheme: scheme, acceptDownloads: true })
  const watch = (page, label) => {
    page.on('console', (message) => { if (['error', 'warning'].includes(message.type()) && !/^Failed to load resource: the server responded with a status of 4\d\d/.test(message.text())) problems.push(`${label} ${viewport}/${scheme} console ${message.type()}: ${message.text()}`) })
    page.on('pageerror', (error) => problems.push(`${label} ${viewport}/${scheme} page error: ${error.message}`))
  }

  const setupMock = await startMock({ dist: join(root, 'dist'), setupRequired: true })
  let page = await context.newPage()
  watch(page, 'setup')
  await page.goto(`${setupMock.origin}/setup`)
  await shot(page, '00-setup-no-token', viewport, scheme)
  await page.goto(setupMock.setupUrl)
  await page.getByRole('heading', { name: 'Create the first owner' }).waitFor()
  if (page.url().includes('token')) problems.push('setup token stayed in the address bar')
  await page.getByLabel('Name').fill('Alex Rivera')
  await page.getByLabel(/^Password/).fill('correct horse battery staple')
  await page.getByLabel('Repeat the password').fill('correct horse battery staple')
  await shot(page, '01-setup', viewport, scheme)
  await page.getByLabel('Six-digit code').fill('000000')
  await page.getByRole('button', { name: 'Create owner' }).click()
  await page.getByRole('alert').waitFor()
  await shot(page, '02-setup-error', viewport, scheme)
  await page.getByLabel('Six-digit code').fill('123456')
  await page.getByRole('button', { name: 'Create owner' }).click()
  await page.getByRole('heading', { name: 'Devices' }).waitFor()
  await page.close()
  await setupMock.close()

  const mock = await startMock({ dist: join(root, 'dist') })
  page = await context.newPage()
  watch(page, 'app')
  await page.goto(`${mock.origin}/`)
  await page.getByRole('heading', { name: 'Sign in' }).waitFor()
  await shot(page, '03-signin', viewport, scheme)
  await page.getByLabel('Name').fill(mock.credentials.name)
  await page.getByLabel('Password').fill('wrong password here')
  await page.getByLabel('Six-digit code').fill('123456')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await page.getByRole('alert').waitFor()
  await shot(page, '04-signin-error', viewport, scheme)
  await page.getByLabel('Password').fill(mock.credentials.password)
  await page.getByLabel('Six-digit code').fill('123456')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await page.getByRole('heading', { name: 'Devices' }).waitFor()
  await page.getByText('Studio desktop').waitFor()
  await shot(page, '10-devices', viewport, scheme)

  await page.getByRole('button', { name: 'Revoke' }).first().click()
  await shot(page, '11-devices-confirm', viewport, scheme)
  await page.getByRole('button', { name: 'Keep' }).click()

  await page.goto(`${mock.origin}/#/members`)
  await page.getByText('Priya Raman').waitFor()
  await shot(page, '20-members', viewport, scheme)

  await page.goto(`${mock.origin}/#/pairing`)
  await page.getByRole('button', { name: 'Create pairing code' }).click()
  await page.getByText('Pairing code for Alex Rivera').waitFor()
  await shot(page, '30-pairing', viewport, scheme)

  await page.goto(`${mock.origin}/#/audit`)
  await page.getByText('owner.login').first().waitFor()
  await shot(page, '40-audit', viewport, scheme)

  await page.goto(`${mock.origin}/#/owners`)
  await page.getByText('Sam Okafor').waitFor()
  mock.state.recentUntil = 0
  await page.getByLabel('Name').fill('Riley Chen')
  await page.getByRole('button', { name: 'Invite owner' }).click()
  await page.getByRole('dialog').waitFor()
  await shot(page, '51-stepup', viewport, scheme)
  await page.getByLabel('Password').fill(mock.credentials.password)
  await page.getByLabel('Six-digit code').fill('123456')
  await page.getByRole('button', { name: 'Confirm' }).click()
  await page.getByText('Setup link for Riley Chen').waitFor()
  await shot(page, '50-owners', viewport, scheme)

  await page.goto(`${mock.origin}/#/settings`)
  await page.getByLabel('Instance name').waitFor()
  await shot(page, '60-settings', viewport, scheme)

  await page.goto(`${mock.origin}/#/system`)
  await page.getByText('Database size').waitFor()
  await shot(page, '70-system', viewport, scheme)
  const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'Export data' }).click()])
  if (!/^sesame-export-\d{4}-\d{2}-\d{2}\.json$/.test(download.suggestedFilename())) problems.push(`unexpected export file name ${download.suggestedFilename()}`)

  await page.close()
  await mock.close()

  const brokenMock = await startMock({ dist: join(root, 'dist'), chainBroken: true, warnings: ['The audit log chain breaks at entry 41: the stored hash does not match the recomputed hash.', 'Requests from a private address carry X-Forwarded-For, but that address is not a trusted proxy.'], lastBackupAt: null })
  page = await context.newPage()
  watch(page, 'broken')
  await page.goto(`${brokenMock.origin}/`)
  await page.getByLabel('Name').fill(brokenMock.credentials.name)
  await page.getByLabel('Password').fill(brokenMock.credentials.password)
  await page.getByLabel('Six-digit code').fill('123456')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await page.getByRole('heading', { name: 'Devices' }).waitFor()
  await page.goto(`${brokenMock.origin}/#/audit`)
  await page.getByText('Chain broken').waitFor()
  await shot(page, '41-audit-broken', viewport, scheme)
  await page.goto(`${brokenMock.origin}/#/system`)
  await page.getByText('Database size').waitFor()
  await shot(page, '71-system-warnings', viewport, scheme)
  await page.goto(`${brokenMock.origin}/pair#code=fictionalcode&fp=abcd`)
  await page.getByRole('heading', { name: 'Open this link in the Sesame app' }).waitFor()
  if (page.url().includes('code=')) problems.push('pairing code stayed in the address bar')
  await shot(page, '80-pair-landing', viewport, scheme)
  await page.close()
  await brokenMock.close()

  const scenes = [['72-updates-unset', 'unset'], ['73-updates-available', 'available'], ['74-updates-current', 'current'], ['75-updates-error', 'error'], ['76-updates-not-configured', 'unconfigured']]
  for (const [name, updates] of scenes) {
    const updatesMock = await startMock({ dist: join(root, 'dist'), updates })
    page = await context.newPage()
    watch(page, name)
    await page.goto(`${updatesMock.origin}/`)
    await page.getByLabel('Name').fill(updatesMock.credentials.name)
    await page.getByLabel('Password').fill(updatesMock.credentials.password)
    await page.getByLabel('Six-digit code').fill('123456')
    await page.getByRole('button', { name: 'Sign in' }).click()
    await page.getByRole('heading', { name: 'Devices' }).waitFor()
    if (updates === 'available') {
      await page.getByText('An update is available.').waitFor()
      await shot(page, '77-banner', viewport, scheme)
    } else if (await page.getByText('An update is available.').count() > 0) {
      problems.push(`banner shown for ${updates}`)
    }
    await page.goto(`${updatesMock.origin}/#/updates`)
    await page.getByRole('heading', { name: 'Updates' }).waitFor()
    await page.getByText('Version 0.1.0').waitFor()
    if (await page.getByText('An update is available.').count() > 0) problems.push('banner shown on the Updates page')
    await shot(page, name, viewport, scheme)
    await page.close()
    await updatesMock.close()
  }

  await browser.close()
}

for (const viewport of Object.keys(viewports)) for (const scheme of schemes) await run(viewport, scheme)

if (problems.length) {
  console.error(problems.join('\n'))
  process.exit(1)
}
console.log(`Screenshots written to ${outDir} with no console errors.`)
