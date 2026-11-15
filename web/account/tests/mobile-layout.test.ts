import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, test } from 'vitest'

const css = readFileSync(resolve(process.cwd(), 'src/app.css'), 'utf8')

function phoneRules(): string {
  const start = css.indexOf('@media (max-width: 860px) {')
  expect(start).toBeGreaterThan(-1)
  let depth = 0
  for (let index = css.indexOf('{', start); index < css.length; index += 1) {
    if (css[index] === '{') depth += 1
    if (css[index] === '}') depth -= 1
    if (depth === 0) return css.slice(start, index + 1)
  }
  throw new Error('The phone rules are not closed.')
}

function phoneRule(selector: string) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const match = phoneRules().match(new RegExp(`(?:^|\\n)\\s*${escaped}\\s*\\{([^}]*)\\}`))
  expect(match, selector).not.toBeNull()
  return match?.[1] ?? ''
}

test('the tap size is 44px', () => {
  expect(css).toMatch(/--portal-tap:\s*2\.75rem;/)
})

test('text fields are 16px on a phone, so iOS does not zoom the page when one is focused', () => {
  expect(phoneRules()).toMatch(/:is\(input, select, textarea\):not\(\[type="checkbox"\]\):not\(\[type="radio"\]\)\s*\{[^}]*font-size:\s*16px/)
})

test('buttons, the menu, its links and the links in forms are at least 44px tall on a phone', () => {
  const rules = phoneRules()
  for (const selector of ['.button.button-sm', '.public-brand', '.public-site', '.public-foot a', '.auth-switch a', '.label-row a']) {
    const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    expect(rules, selector).toMatch(new RegExp(`${escaped}[^{]*\\{[^}]*min-height:\\s*var\\(--portal-tap\\)`))
  }
  for (const selector of ['.menu-button', '.sidebar-nav a', '.sidebar-brand', '.sidebar-origin', '.support-ticket-list > button']) {
    expect(phoneRule(selector), selector).toMatch(/min-height:\s*var\(--portal-tap\)/)
  }
})

test('on a phone the sidebar becomes a top bar and its links open as a sheet with a scrim', () => {
  expect(css).toMatch(/\.menu-button\s*\{\s*display:\s*none;/)
  expect(phoneRule('.menu-button')).toMatch(/display:\s*inline-flex/)
  expect(phoneRule('.sidebar-menu')).toMatch(/display:\s*none/)
  expect(phoneRule('.sidebar-menu')).toMatch(/position:\s*fixed/)
  expect(phoneRule('.sidebar.open .sidebar-menu')).toMatch(/display:\s*flex/)
  expect(phoneRule('.menu-scrim')).toMatch(/background:\s*var\(--backdrop\)/)
  expect(phoneRule('.layout.portal')).toMatch(/display:\s*block/)
})

test('two-column parts of the pages fold to one column on a phone', () => {
  const rules = phoneRules()
  expect(rules).toMatch(/\.support-workspace,\s*\.support-columns,\s*\.support-request-section,\s*\.support-advanced-fields\s*\{\s*grid-template-columns:\s*minmax\(0, 1fr\)/)
  expect(phoneRule('.spec-rows > div')).toMatch(/grid-template-columns:\s*minmax\(0, 1fr\)/)
  expect(phoneRule('.inline-form')).toMatch(/grid-template-columns:\s*minmax\(0, 1fr\)/)
})
