import { screen, waitFor } from '@testing-library/svelte'
import { expect, test } from 'vitest'
import { openApp, useCleanApp } from './support/app'

useCleanApp()

function rect(left: number, width: number): DOMRect {
  return { left, right: left + width, top: 0, bottom: 44, width, height: 44, x: left, y: 0, toJSON: () => ({}) }
}

test('scrolls the selected link into the nav row when the window narrows after it was chosen', async () => {
  openApp('/#/system')
  expect(await screen.findByRole('heading', { name: 'System', level: 1 })).toBeTruthy()
  const nav = screen.getByRole('navigation', { name: 'Console' })
  const link = screen.getByRole('link', { name: 'System' })
  expect(link.getAttribute('aria-current')).toBe('page')
  let scrollLeft = 0
  Object.defineProperty(nav, 'scrollLeft', { configurable: true, get: () => scrollLeft, set: (value: number) => { scrollLeft = value } })
  Object.defineProperty(nav, 'scrollWidth', { configurable: true, get: () => 900 })
  Object.defineProperty(nav, 'clientWidth', { configurable: true, get: () => 390 })
  nav.getBoundingClientRect = () => rect(0, 390)
  link.getBoundingClientRect = () => rect(700 - scrollLeft, 60)
  expect(scrollLeft).toBe(0)
  window.dispatchEvent(new Event('resize'))
  await waitFor(() => expect(scrollLeft).toBe(684))
  window.dispatchEvent(new Event('resize'))
  await new Promise((resolve) => setTimeout(resolve, 20))
  expect(scrollLeft).toBe(684)
})
