<script lang="ts">
  import { onMount } from 'svelte'
  import { getRegistrationStatus, register, signIn, type Account, type RegistrationStatus } from '../lib/auth'
  import { LEGAL_VERSION } from '../lib/legal'
  import { passkeysSupported, signInWithPasskey } from '../lib/passkey'

  export let mode: 'login' | 'register'
  export let onAuthenticated: (account: Account) => void

  let email = ''
  let password = ''
  let inviteCode = ''
  let showPassword = false
  let submitting = false
  let registrationLoading = mode === 'register'
  let registration: RegistrationStatus | null = null
  let error = ''
  let legalAccepted = false

  $: isRegistering = mode === 'register'
  $: registrationAllowed = !isRegistering || registration?.enabled === true
  const canUsePasskey = passkeysSupported()

  onMount(async () => {
    if (!isRegistering) return
    try {
      registration = await getRegistrationStatus()
      inviteCode = new URLSearchParams(window.location.search).get('invite') || ''
    } catch (reason) {
      error = reason instanceof Error ? reason.message : 'Registration status is temporarily unavailable.'
    } finally {
      registrationLoading = false
    }
  })

  async function signInWithAPasskey() {
    submitting = true
    error = ''
    try {
      const account = await signInWithPasskey()
      onAuthenticated(account)
      window.location.assign('/account')
    } catch (reason) {
      error = reason instanceof Error ? reason.message : 'Passkey sign-in is temporarily unavailable.'
    } finally {
      submitting = false
    }
  }

  async function submit() {
    if (isRegistering && !legalAccepted) {
      error = 'Read and accept the Terms of Use and acknowledge the Privacy Policy to continue.'
      return
    }
    submitting = true
    error = ''
    try {
      const account = isRegistering
        ? await register(email, password, inviteCode.trim() || undefined, {
            termsAccepted: true,
            termsVersion: LEGAL_VERSION,
            privacyAcknowledged: true,
            privacyVersion: LEGAL_VERSION,
          })
        : await signIn(email, password)
      onAuthenticated(account)
      window.location.assign('/account')
    } catch (reason) {
      error = reason instanceof Error ? reason.message : 'The account service is temporarily unavailable.'
    } finally {
      password = ''
      submitting = false
    }
  }
</script>

<section class="auth-page">
  <div class="auth-copy">
    <h1>{isRegistering ? 'Join the private beta.' : 'Sign in.'}</h1>
    <p class="intro">This account manages beta access, verified private-beta downloads, licences, browser sessions, and connected desktops. It never stores or unlocks your vault.</p>
    <ul class="account-job-list" aria-label="What the account is for">
      <li>See builds you are eligible to download</li>
      <li>Manage licences and connected desktops</li>
      <li>Review and revoke website sessions</li>
    </ul>
  </div>

  {#if isRegistering && !registrationLoading && !registrationAllowed}
    <div class="auth-form card registration-closed">
      <h2>Accounts are invite-only.</h2>
      <p>There is no public waitlist yet. Existing testers can sign in; new invitations include a private registration link. To ask about early access, use the <a href="/support">support page</a>.</p>
      <a class="button button-soft" href="/login">Back to sign in</a>
    </div>
  {:else}
    <form class="auth-form card" on:submit|preventDefault={submit} aria-busy={registrationLoading || submitting}>
      <h2>{isRegistering ? 'Activate your invitation' : 'Website account'}</h2>
      <p>{isRegistering ? 'Create an account only if you have been invited to test a private-beta build.' : 'Use the password or passkey for this website account, not your vault password.'}</p>
      {#if registrationLoading}
        <p class="auth-loading"><span class="auth-spinner" aria-hidden="true"></span>Checking invitation access…</p>
      {:else}
        <label>Email<input type="email" bind:value={email} autocomplete="email" autocapitalize="none" spellcheck="false" required maxlength="254" disabled={submitting} /></label>
        {#if isRegistering && registration?.requiresInvite}
          <label>Invitation code<input type="text" bind:value={inviteCode} autocomplete="one-time-code" autocapitalize="characters" spellcheck="false" required maxlength="128" disabled={submitting} /></label>
        {/if}
        <label>Password
          <div class="password-field">
            <input type={showPassword ? 'text' : 'password'} bind:value={password} autocomplete={isRegistering ? 'new-password' : 'current-password'} required minlength="12" maxlength="1024" disabled={submitting} />
            <button type="button" class="password-toggle" on:click={() => (showPassword = !showPassword)} disabled={submitting} aria-pressed={showPassword}>{showPassword ? 'Hide' : 'Show'}</button>
          </div>
        </label>
        {#if isRegistering}<small>At least 12 characters. This protects the website account only.</small>{/if}
        {#if isRegistering}
          <label class="legal-agreement">
            <input type="checkbox" bind:checked={legalAccepted} required disabled={submitting} />
            <span>I agree to the <a href="/terms" target="_blank" rel="noreferrer">Terms of Use</a> and acknowledge the <a href="/privacy" target="_blank" rel="noreferrer">Privacy Policy</a>.</span>
          </label>
        {/if}
        {#if error}<p class="auth-error" role="alert">{error}</p>{/if}
        <button class="button auth-submit" type="submit" disabled={submitting || (isRegistering && !legalAccepted)} aria-busy={submitting}>
          {#if submitting}<span class="auth-spinner" aria-hidden="true"></span>{/if}
          {submitting ? isRegistering ? 'Creating account…' : 'Signing in…' : isRegistering ? 'Create beta account' : 'Sign in'}
        </button>
        {#if !isRegistering && canUsePasskey}
          <button class="button auth-passkey" type="button" on:click={signInWithAPasskey} disabled={submitting}>Sign in with a passkey</button>
        {/if}
        <div class="auth-switch">
          {#if isRegistering}
            <a href="/login">Sign in instead</a>
          {:else}
            <a href="/register">Have an invitation? Create an account</a>
            <a href="/forgot-password">Forgot your account password?</a>
          {/if}
        </div>
      {/if}
    </form>
  {/if}
</section>
