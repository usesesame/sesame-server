import { stepUp } from './owner'
import { stepUpAction, type StepUpRequest } from './stepup-action'

export const stepUpDialog = $state({ open: false, busy: false, error: '', action: 'continue' })

let waiting: ((renewed: boolean) => void)[] = []

export function requestStepUp(request?: StepUpRequest): Promise<boolean> {
  if (waiting.length === 0) {
    stepUpDialog.action = request ? stepUpAction(request) : 'continue'
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
