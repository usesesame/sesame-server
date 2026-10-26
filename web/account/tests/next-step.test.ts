import { expect, test } from 'vitest'
import { nextStep, type NextStepInput } from '../src/lib/next-step'

const ready: NextStepInput = {
  email: 'tester@example.invalid',
  emailVerified: true,
  betaGranted: true,
  desktopLinkReady: true,
  connectedDesktops: 0,
}

test('an address that is not verified comes first and names where the link went', () => {
  const step = nextStep({ ...ready, emailVerified: false, connectedDesktops: 0 })
  expect(step?.id).toBe('verify-email')
  expect(step?.body).toContain('tester@example.invalid')
  expect(step?.action).toEqual({ kind: 'verify-email', label: 'Send verification email' })
})

test('a verified account with beta access and no desktop is pointed at connecting one', () => {
  const step = nextStep(ready)
  expect(step?.id).toBe('connect-desktop')
  expect(step?.action).toEqual({ kind: 'tab', tab: 'devices', label: 'Create a link code' })
  expect(step?.secondary).toEqual({ label: 'Download Sesame', path: '/releases' })
})

test('connecting is not offered while linking is turned off, and the reason is given instead', () => {
  const step = nextStep({ ...ready, desktopLinkReady: false })
  expect(step?.id).toBe('linking-unavailable')
  expect(step?.action).toBeUndefined()
  expect(step?.body).toMatch(/turned off|not available/i)
})

test('an account without beta access is told how to ask, not to connect', () => {
  const step = nextStep({ ...ready, betaGranted: false })
  expect(step?.id).toBe('request-access')
  expect(step?.action).toEqual({ kind: 'link', path: '/support/request', label: 'Ask for access' })
})

test('an account that already has a desktop has nothing left to do', () => {
  expect(nextStep({ ...ready, connectedDesktops: 1 })).toBeNull()
  expect(nextStep({ ...ready, connectedDesktops: 3 })).toBeNull()
})

test('an unverified address wins over every other step', () => {
  expect(nextStep({ ...ready, emailVerified: false, betaGranted: false, desktopLinkReady: false })?.id).toBe('verify-email')
})
