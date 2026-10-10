import { stepUp } from './owner'

export const stepUpDialog = $state({ open: false, busy: false, error: '' })

let waiting: ((renewed: boolean) => void)[] = []

export function requestStepUp(): Promise<boolean> {
  if (waiting.length === 0) {
    stepUpDialog.error = ''
    stepUpDialog.busy = false
    stepUpDialog.open = true
  }
  return new Promise((resolve) => { waiting.push(resolve) })
}

function finish(renewed: boolean) {
  stepUpDialog.open = false
  stepUpDialog.busy = false
  const resolvers = waiting
  waiting = []
  resolvers.forEach((resolve) => resolve(renewed))
}

export function cancelStepUp() {
  finish(false)
}

export async function submitStepUp(password: string, code: string) {
  if (stepUpDialog.busy) return
  stepUpDialog.busy = true
  stepUpDialog.error = ''
  try {
    await stepUp({ password, code })
    finish(true)
  } catch (reason) {
    stepUpDialog.error = reason instanceof Error ? reason.message : 'The request failed.'
    stepUpDialog.busy = false
  }
}
