import { fireEvent, screen, waitFor } from '@testing-library/svelte'
import { expect, test, vi } from 'vitest'
import { apiError, device, json, member } from './support/fake-api'
import { openApp, useCleanApp } from './support/app'

useCleanApp()

const stepUpNeeded = () => apiError(403, 'step_up_required', 'Confirm your password and a current code to continue.')

test('lists devices with their holder, platform and version', async () => {
  openApp('/')
  expect(await screen.findByText('Studio desktop')).toBeTruthy()
  expect(screen.getByText(/Alex Rivera · Windows 11 x86_64 · Sesame 0.3.1/)).toBeTruthy()
  expect(screen.getByRole('link', { name: 'Add a device' }).getAttribute('href')).toBe('#/pairing')
})

test('shows an empty state and an error state for devices', async () => {
  openApp('/', { 'GET /v1/owner/devices': () => ({ devices: [] }) })
  expect(await screen.findByText(/No devices yet/)).toBeTruthy()
})

test('shows the server message when devices cannot be loaded', async () => {
  openApp('/', { 'GET /v1/owner/devices': () => apiError(503, 'unavailable', 'The server could not complete that action and no change was committed.') })
  expect((await screen.findByRole('alert')).textContent).toContain('no change was committed')
})

test('revokes a device after confirmation, with step-up when asked', async () => {
  let revoked = false
  let attempts = 0
  const { calls } = openApp('/', {
    'GET /v1/owner/devices': () => ({ devices: revoked ? [] : [device] }),
    'DELETE /v1/owner/devices/dev-1': () => { if (++attempts === 1) return stepUpNeeded(); revoked = true; return undefined },
    'POST /v1/owner/step-up': () => json({ ok: true }),
  })
  await fireEvent.click(await screen.findByRole('button', { name: 'Revoke' }))
  expect(calls.some((call) => call.method === 'DELETE')).toBe(false)
  await fireEvent.click(screen.getByRole('button', { name: 'Revoke device' }))
  const dialog = await screen.findByRole('dialog')
  await fireEvent.input(dialog.querySelector('input[type=password]')!, { target: { value: 'fictional password for tests' } })
  await fireEvent.input(dialog.querySelector('input[inputmode=numeric]')!, { target: { value: '123456' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
  expect(await screen.findByText('Studio desktop was revoked.')).toBeTruthy()
  expect(calls.filter((call) => call.method === 'DELETE')).toHaveLength(2)
  expect(await screen.findByText(/No devices yet/)).toBeTruthy()
})

test('keeps a device when the owner backs out of revoking', async () => {
  const { calls } = openApp('/')
  await fireEvent.click(await screen.findByRole('button', { name: 'Revoke' }))
  await fireEvent.click(screen.getByRole('button', { name: 'Keep' }))
  expect(screen.getByRole('button', { name: 'Revoke' })).toBeTruthy()
  expect(calls.some((call) => call.method === 'DELETE')).toBe(false)
})

test('adds, renames and removes members and reports the revoked devices', async () => {
  let members = [member]
  const { calls } = openApp('/', {
    'GET /v1/owner/members': () => ({ members }),
    'POST /v1/owner/members': (call) => { const created = { id: 'mem-2', name: (call.body as { name: string }).name, createdAt: '2026-10-10T00:00:00Z', deviceCount: 0 }; members = [...members, created]; return json({ member: created }, 201) },
    'PATCH /v1/owner/members/mem-1': (call) => { members = [{ ...members[0], name: (call.body as { name: string }).name }, ...members.slice(1)]; return { member: members[0] } },
    'DELETE /v1/owner/members/mem-1': () => { members = members.slice(1); return { revokedDevices: 2 } },
  })
  await fireEvent.click(await screen.findByRole('link', { name: 'Members' }))
  expect(await screen.findByText('Priya Raman')).toBeTruthy()
  expect(screen.getByText(/2 devices · added/)).toBeTruthy()

  await fireEvent.input(screen.getByLabelText('Name'), { target: { value: 'Tomas Berg' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Add member' }))
  expect(await screen.findByText('Tomas Berg was added.')).toBeTruthy()
  expect(calls.find((call) => call.method === 'POST' && call.path === '/v1/owner/members')!.body).toEqual({ name: 'Tomas Berg' })

  await fireEvent.click(screen.getAllByRole('button', { name: 'Rename' })[0])
  await fireEvent.input(screen.getByLabelText('Member name'), { target: { value: 'Priya R.' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  expect(await screen.findByText('Priya R.')).toBeTruthy()

  await fireEvent.click(screen.getAllByRole('button', { name: 'Remove' })[0])
  expect(screen.getByText('This revokes 2 devices.')).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: 'Remove member' }))
  expect(await screen.findByText('Priya R. was removed and 2 devices were revoked.')).toBeTruthy()
})

test('opens pairing for the chosen member', async () => {
  openApp('/')
  await fireEvent.click(await screen.findByRole('link', { name: 'Members' }))
  await fireEvent.click(await screen.findByRole('button', { name: 'Add a device' }))
  expect(await screen.findByRole('heading', { name: 'Pairing' })).toBeTruthy()
  await screen.findByRole('option', { name: member.name })
  await waitFor(() => expect((screen.getByLabelText('Connect a device for') as HTMLSelectElement).value).toBe(member.id))
})

test('shows a duplicate member name error from the server', async () => {
  openApp('/', { 'POST /v1/owner/members': () => apiError(409, 'conflict', 'That name or record is already in use.') })
  await fireEvent.click(await screen.findByRole('link', { name: 'Members' }))
  await fireEvent.input(await screen.findByLabelText('Name'), { target: { value: 'Priya Raman' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Add member' }))
  expect((await screen.findByRole('alert')).textContent).toBe('That name or record is already in use.')
})

test('does not offer to remove the last active owner', async () => {
  openApp('/')
  await fireEvent.click(await screen.findByRole('link', { name: 'Owners' }))
  expect(await screen.findByText('The last active owner cannot be removed.')).toBeTruthy()
  expect((screen.getByRole('button', { name: 'Remove' }) as HTMLButtonElement).disabled).toBe(true)
})

test('removes another owner and signs out when the owner removes their own account', async () => {
  const owners = [
    { id: 'own-1', name: 'Alex Rivera', createdAt: '2026-07-01T00:00:00Z', lastLoginAt: '2026-10-09T09:00:00Z', setupPending: false, current: true },
    { id: 'own-2', name: 'Sam Okafor', createdAt: '2026-07-02T00:00:00Z', lastLoginAt: null, setupPending: false, current: false },
  ]
  let list = owners
  const { calls } = openApp('/', {
    'GET /v1/owner/owners': () => ({ owners: list }),
    'DELETE /v1/owner/owners/own-2': () => { list = [owners[0]]; return undefined },
    'DELETE /v1/owner/owners/own-1': () => undefined,
  })
  await fireEvent.click(await screen.findByRole('link', { name: 'Owners' }))
  await screen.findByText('Sam Okafor')
  await fireEvent.click(screen.getAllByRole('button', { name: 'Remove' })[1])
  await fireEvent.click(screen.getByRole('button', { name: 'Remove owner' }))
  expect(await screen.findByText('Sam Okafor was removed and signed out.')).toBeTruthy()
  expect(calls.some((call) => call.path === '/v1/owner/owners/own-2' && call.method === 'DELETE')).toBe(true)

  list = owners
  await fireEvent.click(screen.getByRole('link', { name: 'System' }))
  await screen.findByRole('heading', { name: 'System' })
  await fireEvent.click(screen.getByRole('link', { name: 'Owners' }))
  await screen.findByText('Sam Okafor')
  await fireEvent.click(screen.getAllByRole('button', { name: 'Remove' })[0])
  expect(screen.getByText('You will be signed out.')).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: 'Remove owner' }))
  expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeTruthy()
  expect(screen.getByText('You removed your own owner account.')).toBeTruthy()
})

test('saves only the fields that changed and shows the fingerprint', async () => {
  const { calls } = openApp('/', {
    'PATCH /v1/owner/settings': (call) => ({ instanceId: 'inst-1', name: (call.body as { name: string }).name, publicUrl: 'https://sesame.example.test', publicUrlSet: true, createdAt: '2026-07-01T00:00:00Z', fingerprint: 'ab12cd34' }),
  })
  await fireEvent.click(await screen.findByRole('link', { name: 'Settings' }))
  const save = (await screen.findByRole('button', { name: 'Save settings' })) as HTMLButtonElement
  await waitFor(() => expect((screen.getByLabelText('Instance name') as HTMLInputElement).value).toBe('Rivera household'))
  expect(save.disabled).toBe(true)
  expect(screen.getByText('ab12cd34')).toBeTruthy()
  await fireEvent.input(screen.getByLabelText('Instance name'), { target: { value: 'Home server' } })
  await fireEvent.click(save)
  expect(await screen.findByText('Settings saved.')).toBeTruthy()
  expect(calls.find((call) => call.method === 'PATCH' && call.path === '/v1/owner/settings')!.body).toEqual({ name: 'Home server' })
  expect(screen.getByText(/^Home server/, { selector: '.signed-in span' })).toBeTruthy()
})

test('shows the server message when the public URL does not match', async () => {
  openApp('/', { 'PATCH /v1/owner/settings': () => apiError(400, 'public_url_mismatch', 'This server answers at https://sesame.example.test. Change SESAME_PUBLIC_URL and restart the server to use another address.') })
  await fireEvent.click(await screen.findByRole('link', { name: 'Settings' }))
  await waitFor(() => expect((screen.getByLabelText(/^Public URL/) as HTMLInputElement).value).toBe('https://sesame.example.test'))
  await fireEvent.input(screen.getByLabelText(/^Public URL/), { target: { value: 'https://other.example.test' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Save settings' }))
  expect((await screen.findByRole('alert')).textContent).toContain('Change SESAME_PUBLIC_URL')
})

const entry = (seq: number) => ({ seq, actor: 'owner:own-1', action: 'pairing.created', target: 'member:mem-1', detail: { device: 'Priya laptop' }, at: '2026-10-09T10:00:00Z', hash: 'h' })

test('shows the audit chain as intact and loads older entries', async () => {
  const { calls } = openApp('/', {
    'GET /v1/owner/audit': (call) => (call.path.includes('cursor=2')
      ? { entries: [entry(1)], nextCursor: 0, chain: { ok: true, rows: 2, headSeq: 2, headHash: '', firstBreak: null } }
      : { entries: [entry(3), entry(2)], nextCursor: 2, chain: { ok: true, rows: 3, headSeq: 3, headHash: '', firstBreak: null } }),
  })
  await fireEvent.click(await screen.findByRole('link', { name: 'Audit log' }))
  expect(await screen.findByText(/Chain intact, 3 entries checked/)).toBeTruthy()
  expect(await screen.findAllByText('pairing.created')).toHaveLength(2)
  expect(screen.getAllByText(/owner:own-1 · member:mem-1 · device: Priya laptop/)).toHaveLength(2)
  await fireEvent.click(screen.getByRole('button', { name: 'Show older entries' }))
  await waitFor(() => expect(screen.getAllByText('pairing.created')).toHaveLength(3))
  expect(screen.queryByRole('button', { name: 'Show older entries' })).toBeNull()
  expect(calls.some((call) => call.path === '/v1/owner/audit?cursor=2')).toBe(true)
})

test('shows a broken audit chain with the first broken entry', async () => {
  openApp('/', { 'GET /v1/owner/audit': () => ({ entries: [entry(3)], nextCursor: 0, chain: { ok: false, rows: 3, headSeq: 3, headHash: '', firstBreak: { seq: 2, reason: 'the stored hash does not match' } } }) })
  await fireEvent.click(await screen.findByRole('link', { name: 'Audit log' }))
  const status = await screen.findByText(/Chain broken at entry 2/)
  expect(status.closest('.chain')?.getAttribute('data-ok')).toBe('false')
  expect(screen.getByText('The stored hash does not match.')).toBeTruthy()
})

test('shows an empty audit log', async () => {
  openApp('/')
  await fireEvent.click(await screen.findByRole('link', { name: 'Audit log' }))
  expect(await screen.findByText('No entries yet.')).toBeTruthy()
})

test('shows system facts and warnings', async () => {
  openApp('/', { 'GET /v1/owner/system': () => ({ version: '0.1.0', commit: '3f9a1c2d8e4b', schemaVersion: 3, databaseBytes: 1_482_752, lastBackupAt: null, warnings: ['No backup has been made yet.'], auditChainOk: false }) })
  await fireEvent.click(await screen.findByRole('link', { name: 'System' }))
  expect(await screen.findByText('0.1.0, commit 3f9a1c2')).toBeTruthy()
  expect(screen.getByText('1.4 MB')).toBeTruthy()
  expect(screen.getByText('No backup yet')).toBeTruthy()
  expect(screen.getByText('Broken')).toBeTruthy()
  expect(screen.getByText('No backup has been made yet.')).toBeTruthy()
})

test('exports after step-up and downloads a JSON file without logging or storing it', async () => {
  let attempts = 0
  const urls: Blob[] = []
  Object.defineProperty(URL, 'createObjectURL', { value: (blob: Blob) => { urls.push(blob); return 'blob:console-test' }, configurable: true })
  Object.defineProperty(URL, 'revokeObjectURL', { value: () => undefined, configurable: true })
  const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
  const { calls } = openApp('/', {
    'POST /v1/owner/export': () => (++attempts === 1 ? stepUpNeeded() : { format: 'sesame-selfhost-export-v1', members: [member], devices: [device], audit: { entries: [] } }),
    'POST /v1/owner/step-up': () => json({ ok: true }),
  })
  await fireEvent.click(await screen.findByRole('link', { name: 'System' }))
  await fireEvent.click(await screen.findByRole('button', { name: 'Export data' }))
  const dialog = await screen.findByRole('dialog')
  await fireEvent.input(dialog.querySelector('input[type=password]')!, { target: { value: 'fictional password for tests' } })
  await fireEvent.input(dialog.querySelector('input[inputmode=numeric]')!, { target: { value: '123456' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
  expect(await screen.findByText('The export was downloaded.')).toBeTruthy()
  expect(click).toHaveBeenCalledTimes(1)
  expect(JSON.parse(await urls[0].text()).format).toBe('sesame-selfhost-export-v1')
  expect(calls.filter((call) => call.path === '/v1/owner/export')).toHaveLength(2)
  expect(JSON.stringify({ ...window.localStorage })).not.toContain('Priya')
  click.mockRestore()
})

test('shows the server message when the export fails', async () => {
  openApp('/', { 'POST /v1/owner/export': () => apiError(503, 'unavailable', 'The server could not complete that action and no change was committed.') })
  await fireEvent.click(await screen.findByRole('link', { name: 'System' }))
  await fireEvent.click(await screen.findByRole('button', { name: 'Export data' }))
  expect((await screen.findByRole('alert')).textContent).toContain('no change was committed')
})

test('shows no hosted-only screens', async () => {
  openApp('/')
  await screen.findByRole('heading', { name: 'Devices' })
  const labels = screen.getAllByRole('link').map((link) => link.textContent)
  expect(labels).toEqual(['Devices', 'Members', 'Pairing', 'Audit log', 'Owners', 'Settings', 'Updates', 'System', 'Add a device'])
})
