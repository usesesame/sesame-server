import type { SupportCategory } from './support'
import type { HelpEntry } from './support-help'

export interface CategoryGuidance {
  include: string[]
  placeholder: string
}

export const CATEGORY_GUIDANCE: Record<SupportCategory, CategoryGuidance> = {
  general: {
    include: ['What you were trying to do', 'What happened instead', 'Your Sesame version and system, Windows or Linux'],
    placeholder: 'What were you trying to do, and what happened instead?',
  },
  account: {
    include: ['The email address on the account', 'Which page you were on', 'The exact message shown'],
    placeholder: 'Which page were you on, and what did it say?',
  },
  import: {
    include: ['Which app you exported from', 'The file type, such as CSV or JSON, and not the file itself', 'The message Sesame showed'],
    placeholder: 'Which app did you export from, and what did Sesame say when you imported it?',
  },
  sync: {
    include: ['Your Sesame version on each device', 'What the Sync panel says on each device', 'What you did just before it went wrong'],
    placeholder: 'Which devices are involved, and what does the Sync panel say on each?',
  },
  browser_helper: {
    include: ['Your browser and its version', 'What Settings, Connections says about the browser', 'The website you tried'],
    placeholder: 'Which browser and website, and what does Settings, Connections say?',
  },
  billing: {
    include: ['What you tried to buy or activate', 'The email address on the account'],
    placeholder: 'What did you try to buy or activate?',
  },
  bug: {
    include: ['The steps to make it happen again', 'What you expected', 'Your Sesame version and system'],
    placeholder: 'What did you do, what did you expect, and what happened?',
  },
}

export const HELP_ENTRIES: HelpEntry[] = [
  {
    id: 'reset-account-password',
    categories: ['account'],
    keywords: ['forgot password', 'forgot my password', 'reset password', 'password reset', 'cannot sign in', "can't sign in", 'locked out'],
    title: 'Choose a new account password',
    answer: 'The recovery page emails a link to the address on your account. Setting a new password signs out your other website sessions. This is your website account password, not your vault master password.',
    link: { label: 'Recover your account', path: '/forgot-password' },
  },
  {
    id: 'verification-email',
    categories: ['account'],
    keywords: ['verification email', 'verify email', 'verify my email', 'confirmation email', 'no email', 'email did not arrive', 'email has not arrived', 'email not arrived'],
    title: 'The verification email has not arrived',
    answer: 'Look in your spam folder and give it a few minutes. The link works for 24 hours. If you can sign in, the Overview page has a Send verification email button.',
    link: { label: 'Open your account', path: '/account' },
  },
  {
    id: 'desktop-link-code',
    categories: ['account', 'general'],
    keywords: ['link code', 'desktop link', 'link my desktop', 'link desktop', 'connect desktop', 'code expired', 'code does not work'],
    title: 'Linking a desktop with a code',
    answer: 'A link code is one time and works for 10 minutes. Create a new one on the Desktops tab of your account, then enter it in Sesame Settings. Linking needs a verified email and beta access.',
    link: { label: 'Open your account', path: '/account' },
  },
  {
    id: 'unlock-vault',
    categories: ['general', 'account', 'bug'],
    keywords: ['forgot master password', 'forgot my master password', 'lost master password', 'lost my master password', 'lost recovery kit', 'lost my recovery kit', 'cannot unlock', "can't unlock", 'unlock my vault', 'open my vault'],
    title: 'Support cannot unlock a vault',
    answer: 'Use your recovery kit, a PIN, or Windows Hello if you set one up. Without an unlock method the vault cannot be recovered. Never send your master password or recovery kit.',
  },
  {
    id: 'sync-availability',
    categories: ['sync', 'general'],
    keywords: ['sync not working', 'sync does not work', 'sync available', 'when is sync', 'sync not available', 'turn on sync', 'enable sync'],
    title: 'Sync is not on the hosted service yet',
    answer: 'The hosted Sesame service does not offer Sync yet. The roadmap lists what has to pass first.',
    link: { label: 'Read the roadmap', path: '/roadmap#sync', site: true },
  },
  {
    id: 'import-formats',
    categories: ['import'],
    keywords: ['import failed', 'import does not work', 'cannot import', "can't import", 'import csv', 'import json', 'import from bitwarden', 'import from 1password', 'migrate from'],
    title: 'Importing from another manager',
    answer: 'Sesame imports from Bitwarden, 1Password, major browsers, other password managers and authenticator apps. The import screen lists every format, so choose the one that matches your export. If an import stops, tell us which app you exported from and what Sesame said, and never send the file.',
  },
  {
    id: 'browser-connection',
    categories: ['browser_helper'],
    keywords: ['browser extension', 'browser helper', 'autofill not working', 'autofill does not work', 'extension not working', 'chrome extension', 'browser fill'],
    title: 'Checking the browser connection',
    answer: 'Open Sesame Settings, then Connections, and look at the browser connection. If it offers Repair connection, press it, then try the website again.',
  },
  {
    id: 'delete-account',
    categories: ['account', 'general'],
    keywords: ['delete account', 'delete my account', 'remove my account', 'close my account'],
    title: 'Deleting your website account',
    answer: 'The bottom of the Overview page has Delete account. It deletes your sessions, eligibility, licences and desktop links. Local vaults are untouched.',
    link: { label: 'Open your account', path: '/account' },
  },
  {
    id: 'mobile-app',
    categories: ['general'],
    keywords: ['mobile app', 'android app', 'ios app', 'iphone app', 'phone app'],
    title: 'There is no mobile app yet',
    answer: 'Sesame is a desktop app for now.',
  },
]
