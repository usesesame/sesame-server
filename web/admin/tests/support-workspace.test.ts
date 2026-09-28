import { cleanup, fireEvent, render, screen, within } from '@testing-library/svelte'
import { afterEach, expect, test, vi } from 'vitest'
import type { AdminAccount, EmailDeliveryReason, TicketDetail, TicketSummary } from '../src/lib/types'

const api = vi.hoisted(() => ({ request: vi.fn(), mutate: vi.fn() }))

vi.mock('../src/lib/api', () => {
  class APIError extends Error {
    constructor(message: string, public status: number, public code = '') { super(message) }
  }
  return { APIError, apiURL: 'https://api.test.invalid', request: api.request, mutate: api.mutate }
})

import App from '../src/App.svelte'
import { APIError } from '../src/lib/api'

function admin(overrides: Partial<AdminAccount> = {}): AdminAccount {
  return {
    id: 'ops',
    email: 'ops@example.invalid',
    role: 'support',
    mfaVerified: true,
    suspended: false,
    createdAt: '2026-01-01T00:00:00Z',
    permissions: ['support:read', 'support:manage'],
    ...overrides,
  }
}

function ticketSummary(overrides: Partial<TicketSummary> = {}): TicketSummary {
  return {
    id: 'ticket-test',
    email: 'tester@example.invalid',
    subject: 'Cannot sign in',
    status: 'open',
    priority: 'normal',
    category: 'account',
    appVersion: '0.1.0-beta.3',
    diagnosticCode: 'UI-7F2A',
    browserIntegration: 'ready',
    requestId: 'req-1234',
    messageCount: 2,
    createdAt: '2026-08-30T09:00:00Z',
    updatedAt: '2026-08-31T10:00:00Z',
    slaDueAt: '2026-08-31T11:00:00Z',
    slaBreached: false,
    ...overrides,
  }
}

function ticketDetail(overrides: Partial<TicketDetail> = {}): TicketDetail {
  return {
    ...ticketSummary(),
    messages: [
      { id: 'message-test', authorRole: 'user', body: 'I cannot sign in after the update.', sentViaEmail: false, createdAt: '2026-08-30T09:00:00Z' },
      { id: 'message-staff', authorRole: 'staff', adminEmail: 'ops@example.invalid', body: 'A fix is on the way.', sentViaEmail: true, emailDeliveryStatus: 'delivered', emailDeliveryReason: 'delivered', createdAt: '2026-08-31T10:30:00Z' },
    ],
    notes: [{ id: 'note-test', adminEmail: 'ops@example.invalid', body: 'Follow up with the import logs.', createdAt: '2026-08-31T10:45:00Z' }],
    ...overrides,
  }
}

function mockApp(routes: (path: string) => unknown, overrides: Partial<AdminAccount> = {}) {
  api.request.mockImplementation(async (path: string) => {
    if (path === '/v1/admin/auth/me') return { admin: admin(overrides) }
    if (path === '/v1/admin/overview') return { overview: {} }
    return routes(path)
  })
}

afterEach(() => {
  cleanup()
  api.request.mockReset()
  api.mutate.mockReset()
  vi.unstubAllGlobals()
})

test('loads the support queue with ticket state', async () => {
  mockApp((path) => {
    if (path.startsWith('/v1/admin/support/assignees')) return { assignees: [] }
    if (path.startsWith('/v1/admin/support?')) return { tickets: [ticketSummary()], total: 1 }
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Support' }))
  expect(await screen.findByText('Cannot sign in')).toBeTruthy()
  expect(api.request).toHaveBeenCalledWith('/v1/admin/support?size=100&page=1')
  expect(api.request).toHaveBeenCalledWith('/v1/admin/support/assignees')
  expect(screen.getByText('tester@example.invalid')).toBeTruthy()
  expect(within(screen.getByRole('table')).getByText('Account & sign-in')).toBeTruthy()
  expect(screen.getByText('2 messages')).toBeTruthy()
  expect(screen.getByText('1 tickets')).toBeTruthy()
  expect(screen.getByText(/^Due /)).toBeTruthy()
})

test('shows met and overdue first-response states', async () => {
  mockApp((path) => {
    if (path.startsWith('/v1/admin/support/assignees')) return { assignees: [] }
    if (path.startsWith('/v1/admin/support?')) {
      return {
        tickets: [
          ticketSummary({ id: 'ticket-met', subject: 'Import fails', firstResponseAt: '2026-08-31T10:00:00Z' }),
          ticketSummary({ id: 'ticket-overdue', subject: 'Sync question', slaBreached: true }),
        ],
        total: 2,
      }
    }
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Support' }))
  expect(await screen.findByText('Met')).toBeTruthy()
  expect(screen.getByText('Overdue')).toBeTruthy()
  expect(screen.queryByText(/^Due /)).toBeNull()
})

test('opens a ticket and shows the conversation, metadata, and notes', async () => {
  const detail = ticketDetail({
    accountId: 'account-test',
    linkedDevices: [{ id: 'device-test', name: 'Office PC', connectedAt: '2026-08-01T00:00:00Z', expiresAt: '2026-09-01T00:00:00Z' }],
  })
  mockApp((path) => {
    if (path.startsWith('/v1/admin/support/assignees')) return { assignees: [] }
    if (path.startsWith('/v1/admin/support?')) return { tickets: [ticketSummary()], total: 1 }
    if (path === '/v1/admin/support/ticket-test') return { ticket: detail }
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Support' }))
  await fireEvent.click(await screen.findByText('Cannot sign in'))
  expect(await screen.findByText('I cannot sign in after the update.')).toBeTruthy()
  expect(api.request).toHaveBeenCalledWith('/v1/admin/support/ticket-test')
  expect(screen.getByText('Linked desktop: Office PC')).toBeTruthy()
  expect(screen.getByText('A fix is on the way.')).toBeTruthy()
  expect(screen.getByText(/email delivered/)).toBeTruthy()
  expect(screen.getByText('Follow up with the import logs.')).toBeTruthy()
})

test('labels a guest request without an account', async () => {
  mockApp((path) => {
    if (path.startsWith('/v1/admin/support/assignees')) return { assignees: [] }
    if (path.startsWith('/v1/admin/support?')) return { tickets: [ticketSummary()], total: 1 }
    if (path === '/v1/admin/support/ticket-test') return { ticket: ticketDetail() }
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Support' }))
  await fireEvent.click(await screen.findByText('Cannot sign in'))
  expect(await screen.findByText('Guest request')).toBeTruthy()
})

test('posts a staff reply and keeps the returned ticket', async () => {
  const replied = ticketDetail({
    status: 'waiting',
    messages: [
      ...ticketDetail().messages,
      { id: 'message-reply', authorRole: 'staff', adminEmail: 'ops@example.invalid', body: 'A fix is on the way.', sentViaEmail: false, createdAt: '2026-08-31T11:00:00Z' },
    ],
  })
  mockApp((path) => {
    if (path.startsWith('/v1/admin/support/assignees')) return { assignees: [] }
    if (path.startsWith('/v1/admin/support?')) return { tickets: [ticketSummary()], total: 1 }
    if (path === '/v1/admin/support/ticket-test') return { ticket: replied }
    return {}
  })
  api.mutate.mockResolvedValue({ ticket: replied })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Support' }))
  await fireEvent.click(await screen.findByText('Cannot sign in'))
  await fireEvent.input(await screen.findByLabelText('Reply to user'), { target: { value: 'We reset the session for this account.' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Post reply' }))
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/support/ticket-test/reply', 'POST', { body: 'We reset the session for this account.' })
  expect(await screen.findByText('Reply added to the user\'s support portal.')).toBeTruthy()
  expect(screen.getByRole('status', { name: 'Support status' }).textContent).toBe('Notice: Reply added to the user\'s support portal.')
  expect((screen.getByLabelText('Reply to user') as HTMLTextAreaElement).value).toBe('')
})

test('adds an internal note', async () => {
  const note = { id: 'note-new', adminEmail: 'ops@example.invalid', body: 'Customer confirmed the fix.', createdAt: '2026-08-31T12:00:00Z' }
  mockApp((path) => {
    if (path.startsWith('/v1/admin/support/assignees')) return { assignees: [] }
    if (path.startsWith('/v1/admin/support?')) return { tickets: [ticketSummary()], total: 1 }
    if (path === '/v1/admin/support/ticket-test') return { ticket: ticketDetail() }
    return {}
  })
  api.mutate.mockResolvedValue({ note })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Support' }))
  await fireEvent.click(await screen.findByText('Cannot sign in'))
  await fireEvent.input(await screen.findByLabelText('Internal note'), { target: { value: 'Customer confirmed the fix.' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Add note' }))
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/support/ticket-test/notes', 'POST', { body: 'Customer confirmed the fix.' })
  expect(await screen.findByText('Internal note added.')).toBeTruthy()
  expect(screen.getByText('Customer confirmed the fix.')).toBeTruthy()
})

test('changes ticket status and priority', async () => {
  mockApp((path) => {
    if (path.startsWith('/v1/admin/support/assignees')) return { assignees: [] }
    if (path.startsWith('/v1/admin/support?')) return { tickets: [ticketSummary()], total: 1 }
    if (path === '/v1/admin/support/ticket-test') return { ticket: ticketDetail() }
    return {}
  })
  api.mutate.mockResolvedValue({})
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Support' }))
  await fireEvent.click(await screen.findByText('Cannot sign in'))
  await fireEvent.change(await screen.findByDisplayValue('Open'), { target: { value: 'closed' } })
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/support/ticket-test/status', 'POST', { status: 'closed' })
  expect(await screen.findByText('Status changed to closed.')).toBeTruthy()
  await fireEvent.change(screen.getByDisplayValue('Normal'), { target: { value: 'urgent' } })
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/support/ticket-test/priority', 'POST', { priority: 'urgent' })
  expect(await screen.findByText('Priority changed to urgent.')).toBeTruthy()
})

test('assigns a ticket through the assignee list', async () => {
  let assigned: string | undefined
  mockApp((path) => {
    if (path.startsWith('/v1/admin/support/assignees')) return { assignees: [{ id: 'admin-support', email: 'support@example.invalid' }] }
    if (path.startsWith('/v1/admin/support?')) return { tickets: [ticketSummary({ assignedAdminId: assigned })], total: 1 }
    if (path === '/v1/admin/support/ticket-test') return { ticket: ticketDetail({ assignedAdminId: assigned }) }
    return {}
  })
  api.mutate.mockImplementation(async (path: string) => {
    if (path.endsWith('/assign')) assigned = 'admin-support'
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Support' }))
  await fireEvent.click(await screen.findByText('Cannot sign in'))
  const select = await screen.findByLabelText('Assigned administrator')
  await fireEvent.change(select, { target: { value: 'admin-support' } })
  expect(api.mutate).toHaveBeenCalledWith('/v1/admin/support/ticket-test/assign', 'POST', { adminId: 'admin-support' })
  expect(await screen.findByText('Ticket assigned.')).toBeTruthy()
  const assignedTo = screen.getByText('Assigned to').closest('div') as HTMLElement
  expect(await within(assignedTo).findByText('support@example.invalid')).toBeTruthy()
})

test('sends the selected filters to the queue', async () => {
  mockApp((path) => {
    if (path.startsWith('/v1/admin/support/assignees')) return { assignees: [] }
    if (path.startsWith('/v1/admin/support?')) return { tickets: [], total: 0 }
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Support' }))
  await fireEvent.change(screen.getAllByRole('combobox')[0], { target: { value: 'open' } })
  expect(api.request).toHaveBeenCalledWith('/v1/admin/support?size=100&page=1&status=open')
  expect(await screen.findByText('No tickets match these filters.')).toBeTruthy()
})

test('readonly staff can read a ticket without staff controls', async () => {
  mockApp((path) => {
    if (path.startsWith('/v1/admin/support/assignees')) return { assignees: [] }
    if (path.startsWith('/v1/admin/support?')) return { tickets: [ticketSummary()], total: 1 }
    if (path === '/v1/admin/support/ticket-test') return { ticket: ticketDetail() }
    return {}
  }, { role: 'readonly', permissions: ['support:read'] })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Support' }))
  await fireEvent.click(await screen.findByText('Cannot sign in'))
  expect(await screen.findByText('I cannot sign in after the update.')).toBeTruthy()
  expect(screen.queryByLabelText('Reply to user')).toBeNull()
  expect(screen.queryByLabelText('Internal note')).toBeNull()
  expect(screen.queryByLabelText('Assigned administrator')).toBeNull()
  expect(screen.queryByRole('button', { name: 'Post reply' })).toBeNull()
})

test('explains every staff reply delivery reason without the raw outbox status', async () => {
  const reasons: Array<[EmailDeliveryReason, string]> = [
    ['delivered', 'Reply email delivered'],
    ['pending', 'Reply email waiting to send'],
    ['failed', 'Reply email failed'],
    ['guest', 'Guest request, no reply email'],
    ['mail-off', 'Mail is not configured, so no reply email was sent'],
    ['opted-out', 'Reply email turned off by the account'],
    ['not-queued', 'Reply email not queued'],
  ]
  const detail = ticketDetail({
    messages: [
      { id: 'message-user', authorRole: 'user', body: 'I cannot sign in after the update.', sentViaEmail: false, createdAt: '2026-08-30T09:00:00Z' },
      ...reasons.map(([reason]) => ({
        id: `message-${reason}`, authorRole: 'staff' as const, adminEmail: 'ops@example.invalid',
        body: `Fictional reply for ${reason}.`, sentViaEmail: true, emailDeliveryReason: reason, createdAt: '2026-08-31T10:30:00Z',
      })),
    ],
  })
  mockApp((path) => {
    if (path.startsWith('/v1/admin/support/assignees')) return { assignees: [] }
    if (path.startsWith('/v1/admin/support?')) return { tickets: [ticketSummary()], total: 1 }
    if (path === '/v1/admin/support/ticket-test') return { ticket: detail }
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Support' }))
  await fireEvent.click(await screen.findByText('Cannot sign in'))
  for (const [, label] of reasons) {
    expect(await screen.findByText(new RegExp(label.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))).toBeTruthy()
  }
  for (const status of ['delivered', 'pending', 'processing', 'failed']) {
    expect(screen.queryByText(new RegExp(`· email ${status}\\b`))).toBeNull()
  }
})

test('shows mail and staff notification state in the ticket header', async () => {
  mockApp((path) => {
    if (path.startsWith('/v1/admin/support/assignees')) return { assignees: [] }
    if (path.startsWith('/v1/admin/support?')) return { tickets: [ticketSummary()], total: 1 }
    if (path === '/v1/admin/support/ticket-test') return { ticket: ticketDetail(), mail: { deliveryConfigured: false, staffNotifyConfigured: true } }
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Support' }))
  await fireEvent.click(await screen.findByText('Cannot sign in'))
  expect(await screen.findByText('Requester receipt email: off, mail is not configured')).toBeTruthy()
  expect(screen.getByText('Staff notification email: on')).toBeTruthy()
})

test('gives ticket rows button semantics and activates them from the keyboard', async () => {
  mockApp((path) => {
    if (path.startsWith('/v1/admin/support/assignees')) return { assignees: [] }
    if (path.startsWith('/v1/admin/support?')) return { tickets: [ticketSummary()], total: 1 }
    if (path === '/v1/admin/support/ticket-test') return { ticket: ticketDetail() }
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Support' }))
  const row = await screen.findByRole('button', { name: 'Open ticket Cannot sign in from tester@example.invalid' })
  expect(row.getAttribute('tabindex')).toBe('0')
  const detailRequests = () => api.request.mock.calls.filter(([path]) => path === '/v1/admin/support/ticket-test').length
  await fireEvent.keyDown(row, { key: 'Enter' })
  expect(detailRequests()).toBe(1)
  expect(await screen.findByText('I cannot sign in after the update.')).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: 'Close details' }))
  await fireEvent.keyDown(screen.getByRole('button', { name: 'Open ticket Cannot sign in from tester@example.invalid' }), { key: ' ' })
  expect(detailRequests()).toBe(2)
  expect(await screen.findByText('I cannot sign in after the update.')).toBeTruthy()
})

test('announces ticket loading and loaded states to assistive technology', async () => {
  let resolveDetail: (value: unknown) => void = () => undefined
  const detailPromise = new Promise((resolve) => { resolveDetail = resolve })
  mockApp((path) => {
    if (path.startsWith('/v1/admin/support/assignees')) return { assignees: [] }
    if (path.startsWith('/v1/admin/support?')) return { tickets: [ticketSummary()], total: 1 }
    if (path === '/v1/admin/support/ticket-test') return detailPromise
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Support' }))
  await fireEvent.click(await screen.findByText('Cannot sign in'))
  expect(screen.getByRole('status', { name: 'Support status' }).textContent).toBe('Loading ticket details.')
  resolveDetail({ ticket: ticketDetail() })
  expect(await screen.findByText('I cannot sign in after the update.')).toBeTruthy()
  expect(screen.getByRole('status', { name: 'Support status' }).textContent).toBe('Ticket details loaded.')
})

test('shows the queue failure to the operator and announces it', async () => {
  mockApp((path) => {
    if (path.startsWith('/v1/admin/support/assignees')) return { assignees: [] }
    if (path.startsWith('/v1/admin/support?')) throw new APIError('The support queue is unavailable.', 503)
    return {}
  })
  render(App)
  await fireEvent.click(await screen.findByRole('button', { name: 'Support' }))
  expect((await screen.findByRole('alert')).textContent).toContain('The support queue is unavailable.')
  expect(screen.getByRole('status', { name: 'Support status' }).textContent).toBe('Error: The support queue is unavailable.')
})
