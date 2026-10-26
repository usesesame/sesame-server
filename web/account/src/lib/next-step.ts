export type AccountTab = 'overview' | 'security' | 'sessions' | 'devices' | 'activity'

export interface NextStepInput {
  email: string
  emailVerified: boolean
  betaGranted: boolean
  desktopLinkReady: boolean
  connectedDesktops: number
}

export type NextStepAction =
  | { kind: 'verify-email'; label: string }
  | { kind: 'tab'; tab: AccountTab; label: string }
  | { kind: 'link'; path: string; label: string }

export interface NextStep {
  id: 'verify-email' | 'connect-desktop' | 'linking-unavailable' | 'request-access'
  title: string
  body: string
  action?: NextStepAction
  secondary?: { label: string; path: string }
}

export function nextStep(input: NextStepInput): NextStep | null {
  if (!input.emailVerified) {
    return {
      id: 'verify-email',
      title: 'Verify your email',
      body: `Open the link we sent to ${input.email} to unlock linking a desktop. It may take a few minutes to arrive.`,
      action: { kind: 'verify-email', label: 'Send verification email' },
    }
  }
  if (!input.betaGranted) {
    return {
      id: 'request-access',
      title: 'Ask for beta access',
      body: 'This account does not have beta access yet. Send a message from the support page if you expected it.',
      action: { kind: 'link', path: '/support/request', label: 'Ask for access' },
    }
  }
  if (input.connectedDesktops > 0) return null
  if (!input.desktopLinkReady) {
    return {
      id: 'linking-unavailable',
      title: 'Connecting a desktop is not available right now',
      body: 'Linking is turned off or could not be checked. Reload in a moment, or ask support if it stays this way.',
    }
  }
  return {
    id: 'connect-desktop',
    title: 'Connect Sesame on your computer',
    body: 'Install Sesame, then create a one-time code here and enter it in Sesame Settings. Your vault stays on your computer.',
    action: { kind: 'tab', tab: 'devices', label: 'Create a link code' },
    secondary: { label: 'Download Sesame', path: '/releases' },
  }
}
