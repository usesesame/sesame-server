import { expect, test } from 'vitest'
import { HELP_ENTRIES, CATEGORY_GUIDANCE } from '../src/lib/support-answers'
import { matchHelp } from '../src/lib/support-help'
import { SUPPORT_CATEGORIES } from '../src/lib/support'

const everyText = () => [
  ...HELP_ENTRIES.flatMap((entry) => [entry.title, entry.answer, entry.link?.label ?? '']),
  ...Object.values(CATEGORY_GUIDANCE).flatMap((item) => [item.placeholder, ...item.include]),
]

test('every answer is complete, has its own id and keywords, and links to a real kind of address', () => {
  expect(new Set(HELP_ENTRIES.map((entry) => entry.id)).size).toBe(HELP_ENTRIES.length)
  for (const entry of HELP_ENTRIES) {
    expect(entry.title.length, entry.id).toBeGreaterThan(4)
    expect(entry.answer.length, entry.id).toBeGreaterThan(20)
    expect(entry.keywords.length, entry.id).toBeGreaterThan(1)
    expect(entry.categories.length, entry.id).toBeGreaterThan(0)
    if (entry.link) expect(entry.link.path.startsWith('/'), entry.id).toBe(true)
  }
})

test('the wording follows the house rules: no dashes as punctuation, no curly quotes, no parentheses in sentences', () => {
  for (const text of everyText()) {
    expect(text).not.toMatch(/[—–]/)
    expect(text).not.toMatch(/[“”‘’]/)
    expect(text).not.toMatch(/\(/)
  }
})

test('no answer claims security, an audit or post quantum safety, which nobody independent has checked', () => {
  for (const text of everyText()) {
    expect(text).not.toMatch(/\b(secure|audited|production ready|quantum)\b/i)
  }
})

test('the answer about an unlockable vault says never to send the secrets', () => {
  const unlock = HELP_ENTRIES.find((entry) => entry.id === 'unlock-vault')
  expect(unlock?.answer).toMatch(/never send your master password or recovery kit/i)
})

test('every topic has guidance, and it says what to include and what to ask', () => {
  for (const option of SUPPORT_CATEGORIES) {
    expect(CATEGORY_GUIDANCE[option.value].include.length, option.value).toBeGreaterThan(1)
    expect(CATEGORY_GUIDANCE[option.value].placeholder.endsWith('?') || CATEGORY_GUIDANCE[option.value].placeholder.endsWith('.'), option.value).toBe(true)
  }
})

test.each([
  ['account', 'I forgot my password and I cannot sign in', 'reset-account-password'],
  ['account', 'The verification email did not arrive', 'verification-email'],
  ['account', 'My link code expired before I entered it', 'desktop-link-code'],
  ['general', 'I forgot my master password, can you unlock my vault', 'unlock-vault'],
  ['sync', 'Sync not working between my two computers', 'sync-availability'],
  ['import', 'Import failed with a CSV from Bitwarden', 'import-formats'],
  ['browser_helper', 'The browser extension is not working on my site', 'browser-connection'],
  ['general', 'How do I delete my account', 'delete-account'],
  ['general', 'Is there a mobile app', 'mobile-app'],
])('%s: "%s" finds %s first', (category, text, id) => {
  expect(matchHelp(HELP_ENTRIES, category as never, text)[0]?.id).toBe(id)
})

test('text that is just a complaint about something else finds nothing', () => {
  expect(matchHelp(HELP_ENTRIES, 'bug', 'The window flickers when I resize it on my monitor')).toEqual([])
  expect(matchHelp(HELP_ENTRIES, 'general', 'Please add a dark theme for the lock screen')).toEqual([])
})
