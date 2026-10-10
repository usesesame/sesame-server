import { loadSystem } from './owner'

export const updateNotice = $state({ available: false, dismissed: false })

export async function refreshUpdateNotice() {
  try {
    updateNotice.available = (await loadSystem()).updateAvailable === true
  } catch {
    updateNotice.available = false
  }
}

export function resetUpdateNotice() {
  updateNotice.available = false
  updateNotice.dismissed = false
}
