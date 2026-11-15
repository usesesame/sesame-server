import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, test } from 'vitest'

const css = readFileSync(resolve(process.cwd(), 'src/app.css'), 'utf8')

function rule(selector: string) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const bodies = [...css.matchAll(new RegExp(`(?:^|\\n)\\s*${escaped}\\s*\\{([^}]*)\\}`, 'g'))].map((match) => match[1])
  expect(bodies.length, selector).toBeGreaterThan(0)
  return bodies.join(';')
}

test('the six button kinds each have a rest, hover and pressed rule, and disabled buttons look disabled', () => {
  expect(rule('.button:disabled')).toMatch(/opacity:\s*var\(--control-disabled\)/)
  for (const kind of ['.button', '.button-secondary', '.button-soft', '.button-text', '.button-danger', '.button-danger-text']) {
    rule(kind)
    rule(`${kind}:hover:not(:disabled)`)
    rule(`${kind}:active:not(:disabled)`)
  }
  expect(rule('.button-secondary')).toMatch(/border-color:\s*var\(--border-input\)/)
  expect(rule(':focus-visible')).toMatch(/outline:\s*var\(--focus-outline\)/)
})

test('text buttons have their own padding so hover, press and the focus ring share one shape', () => {
  expect(rule('.button-text')).toMatch(/padding:\s*0 var\(--portal-text-px\)/)
  expect(rule('.button-danger-text')).toMatch(/padding:\s*0 var\(--portal-text-px\)/)
  expect(rule('.button-text:hover:not(:disabled)')).toMatch(/background:/)
})

test('fields sit on the surface with a visible edge, and show hover, focus, error and disabled states', () => {
  const field = rule('input:not([type="checkbox"]):not([type="radio"]),\nselect,\ntextarea')
  expect(field).toMatch(/border:\s*1px solid var\(--border-input\)/)
  expect(field).toMatch(/background:\s*var\(--surface\)/)
  expect(field).toMatch(/font-weight:\s*var\(--weight-regular\)/)
  expect(css).toMatch(/:hover:not\(:focus\):not\(:disabled\):not\(\[readonly\]\)\s*\{\s*border-color:\s*var\(--text-faint\)/)
  expect(css).toMatch(/\):focus\s*\{[^}]*border-color:\s*var\(--focus-ring\)/)
  expect(css).toMatch(/\[aria-invalid="true"\]\s*\{\s*border-color:\s*var\(--danger\)/)
  expect(css).toMatch(/:is\(:disabled, \[readonly\]\)\s*\{[^}]*background:\s*var\(--bg-workspace\)/)
})

test('errors use the danger colour, codes the code font, and the password toggle the page font', () => {
  expect(rule('.inline-danger')).toMatch(/color:\s*var\(--danger\)/)
  expect(rule('.code-group')).toMatch(/font-family:\s*var\(--font-code\)/)
  expect(rule('.password-toggle')).toMatch(/font-family:\s*inherit/)
})

test('the gold bar is kept for the warning notice only', () => {
  const gold = [...css.matchAll(/([^{}]+)\{[^}]*inset 4px 0 0 var\(--gold\)/g)].map((match) => match[1].trim())
  expect(gold).toEqual(['.notice-warn'])
})

test('the unread count is a gold tag, and section titles use the UI font at the fourth type step', () => {
  expect(rule('.nav-count')).toMatch(/color:\s*var\(--gold-text\)/)
  const title = rule('.section-title')
  expect(title).toMatch(/font-family:\s*var\(--font-ui\)/)
  expect(title).toMatch(/font-size:\s*var\(--type-4\)/)
})

test('the stylesheet loads no data URLs, which the img-src policy would block, and uses only the token weights', () => {
  expect(css).not.toMatch(/url\(\s*["']?data:/)
  expect(css).not.toMatch(/font-weight:\s*\d/)
  expect(css).not.toMatch(/text-transform:\s*uppercase/)
  expect(css).not.toMatch(/gradient\(/)
})

test('portal-local values are defined once at the top of the stylesheet', () => {
  const top = css.slice(0, css.indexOf('}'))
  const local = [...css.matchAll(/--(portal-[a-z0-9-]+)\s*:/g)].map((match) => match[1])
  for (const name of new Set(local)) expect(top, name).toContain(`--${name}:`)
  const definedOutsideTop = [...css.slice(css.indexOf('}')).matchAll(/--(portal-[a-z0-9-]+)\s*:/g)].map((match) => match[1])
  expect(definedOutsideTop.every((name) => name === 'portal-shadow-secondary')).toBe(true)
})

test('the sidebar reaches the full height of the window', () => {
  expect(rule('.sidebar-inner')).toMatch(/position:\s*sticky[^;]*;[^}]*height:\s*100dvh/)
  expect(rule('.layout.portal')).toMatch(/min-height:\s*100dvh/)
})

test('the mono face is kept for what a person types, copies or compares', () => {
  const mono = [...css.matchAll(/([^{}]+)\{[^}]*font-family:\s*var\(--font-code\)/g)].map((match) => match[1].trim())
  expect(mono.sort()).toEqual(['.code-group', '.code-groups', '.version'])
  expect(rule('.page-fact')).toMatch(/font-family:\s*var\(--font-ui\)/)
  expect(rule('.page-fact-main')).toMatch(/font-size:\s*var\(--type-3\)[^}]*font-weight:\s*var\(--weight-medium\)/)
  expect(rule('.page-fact-detail')).toMatch(/color:\s*var\(--text-muted\)/)
})
