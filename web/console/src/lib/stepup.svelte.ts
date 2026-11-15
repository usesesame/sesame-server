import { stepUp } from './owner'
import { stepUpAction, type StepUpRequest } from './stepup-action'

type Waiter = { action: string; resolve: (renewed: boolean) => void }

export const stepUpDialog = $state({ open: false, busy: false, error: '', actions: [] as string[] })

let waiting: Waiter[] = []

function track(next: Waiter[]) {
  waiting = next
  stepUpDialog.actions = next.map((waiter) => waiter.action)
}

export function requestStepUp(request?: StepUpRequest): Promise<boolean> {
  const action = request ? stepUpAction(request) : 'continue'
  if (waiting.length === 0) {
    stepUpDialog.error = ''
    stepUpDialog.busy = false
    stepUpDialog.open = true
  }
  return new Promise((resolve) => { track([...waiting, { action, resolve }]) })
}

function settle(renewed: boolean, settled: Waiter[]) {
  track(waiting.filter((waiter) => !settled.includes(waiter)))
  stepUpDialog.busy = false
  if (waiting.length === 0) stepUpDialog.open = false
  settled.forEach((waiter) => waiter.resolve(renewed))
}

export function cancelStepUp() {
  settle(false, waiting)
}

export async function submitStepUp(password: string, code: string): Promise<boolean> {
  if (stepUpDialog.busy) return false
  const named = waiting
  stepUpDialog.busy = true
  stepUpDialog.error = ''
  try {
    await stepUp({ password, code })
  } catch (reason) {
    stepUpDialog.error = reason instanceof Error ? reason.message : 'The request failed.'
    stepUpDialog.busy = false
    return false
  }
  settle(true, named)
  return true
}
