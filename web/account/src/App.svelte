<script lang="ts">
  import { onMount } from 'svelte'
  import AccountPage from './pages/AccountPage.svelte'
  import AccountFlowPage from './pages/AccountFlowPage.svelte'
  import AuthPage from './pages/AuthPage.svelte'
  import SupportPage from './pages/SupportPage.svelte'
  import { loadAuthState, type Account, type AuthState } from './lib/auth'
  import { siteOrigin } from './lib/runtime-config'

  const FLOW_PAGES = ['forgot-password', 'reset-password', 'verify-email', 'confirm-email-change'] as const
  type FlowPage = (typeof FLOW_PAGES)[number]
  type Page = 'account' | 'login' | 'register' | 'support' | FlowPage | 'not-found'

  export let initialPath = typeof window === 'undefined' ? '/account' : window.location.pathname

  const route = initialPath.replace(/\/+$/, '') || '/'
  const page: Page = route === '/' || route === '/account'
    ? 'account'
    : (['login', 'register', 'support', ...FLOW_PAGES] as string[]).includes(route.slice(1))
      ? (route.slice(1) as Page)
      : 'not-found'
  const isFlow = (value: Page): value is FlowPage => (FLOW_PAGES as readonly string[]).includes(value)

  const needsSessionCheck = page === 'account' || page === 'support'
  let account: Account | null = null
  let authState: AuthState = needsSessionCheck ? { state: 'loading' } : { state: 'anonymous' }
  const siteHost = new URL(siteOrigin).host

  onMount(() => {
    if (!needsSessionCheck) return
    void loadAuthState().then((state) => {
      authState = state
      account = state.state === 'authenticated'
        ? state.account
        : state.state === 'offline'
          ? state.account || null
          : null
    })
  })
</script>

<a class="skip-link" href="#top">Skip to content</a>

<header class="site-header">
  <div class="header-inner">
    <a class="brand" href="/account">
      <span class="brand-mark" aria-hidden="true">S</span>
      <span>Sesame <span class="brand-portal">account</span></span>
    </a>
    <nav aria-label="Portal navigation">
      <a href="/account" aria-current={page === 'account' ? 'page' : undefined}>Account</a>
      <a href="/support" aria-current={page === 'support' ? 'page' : undefined}>Support</a>
      <a href={siteOrigin}>{siteHost}</a>
    </nav>
    <div class="header-account-actions">
      {#if authState.state === 'authenticated' || (authState.state === 'offline' && account)}
        <span class="header-account-email" role="status">{account?.email}</span>
      {:else if authState.state === 'error' || authState.state === 'offline'}
        <span class="header-account-status" role="status">Account service unavailable</span>
      {:else if authState.state === 'anonymous' && page !== 'login'}
        <a class="button button-sm" href="/login">Sign in</a>
      {:else if authState.state === 'loading'}
        <span class="header-session-loader" role="status" aria-label="Checking account session"><span class="session-spinner" aria-hidden="true"></span></span>
      {/if}
    </div>
  </div>
</header>

<main id="top">
  {#if page === 'login' || page === 'register'}
    <AuthPage mode={page} onAuthenticated={(nextAccount) => (account = nextAccount)} />
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
  {:else if page === 'account'}
    <AccountPage
      {account}
      {authState}
      onSignedOut={() => {
        account = null
        authState = { state: 'anonymous' }
      }}
    />
  {:else}
    <section class="compact-page-hero">
      <h1>Page not found.</h1>
      <p class="intro">That address is not part of the account portal. <a href="/account">Go to your account</a>.</p>
    </section>
  {/if}
</main>

<footer class="site-footer">
  <div class="footer-inner">
    <p class="footer-note">
      Sesame never receives your vault. This portal manages only the optional website account.
    </p>
    <nav aria-label="Site links">
      <a href={`${siteOrigin}/privacy`}>Privacy</a>
      <a href={`${siteOrigin}/terms`}>Terms</a>
      <a href={`${siteOrigin}/security`}>Security</a>
      <a href={siteOrigin}>{siteHost}</a>
    </nav>
  </div>
</footer>

<style>
  .brand-portal {
    color: var(--text-muted);
  }

  .header-account-email {
    color: var(--text-muted);
    font-size: 0.9rem;
    max-width: 22ch;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
