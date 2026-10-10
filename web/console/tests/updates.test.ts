import { fireEvent, screen, waitFor, within } from '@testing-library/svelte'
import { expect, test, vi } from 'vitest'
import { apiError, json, upToDate } from './support/fake-api'
import { openApp, useCleanApp } from './support/app'

useCleanApp()

const available = {
  ...upToDate,
  latest: { ...upToDate.latest, version: '0.2.0', notesUrl: 'https://updates.example.test/notes/0.2.0', security: true },
  available: true,
  commands: [
    { label: 'Pull the image', text: 'docker compose pull sesame' },
    { label: 'Restart', text: 'docker compose up -d sesame' },
  ],
}
const unset = { ...upToDate, enabled: null, latest: null, checkedAt: null }
const system = (updateAvailable: boolean) => ({ version: '0.1.0', commit: 'unknown', schemaVersion: 3, databaseBytes: 2048, lastBackupAt: null, warnings: [], auditChainOk: true, updateAvailable })
const stepUpNeeded = () => apiError(403, 'step_up_required', 'Confirm your password and a current code to continue.')

async function openUpdates(info: object, extra: Parameters<typeof openApp>[1] = {}) {
  const fake = openApp('/', { 'GET /v1/owner/updates': () => info, ...extra })
  await fireEvent.click(await screen.findByRole('link', { name: 'Updates' }))
  await screen.findByRole('heading', { name: 'Updates' })
  return fake
}

async function confirmStepUp() {
  const dialog = await screen.findByRole('dialog')
  await fireEvent.input(dialog.querySelector('input[type=password]')!, { target: { value: 'fictional password for tests' } })
  await fireEvent.input(dialog.querySelector('input[inputmode=numeric]')!, { target: { value: '123456' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
}

test('shows the current version and that it is up to date', async () => {
  await openUpdates(upToDate)
  expect(await screen.findByText('Version 0.1.0')).toBeTruthy()
  expect(screen.getByText('Up to date')).toBeTruthy()
  expect(screen.queryByText('Security update')).toBeNull()
  expect(screen.queryByRole('button', { name: 'Copy' })).toBeNull()
  expect((screen.getByRole('checkbox', { name: /Check for updates/ }) as HTMLInputElement).checked).toBe(true)
})

test('shows the new version, the security mark, the notes link and copyable commands', async () => {
  const writeText = vi.fn(async () => undefined)
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
  await openUpdates(available)
  expect(await screen.findByText('0.2.0 is available')).toBeTruthy()
  expect(screen.getByText('Security update')).toBeTruthy()
  const notes = screen.getByRole('link', { name: 'Release notes' })
  expect(notes.getAttribute('href')).toBe('https://updates.example.test/notes/0.2.0')
  expect(notes.getAttribute('rel')).toContain('noopener')
  expect(screen.getByText('docker compose pull sesame')).toBeTruthy()
  await fireEvent.click(within(screen.getByRole('group', { name: 'Restart' })).getByRole('button', { name: 'Copy' }))
  expect(writeText).toHaveBeenCalledWith('docker compose up -d sesame')
  expect(await screen.findByRole('button', { name: 'Copied' })).toBeTruthy()
})

test('omits the security mark for an ordinary release', async () => {
  await openUpdates({ ...available, latest: { ...available.latest, security: false } })
  expect(await screen.findByText('0.2.0 is available')).toBeTruthy()
  expect(screen.queryByText('Security update')).toBeNull()
})

test('does not link a release notes address that is not http or https', async () => {
  await openUpdates({ ...available, latest: { ...available.latest, notesUrl: 'javascript:alert(1)' } })
  expect(await screen.findByText('0.2.0 is available')).toBeTruthy()
  expect(screen.queryByRole('link', { name: 'Release notes' })).toBeNull()
})

test('asks for a choice when update checks were never set and offers one primary button', async () => {
  await openUpdates(unset)
  const turnOn = await screen.findByRole('button', { name: 'Turn on update checks' })
  expect(screen.getByText("Only this server's address is visible to Sesame.")).toBeTruthy()
  expect(document.querySelectorAll('.view button.primary')).toHaveLength(1)
  expect(turnOn.classList.contains('primary')).toBe(true)
  expect(screen.queryByRole('checkbox')).toBeNull()
  expect(screen.queryByRole('button', { name: 'Check now' })).toBeNull()
})

test('turns checks on after step-up and shows the result', async () => {
  let attempts = 0
  let current: object = unset
  const { calls } = await openUpdates(unset, {
    'GET /v1/owner/updates': () => current,
    'PATCH /v1/owner/updates': () => { if (++attempts === 1) return stepUpNeeded(); current = upToDate; return upToDate },
    'POST /v1/owner/step-up': () => json({ ok: true }),
  })
  await fireEvent.click(await screen.findByRole('button', { name: 'Turn on update checks' }))
  await confirmStepUp()
  expect(await screen.findByText('Up to date')).toBeTruthy()
  const patches = calls.filter((call) => call.method === 'PATCH' && call.path === '/v1/owner/updates')
  expect(patches).toHaveLength(2)
  expect(patches[1].body).toEqual({ enabled: true, channel: 'stable' })
  expect(screen.queryByRole('button', { name: 'Turn on update checks' })).toBeNull()
})

test('keeps checks off when the owner chooses so', async () => {
  const off = { ...upToDate, enabled: false, latest: null, checkedAt: null }
  const { calls } = await openUpdates(unset, { 'PATCH /v1/owner/updates': () => off })
  await fireEvent.click(await screen.findByRole('button', { name: 'Keep them off' }))
  await waitFor(() => expect((screen.getByRole('checkbox') as HTMLInputElement).checked).toBe(false))
  expect(calls.find((call) => call.method === 'PATCH' && call.path === '/v1/owner/updates')!.body).toEqual({ enabled: false, channel: 'stable' })
  expect(screen.queryByRole('button', { name: 'Check now' })).toBeNull()
})

test('turns checks off with the toggle', async () => {
  const off = { ...upToDate, enabled: false, latest: null, checkedAt: null }
  const { calls } = await openUpdates(upToDate, { 'PATCH /v1/owner/updates': () => off })
  await fireEvent.click(await screen.findByRole('checkbox', { name: /Check for updates/ }))
  await waitFor(() => expect(screen.queryByRole('button', { name: 'Check now' })).toBeNull())
  expect(calls.find((call) => call.method === 'PATCH' && call.path === '/v1/owner/updates')!.body).toEqual({ enabled: false, channel: 'stable' })
  expect((screen.getByRole('checkbox') as HTMLInputElement).checked).toBe(false)
})

test('puts the toggle back and shows the message when step-up is cancelled', async () => {
  await openUpdates(upToDate, { 'PATCH /v1/owner/updates': () => stepUpNeeded() })
  await fireEvent.click(await screen.findByRole('checkbox', { name: /Check for updates/ }))
  const dialog = await screen.findByRole('dialog')
  await fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  expect(await screen.findByText('The action was cancelled.')).toBeTruthy()
  expect((screen.getByRole('checkbox') as HTMLInputElement).checked).toBe(true)
})

test('checks now and shows the new release', async () => {
  const { calls } = await openUpdates(upToDate, {
    'POST /v1/owner/updates/check': () => { return available },
  })
  await fireEvent.click(await screen.findByRole('button', { name: 'Check now' }))
  expect(await screen.findByText('0.2.0 is available')).toBeTruthy()
  expect(calls.filter((call) => call.method === 'GET' && call.path === '/v1/owner/updates')).toHaveLength(1)
  expect(calls.some((call) => call.method === 'POST' && call.path === '/v1/owner/updates/check')).toBe(true)
})

test('says checks are off and drops the check button when the server refuses a check', async () => {
  const off = { ...upToDate, enabled: false, latest: null, checkedAt: null }
  let served: object = upToDate
  await openUpdates(upToDate, {
    'GET /v1/owner/updates': () => served,
    'POST /v1/owner/updates/check': () => { served = off; return apiError(409, 'updates_off', 'Update checks are off.') },
  })
  await fireEvent.click(await screen.findByRole('button', { name: 'Check now' }))
  expect((await screen.findByRole('alert')).textContent).toBe('Update checks are off.')
  await waitFor(() => expect(screen.queryByRole('button', { name: 'Check now' })).toBeNull())
})

test('shows the server message when checks are rate limited', async () => {
  await openUpdates(upToDate, { 'POST /v1/owner/updates/check': () => apiError(429, 'too_many_attempts', 'Too many attempts. Try again later.') })
  await fireEvent.click(await screen.findByRole('button', { name: 'Check now' }))
  expect((await screen.findByRole('alert')).textContent).toBe('Too many attempts. Try again later.')
})

test('maps an invalid_updates refusal to one sentence', async () => {
  await openUpdates(upToDate, { 'PATCH /v1/owner/updates': () => apiError(400, 'invalid_updates', 'Enabled must be true or false.') })
  await fireEvent.click(await screen.findByRole('checkbox', { name: /Check for updates/ }))
  expect((await screen.findByRole('alert')).textContent).toBe('The update settings were not accepted.')
  expect((screen.getByRole('checkbox') as HTMLInputElement).checked).toBe(true)
})

test('tells the owner when a direct update needs a newer starting version', async () => {
  await openUpdates({ ...available, latest: { ...available.latest, minimumFrom: '0.1.5' } })
  expect(await screen.findByText('Direct updates need 0.1.5 or later.')).toBeTruthy()
})

test('stays quiet about the starting version when the current one is new enough', async () => {
  await openUpdates({ ...available, latest: { ...available.latest, minimumFrom: '0.1.0' } })
  await screen.findByText('0.2.0 is available')
  expect(screen.queryByText(/Direct updates need/)).toBeNull()
})

test('keeps two commands with the same label', async () => {
  await openUpdates({ ...available, commands: [{ label: 'Run', text: 'docker compose pull' }, { label: 'Run', text: 'docker compose up -d' }] })
  expect(await screen.findByText('docker compose pull')).toBeTruthy()
  expect(screen.getByText('docker compose up -d')).toBeTruthy()
})

test('shows the server message when the check request fails', async () => {
  await openUpdates(upToDate, { 'POST /v1/owner/updates/check': () => apiError(503, 'unavailable', 'The server could not complete that action and no change was committed.') })
  await fireEvent.click(await screen.findByRole('button', { name: 'Check now' }))
  expect((await screen.findByRole('alert')).textContent).toContain('no change was committed')
})

test('says updates are not set up and offers no toggle', async () => {
  await openUpdates({ ...upToDate, configured: false, enabled: null, latest: null, checkedAt: null, error: 'not_configured' })
  expect(await screen.findByText('Updates are not set up for this build.')).toBeTruthy()
  expect(screen.queryByRole('checkbox')).toBeNull()
  expect(screen.queryByRole('button', { name: 'Turn on update checks' })).toBeNull()
  expect(screen.getByText('Version 0.1.0')).toBeTruthy()
})

test.each([
  ['feed_unreachable', 'The update feed could not be reached.'],
  ['feed_invalid', 'The update feed could not be verified.'],
  ['feed_rollback', 'The update feed offered an older release, so it was ignored.'],
  ['feed_expired', 'The update feed has expired.'],
  ['not_configured', 'Updates are not set up for this build.'],
  ['something_new', 'The update check failed.'],
])('turns the %s error code into one sentence', async (code, sentence) => {
  await openUpdates({ ...upToDate, latest: null, checkedAt: null, error: code })
  expect(await screen.findByText(sentence)).toBeTruthy()
  expect(document.body.textContent).not.toContain(code)
})

test('shows the banner on every page and links to Updates', async () => {
  openApp('/', { 'GET /v1/owner/system': () => system(true) })
  const link = await screen.findByRole('link', { name: 'View update' })
  expect(link.closest('.banner')?.textContent).toContain('An update is available.')
  expect(link.getAttribute('href')).toBe('#/updates')
  await fireEvent.click(screen.getByRole('link', { name: 'Members' }))
  await screen.findByRole('heading', { name: 'Members' })
  expect(screen.getByRole('link', { name: 'View update' })).toBeTruthy()
  await fireEvent.click(screen.getByRole('link', { name: 'System' }))
  await screen.findByRole('heading', { name: 'System' })
  expect(screen.getByRole('link', { name: 'View update' })).toBeTruthy()
})

test('hides the banner on the Updates page itself', async () => {
  openApp('/', { 'GET /v1/owner/system': () => system(true), 'GET /v1/owner/updates': () => available })
  await screen.findByRole('link', { name: 'View update' })
  await fireEvent.click(screen.getByRole('link', { name: 'View update' }))
  await screen.findByText('0.2.0 is available')
  expect(screen.queryByRole('link', { name: 'View update' })).toBeNull()
})

test('dismisses the banner for the session without storing anything', async () => {
  openApp('/', { 'GET /v1/owner/system': () => system(true) })
  await fireEvent.click(await screen.findByRole('button', { name: 'Dismiss' }))
  expect(screen.queryByRole('link', { name: 'View update' })).toBeNull()
  await fireEvent.click(screen.getByRole('link', { name: 'Members' }))
  await screen.findByRole('heading', { name: 'Members' })
  expect(screen.queryByRole('link', { name: 'View update' })).toBeNull()
  expect(JSON.stringify({ ...window.localStorage }) + JSON.stringify({ ...window.sessionStorage })).toBe('{}{}')
})

test('shows no banner when no update is available', async () => {
  openApp('/')
  await screen.findByRole('heading', { name: 'Devices' })
  expect(screen.queryByRole('link', { name: 'View update' })).toBeNull()
})

test('shows no banner when the system request fails', async () => {
  openApp('/', { 'GET /v1/owner/system': () => apiError(503, 'unavailable', 'The server could not complete that action and no change was committed.') })
  await screen.findByRole('heading', { name: 'Devices' })
  expect(screen.queryByRole('link', { name: 'View update' })).toBeNull()
})

test('does not repeat the update toggle on the Settings page', async () => {
  openApp('/')
  await fireEvent.click(await screen.findByRole('link', { name: 'Settings' }))
  await screen.findByLabelText('Instance name')
  expect(screen.queryByRole('checkbox')).toBeNull()
})
