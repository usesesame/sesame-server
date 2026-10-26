import { afterEach, beforeEach, expect, test } from 'vitest'
import type { SupportCategory, SupportTicketSummary } from '../src/lib/support'
import { clearSupportDraft, loadSupportDraft, matchHelp, relatedOpenTickets, saveSupportDraft, type HelpEntry } from '../src/lib/support-help'

const entries: HelpEntry[] = [
  { id: 'reset', categories: ['account'], keywords: ['forgot password', 'reset password', 'cannot sign in', "can't sign in"], title: 'Reset your account password', answer: 'Use the recovery page.', link: { label: 'Recover your account', path: '/forgot-password' } },
  { id: 'email', categories: ['account'], keywords: ['verification email', 'verify email', 'no email', 'spam'], title: 'No verification email', answer: 'Check spam.' },
  { id: 'sync', categories: ['sync'], keywords: ['sync', 'devices', 'revision'], title: 'About Sync', answer: 'Sync is closed.' },
  { id: 'general', categories: ['general', 'account', 'sync', 'bug', 'import', 'browser_helper', 'billing'], keywords: ['lost master password'], title: 'Master password', answer: 'Support cannot unlock a vault.' },
]

test('a typed phrase finds the answer that names it', () => {
  expect(matchHelp(entries, 'general', 'I forgot password for my account').map((entry) => entry.id)).toEqual(['reset'])
})

test('the chosen topic ranks its own answers ahead of the rest', () => {
  expect(matchHelp(entries, 'sync', 'my devices show a different revision').map((entry) => entry.id)).toEqual(['sync'])
  expect(matchHelp(entries, 'account', 'I have no email and cannot sign in').map((entry) => entry.id)).toEqual(['reset', 'email'])
})

test('matching ignores case and punctuation and a curly apostrophe', () => {
  expect(matchHelp(entries, 'account', "CAN’T SIGN IN!!").map((entry) => entry.id)).toEqual(['reset'])
})

test('nothing is offered for short or unrelated text, so the form is not buried', () => {
  expect(matchHelp(entries, 'general', 'help')).toEqual([])
  expect(matchHelp(entries, 'general', 'the colour of the button is odd')).toEqual([])
  expect(matchHelp(entries, 'general', '')).toEqual([])
})

test('at most three answers are offered, best first', () => {
  const many: HelpEntry[] = Array.from({ length: 6 }, (_, index) => ({ id: `e${index}`, categories: ['general'] as SupportCategory[], keywords: ['crash', ...Array(index).fill('crash')], title: `E${index}`, answer: 'x' }))
  expect(matchHelp(many, 'general', 'crash').length).toBeLessThanOrEqual(3)
})

test('an answer is not offered twice and a keyword counts once however often it is typed', () => {
  const ids = matchHelp(entries, 'account', 'forgot password forgot password forgot password').map((entry) => entry.id)
  expect(ids).toEqual(['reset'])
})

function ticket(overrides: Partial<SupportTicketSummary>): SupportTicketSummary {
  return { id: 't1', subject: 'Sync keeps failing', status: 'open', category: 'sync', appVersion: '', diagnosticCode: '', browserIntegration: '', requestId: '', messageCount: 1, unreadCount: 0, createdAt: '2026-10-01T10:00:00Z', updatedAt: '2026-10-20T10:00:00Z', autoClosed: false, canClose: true, canReopen: false, ...overrides }
}

test('an open request on the same topic is pointed out, newest first, and closed ones are not', () => {
  const found = relatedOpenTickets([ticket({ id: 'old', updatedAt: '2026-09-01T10:00:00Z' }), ticket({ id: 'new' }), ticket({ id: 'done', status: 'closed' }), ticket({ id: 'other', category: 'billing' })], 'sync')
  expect(found.map((item) => item.id)).toEqual(['new', 'old'])
})

test('a request waiting for the person counts as open too', () => {
  expect(relatedOpenTickets([ticket({ status: 'waiting' }), ticket({ id: 'review', status: 'in_progress' })], 'sync')).toHaveLength(2)
})

test('the general topic points at nothing, because it says nothing about what the request is', () => {
  expect(relatedOpenTickets([ticket({ category: 'general' })], 'general')).toEqual([])
})

beforeEach(() => sessionStorage.clear())
afterEach(() => sessionStorage.clear())

test('a half written request comes back after a reload and is cleared when it is sent', () => {
  saveSupportDraft({ category: 'sync', subject: 'Devices disagree', message: 'Two of my devices show different logins after I changed the vault key.' })
  expect(loadSupportDraft()).toEqual({ category: 'sync', subject: 'Devices disagree', message: 'Two of my devices show different logins after I changed the vault key.' })
  clearSupportDraft()
  expect(loadSupportDraft()).toBeNull()
})

test('an empty draft is not kept, and secret shaped text is never written to storage', () => {
  saveSupportDraft({ category: 'general', subject: '', message: '   ' })
  expect(loadSupportDraft()).toBeNull()
  saveSupportDraft({ category: 'account', subject: 'Help', message: 'my recovery kit is F9K4P-7XQ2M-T6V8C-H3R5W-J8L2N' })
  expect(loadSupportDraft()).toBeNull()
  expect(JSON.stringify({ ...sessionStorage })).not.toContain('F9K4P')
})

test('a damaged or oversized stored draft is ignored', () => {
  sessionStorage.setItem('sesame-support-draft-v1', '{not json')
  expect(loadSupportDraft()).toBeNull()
  sessionStorage.setItem('sesame-support-draft-v1', JSON.stringify({ category: 'nonsense', subject: 'x', message: 'y' }))
  expect(loadSupportDraft()?.category).toBe('general')
  sessionStorage.setItem('sesame-support-draft-v1', JSON.stringify({ category: 'sync', subject: 'a'.repeat(500), message: 'b'.repeat(9000) }))
  const loaded = loadSupportDraft()
  expect(loaded?.subject.length).toBeLessThanOrEqual(120)
  expect(loaded?.message.length).toBeLessThanOrEqual(4000)
})
