<script lang="ts">
  import { onMount, tick } from 'svelte'
  import AccountPage from './pages/AccountPage.svelte'
  import AccountFlowPage from './pages/AccountFlowPage.svelte'
  import AuthPage from './pages/AuthPage.svelte'
  import LegalPage from './pages/LegalPage.svelte'
  import SupportPage from './pages/SupportPage.svelte'
  import SupportRequestPage from './pages/SupportRequestPage.svelte'
  import PortalSidebar from './lib/PortalSidebar.svelte'
  import PageHeader from './lib/PageHeader.svelte'
  import { loadAuthState, getAccountBootstrap, signOut, type Account, type AuthState } from './lib/auth'
  import { accountPath, accountTabFromPath, accountTabLabel, normalizePath, type AccountTab } from './lib/account-routes'
  import { siteOrigin } from './lib/runtime-config'

  const FLOW_PAGES = ['forgot-password', 'reset-password', 'verify-email', 'confirm-email-change'] as const
  const LEGAL_PAGES = ['terms', 'privacy', 'security'] as const
  type FlowPage = (typeof FLOW_PAGES)[number]
  type LegalPageName = (typeof LEGAL_PAGES)[number]
  type Page = 'account' | 'login' | 'register' | 'support' | 'support-request' | FlowPage | LegalPageName | 'not-found'

  export let initialPath = typeof window === 'undefined' ? '/account' : window.location.pathname

  const route = normalizePath(initialPath)
  const initialHash = typeof window === 'undefined' ? '' : window.location.hash
  const initialTab = accountTabFromPath(route, initialHash)
  const page: Page = initialTab
    ? 'account'
    : route === '/support/request'
      ? 'support-request'
      : (['login', 'register', 'support', ...FLOW_PAGES, ...LEGAL_PAGES] as string[]).includes(route.slice(1))
        ? (route.slice(1) as Page)
        : 'not-found'
  const isFlow = (value: Page): value is FlowPage => (FLOW_PAGES as readonly string[]).includes(value)
  const isLegal = (value: Page): value is LegalPageName => (LEGAL_PAGES as readonly string[]).includes(value)

  const focusedPage = page === 'login' || page === 'register' || isFlow(page)
  const needsSessionCheck = page === 'account' || page === 'support' || page === 'support-request' || page === 'login'
  let account: Account | null = null
  let authState: AuthState = needsSessionCheck ? { state: 'loading' } : { state: 'anonymous' }
  let supportUnread = 0
  let sessionExpired = false
  let accountTab: AccountTab = initialTab ?? 'overview'
  let signingOut = false
  let signOutError = ''
  const siteHost = new URL(siteOrigin).host
  $: serviceNotice = authState.state === 'offline' ? 'You appear to be offline. Reconnect and try again.' : authState.state === 'error' ? authState.error.message : ''
  $: signedIn = Boolean(account) && (authState.state === 'authenticated' || authState.state === 'offline')
  $: shell = page === 'account' || ((page === 'support' || page === 'support-request') && signedIn)
  $: currentDestination = page === 'account' ? accountTab : page === 'support' || page === 'support-request' ? 'support' : ''
  $: if (page === 'account' && typeof document !== 'undefined') document.title = `${accountTabLabel(accountTab)}, Sesame account`

  onMount(() => {
    if (page === 'account' && initialHash === '#security' && accountTabFromPath(route) === 'overview') {
      window.history.replaceState(window.history.state, '', accountPath('security'))
    }
    const popstate = () => {
      const next = accountTabFromPath(window.location.pathname, window.location.hash)
      if (page === 'account' && next) accountTab = next
    }
    window.addEventListener('popstate', popstate)
    if (needsSessionCheck) {
      void loadAuthState().then((state) => {
        const hasAccount = state.state === 'authenticated' || (state.state === 'offline' && Boolean(state.account))
        if (page === 'account' && !hasAccount) {
          window.location.replace(state.state === 'anonymous' && state.expired ? '/login?expired=1' : '/login')
          return
        }
        authState = state
        account = state.state === 'authenticated'
          ? state.account
          : state.state === 'offline'
            ? state.account || null
            : null
        sessionExpired = (state.state === 'anonymous' && state.expired === true) || (page === 'login' && new URLSearchParams(window.location.search).get('expired') === '1')
        if (state.state === 'authenticated' || (state.state === 'offline' && state.account)) {
          void getAccountBootstrap()
            .then((bootstrap) => { supportUnread = bootstrap.notificationCounts.support })
            .catch(() => { supportUnread = 0 })
        }
      })
    }
    return () => window.removeEventListener('popstate', popstate)
  })

  async function goToTab(next: AccountTab, focusTitle = true) {
    const path = accountPath(next)
    if (normalizePath(window.location.pathname) !== path) window.history.pushState(window.history.state, '', path)
    accountTab = next
    await tick()
    document.documentElement.scrollTop = 0
    if (focusTitle) document.getElementById('page-title')?.focus()
  }

  function navigate(event: MouseEvent, path: string, fromMenu: boolean) {
    if (page !== 'account' || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    const next = accountTabFromPath(path)
    if (!next) return
    event.preventDefault()
    void goToTab(next, !fromMenu)
  }

  async function leave() {
    signingOut = true
    signOutError = ''
    try {
      await signOut()
      account = null
      authState = { state: 'anonymous' }
      window.location.assign('/')
    } catch (reason) {
      signOutError = reason instanceof Error ? reason.message : 'Sign out is temporarily unavailable.'
    } finally {
      signingOut = false
    }
  }
</script>

<a class="skip-link" href="#top">Skip to content</a>

<div class={shell ? 'layout portal' : 'layout public'}>
  {#if shell}
    <PortalSidebar
      current={currentDestination}
      email={account?.email ?? ''}
      {supportUnread}
      {signingOut}
      {signOutError}
      onNavigate={navigate}
      onSignOut={leave}
    />
  {:else}
    <header class="public-bar">
      <a class="public-brand" href="/account">
        <img class="brand-mark" src="/favicon.svg" alt="" />
        <span class="brand-words"><strong>Sesame</strong><span class="brand-portal">Account</span></span>
      </a>
      <div class="public-actions">
        <a class="public-site" href={siteOrigin}>{siteHost}</a>
        {#if !focusedPage}
          {#if authState.state === 'error' || authState.state === 'offline'}
            <span class="public-status" role="status">Account service unavailable</span>
          {:else if authState.state === 'anonymous'}
            <a class="button button-secondary button-sm" href="/login">Sign in</a>
          {:else if authState.state === 'loading'}
            <span class="header-session-loader" role="status" aria-label="Checking account session"><span class="session-spinner" aria-hidden="true"></span></span>
          {/if}
        {/if}
      </div>
    </header>
  {/if}

  <main id="top" class={shell ? 'portal-main' : 'public-main'} tabindex="-1">
    {#if page === 'account'}
      <AccountPage
        {account}
        {authState}
        tab={accountTab}
        onSelectTab={(next) => void goToTab(next)}
        onSignedOut={() => {
          account = null
          authState = { state: 'anonymous' }
        }}
      />
    {:else if page === 'login' || page === 'register'}
      <AuthPage
        mode={page}
        sessionExpired={page === 'login' && sessionExpired}
        unavailable={page === 'login' ? serviceNotice : ''}
        onAuthenticated={(nextAccount) => { account = nextAccount; sessionExpired = false }}
      />
    {:else if isFlow(page)}
      <AccountFlowPage
        mode={page}
        onAuthenticated={(nextAccount) => {
          account = nextAccount
          authState = { state: 'authenticated', account: nextAccount }
        }}
      />
    {:else if page === 'support'}
      <SupportPage {account} />
    {:else if page === 'support-request'}
      <SupportRequestPage {account} authLoading={authState.state === 'loading'} />
    {:else if isLegal(page)}
      <LegalPage document={page} />
    {:else}
      <section class="public-page">
        <PageHeader title="Page not found" />
        <p class="page-lede">That address is not part of the account portal. <a href="/account">Go to your account</a>.</p>
      </section>
    {/if}
  </main>

  {#if !shell}
    <footer class="public-foot">
      <span>&copy; 2026 Sesame</span>
      <a href={`${siteOrigin}/privacy`}>Privacy</a>
      <a href={`${siteOrigin}/terms`}>Terms</a>
      <a href={`${siteOrigin}/security`}>Security</a>
      <a href="/support">Support</a>
    </footer>
  {/if}
</div>
