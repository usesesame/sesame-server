import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, test } from 'vitest'

const css = readFileSync(resolve(process.cwd(), 'src/app.css'), 'utf8')

function phoneRules(): string {
  const start = css.indexOf('@media (max-width: 620px) {')
  expect(start).toBeGreaterThan(-1)
  let depth = 0
  for (let index = css.indexOf('{', start); index < css.length; index += 1) {
    if (css[index] === '{') depth += 1
    if (css[index] === '}') depth -= 1
    if (depth === 0) return css.slice(start, index + 1)
  }
  throw new Error('The phone rules are not closed.')
}

test('text fields are 16px on a phone, so iOS does not zoom the page when one is focused', () => {
  expect(phoneRules()).toMatch(/:is\(input, select, textarea\):not\(\[type="checkbox"\]\):not\(\[type="radio"\]\)\s*\{[^}]*font-size:\s*16px/)
})

test('navigation, tabs, footer links and the links in forms are at least 44px tall on a phone', () => {
  const rules = phoneRules()
  for (const selector of ['.button, .button.button-sm, .danger-button', '.brand', '.site-header nav a', '.account-tabs button', '.footer-col a', '.footer-compact-inner a', '.auth-switch a', '.label-row a', '.row-link']) {
    const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    expect(rules, selector).toMatch(new RegExp(`${escaped}[^{]*\\{[^}]*min-height:\\s*44px`))
  }
})

test('the phone header drops the truncated email and the site address link', () => {
  expect(phoneRules()).toMatch(/\.header-account-email[^{]*\{[^}]*display:\s*none/)
  expect(phoneRules()).toMatch(/\.nav-site[^{]*\{[^}]*display:\s*none/)
})

test('the account tabs scroll in one row on a phone instead of leaving one tab alone on a second row', () => {
  expect(phoneRules()).toMatch(/\.account-tabs\s*\{[^}]*flex-wrap:\s*nowrap[^}]*overflow-x:\s*auto/)
})
