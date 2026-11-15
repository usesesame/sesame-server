import { readFileSync } from 'node:fs'
import path from 'node:path'
import { expect, test } from 'vitest'

const css = readFileSync(path.join(process.cwd(), 'src', 'app.css'), 'utf8')

function sheet() {
  const style = document.createElement('style')
  style.textContent = css
  document.head.append(style)
  const result = style.sheet as CSSStyleSheet
  style.remove()
  return result
}

function declarations(rules: CSSRuleList, selector: string) {
  return [...rules]
    .filter((rule): rule is CSSStyleRule => rule instanceof CSSStyleRule)
    .filter((rule) => rule.selectorText.split(',').map((part) => part.trim()).includes(selector))
    .map((rule) => rule.style)
}

const phone = () => [...sheet().cssRules].filter((rule): rule is CSSMediaRule => rule instanceof CSSMediaRule && rule.conditionText === '(max-width: 500px)')

test('keeps the desktop flex bases on toolbar inputs', () => {
  const rules = sheet().cssRules
  expect(declarations(rules, '.toolbar .search').map((style) => style.getPropertyValue('flex-basis'))).toContain('220px')
  expect(declarations(rules, '.toolbar > input:not(.search)').map((style) => style.getPropertyValue('flex-basis'))).toContain('240px')
})

test('resets the toolbar input flex basis when the toolbar becomes a column on phones', () => {
  const media = phone()
  expect(media).toHaveLength(1)
  const rules = media[0].cssRules
  expect(declarations(rules, '.toolbar').map((style) => style.getPropertyValue('flex-direction'))).toContain('column')
  for (const selector of ['.toolbar .search', '.toolbar > input:not(.search)']) {
    const styles = declarations(rules, selector)
    expect(styles.length).toBeGreaterThan(0)
    const last = styles[styles.length - 1]
    expect(last.getPropertyValue('flex-basis')).toBe('auto')
    expect(last.getPropertyValue('flex-grow')).toBe('0')
  }
})
