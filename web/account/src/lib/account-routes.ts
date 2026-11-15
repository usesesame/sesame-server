import type { AccountTab } from './next-step'

export type { AccountTab }

export type AccountDestination = { id: AccountTab; label: string; path: string }

export const ACCOUNT_TABS: readonly AccountDestination[] = [
  { id: 'overview', label: 'Overview', path: '/account' },
  { id: 'security', label: 'Security', path: '/account/security' },
  { id: 'sessions', label: 'Sessions', path: '/account/sessions' },
  { id: 'devices', label: 'Desktops', path: '/account/desktops' },
  { id: 'activity', label: 'Activity', path: '/account/activity' },
]

export function normalizePath(path: string): string {
  return path.replace(/\/+$/, '') || '/'
}

export function accountTabFromPath(path: string, hash = ''): AccountTab | null {
  const route = normalizePath(path)
  if (route === '/' || route === '/account') return hash === '#security' ? 'security' : 'overview'
  return ACCOUNT_TABS.find((item) => item.path === route)?.id ?? null
}

export function accountPath(tab: AccountTab): string {
  return ACCOUNT_TABS.find((item) => item.id === tab)?.path ?? '/account'
}

export function accountTabLabel(tab: AccountTab): string {
  return ACCOUNT_TABS.find((item) => item.id === tab)?.label ?? 'Overview'
}
