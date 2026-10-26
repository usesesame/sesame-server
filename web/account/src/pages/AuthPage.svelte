<script lang="ts">
  import { onDestroy, onMount, tick } from 'svelte'
  import { getRegistrationStatus, register, signIn, type Account, type RegistrationStatus } from '../lib/auth'
  import { LEGAL_VERSION } from '../lib/legal'
  import { conditionalPasskeysAvailable, passkeysSupported, signInWithPasskey } from '../lib/passkey'
  import PasswordField from '../lib/PasswordField.svelte'
  import { siteOrigin } from '../lib/runtime-config'

  export let mode: 'login' | 'register'
  export let sessionExpired = false
  export let unavailable = ''
  export let onAuthenticated: (account: Account) => void

  const MIN_PASSWORD_LENGTH = 12

  let email = ''
  let password = ''
  let showPassword = false
  let submitting = false
  let registrationLoading = mode === 'register'
  let registrationError = ''
  let registration: RegistrationStatus | null = null
  let registered = false
  let registeredEmail = ''
  let error = ''
  let legalAccepted = false
  let legalMissing = false
  let errorElement: HTMLElement | undefined
  let legalBox: HTMLInputElement | undefined

  const linkedInvite = mode === 'register' && typeof window !== 'undefined'
    ? (new URLSearchParams(window.location.search).get('invite') ?? '').trim()
    : ''
  let inviteCode = linkedInvite
  let inviteFromLink = linkedInvite !== ''

  $: isRegistering = mode === 'register'
  $: registrationAllowed = registration?.enabled === true
  $: passwordNote = password.length === 0
    ? `${MIN_PASSWORD_LENGTH} characters or more. A few unrelated words works well.`
    : password.length < MIN_PASSWORD_LENGTH
      ? `${password.length} of ${MIN_PASSWORD_LENGTH} characters`
      : 'Long enough'

  const canUsePasskey = passkeysSupported()
  let quietPasskeyAvailable = false
  let quietPasskey: AbortController | undefined

  async function loadRegistration() {
    registrationLoading = true
    registrationError = ''
    try {
      registration = await getRegistrationStatus()
    } catch (reason) {
      registration = null
      registrationError = reason instanceof Error ? reason.message : 'Registration status is temporarily unavailable.'
    } finally {
      registrationLoading = false
    }
  }

  function finishSignIn(account: Account) {
    onAuthenticated(account)
    window.location.assign('/account')
  }

  async function startQuietPasskey() {
    quietPasskey?.abort()
    const controller = new AbortController()
    quietPasskey = controller
    try {
      finishSignIn(await signInWithPasskey({ conditional: true, signal: controller.signal }))
    } catch {
      return
    }
  }

  onMount(() => {
    if (isRegistering) {
      void loadRegistration()
      return
    }
    if (!canUsePasskey) return
    void conditionalPasskeysAvailable().then((available) => {
      quietPasskeyAvailable = available
      if (available) void startQuietPasskey()
    })
  })

  onDestroy(() => quietPasskey?.abort())

  async function showError(message: string) {
    error = message
    await tick()
    errorElement?.focus()
  }

  async function signInWithAPasskey() {
    quietPasskey?.abort()
    submitting = true
    error = ''
    try {
      finishSignIn(await signInWithPasskey())
    } catch (reason) {
      await showError(reason instanceof Error ? reason.message : 'Passkey sign-in is temporarily unavailable.')
      if (quietPasskeyAvailable) void startQuietPasskey()
    } finally {
      submitting = false
    }
  }

  async function submit() {
    error = ''
    legalMissing = false
    if (isRegistering && !legalAccepted) {
      legalMissing = true
      await tick()
      legalBox?.focus()
      return
    }
    quietPasskey?.abort()
    submitting = true
    let failure = ''
    try {
      if (isRegistering) {
        await register(email, password, inviteCode.trim() || undefined, {
          termsAccepted: true,
          termsVersion: LEGAL_VERSION,
          privacyAcknowledged: true,
          privacyVersion: LEGAL_VERSION,
        })
        registeredEmail = email
        registered = true
        return
      }
      finishSignIn(await signIn(email, password))
    } catch (reason) {
      failure = reason instanceof Error ? reason.message : 'The account service is temporarily unavailable.'
    } finally {
      password = ''
      submitting = false
    }
    if (failure) await showError(failure)
    if (!isRegistering && quietPasskeyAvailable) void startQuietPasskey()
  }
</script>

<section class="auth-page">
  <div class="auth-column">
    {#if isRegistering && !registrationLoading && !registrationError && !registrationAllowed}
      <h1>Accounts are invite-only for now</h1>
      <div class="auth-form card registration-closed">
        <p>New accounts need an invitation link. If you have one, open it from the email it came in. To ask for early access, send a message from the support page.</p>
        <a class="button" href="/support/request">Ask for an invitation</a>
        <a class="button button-soft" href="/login">Back to sign in</a>
      </div>
    {:else if registered}
      <h1>Check your email</h1>
      <div class="auth-form card">
        <p>If <strong>{registeredEmail}</strong> can be registered, a verification link is on its way. Open it, then sign in. This page never says whether an address already has an account.</p>
        <p>Nothing yet? Look in your spam folder and wait a few minutes. If it still has not arrived, <a href="/support/request">ask support</a>.</p>
        <a class="button button-soft" href="/login">Back to sign in</a>
      </div>
    {:else}
      <h1>{isRegistering ? 'Create your account' : 'Sign in'}</h1>
      <p class="auth-lede">
        {isRegistering
          ? "Your account is for downloads, your devices and support. This page never asks for your vault's master password."
          : "Use your Sesame website account. This is not your vault's master password."}
      </p>
      <form class="auth-form card" on:submit|preventDefault={submit} aria-busy={registrationLoading || submitting}>
        {#if sessionExpired}<p class="auth-notice" role="status">Your session expired. Sign in again to continue.</p>{/if}
        {#if unavailable}<p class="auth-notice auth-notice-warn" role="alert">{unavailable} <button type="button" class="inline-action" on:click={() => window.location.reload()}>Try again</button></p>{/if}
        {#if registrationLoading}
          <p class="auth-loading"><span class="auth-spinner" aria-hidden="true"></span>Checking invitation access…</p>
        {:else if registrationError}
          <p class="auth-error" role="alert">{registrationError}</p>
          <button class="button button-soft" type="button" on:click={() => void loadRegistration()}>Try again</button>
        {:else}
          <div class="field">
            <label for="auth-email">Email</label>
            <input id="auth-email" type="email" bind:value={email} autocomplete={!isRegistering && quietPasskeyAvailable ? 'username webauthn' : 'email'} autocapitalize="none" spellcheck="false" inputmode="email" required maxlength="254" disabled={submitting} />
          </div>
          {#if isRegistering && registration?.requiresInvite}
            {#if inviteFromLink}
              <div class="invite-applied">
                <p><strong>Invitation applied</strong></p>
                <button type="button" class="inline-action" on:click={() => (inviteFromLink = false)} disabled={submitting}>Use another code</button>
              </div>
            {:else}
              <div class="field">
                <label for="auth-invite">Invitation code</label>
                <input id="auth-invite" type="text" bind:value={inviteCode} autocomplete="off" autocapitalize="characters" spellcheck="false" required maxlength="128" disabled={submitting} />
              </div>
            {/if}
          {/if}
          <PasswordField
            id="auth-password"
            label={isRegistering ? 'Create a password' : 'Password'}
            bind:value={password}
            bind:revealed={showPassword}
            autocomplete={isRegistering ? 'new-password' : 'current-password'}
            describedby={isRegistering ? 'auth-password-note' : undefined}
            minlength={isRegistering ? MIN_PASSWORD_LENGTH : undefined}
            disabled={submitting}
          >
            <svelte:fragment slot="aside">{#if !isRegistering}<a href="/forgot-password">Forgot password?</a>{/if}</svelte:fragment>
            <svelte:fragment slot="note">{#if isRegistering}<small id="auth-password-note" aria-live="polite">{passwordNote}</small>{/if}</svelte:fragment>
          </PasswordField>
          {#if isRegistering}
            <div class="field legal-field">
              <label class="legal-agreement" for="auth-legal">
                <input id="auth-legal" bind:this={legalBox} type="checkbox" bind:checked={legalAccepted} aria-describedby={legalMissing ? 'auth-legal-error' : undefined} aria-required="true" disabled={submitting} />
                <span>I agree to the <a href={`${siteOrigin}/terms`} target="_blank" rel="noopener noreferrer">Terms of Use</a> and acknowledge the <a href={`${siteOrigin}/privacy`} target="_blank" rel="noopener noreferrer">Privacy Policy</a>.</span>
              </label>
              {#if legalMissing}<p id="auth-legal-error" class="auth-error" role="alert">Accept the Terms of Use and acknowledge the Privacy Policy to continue.</p>{/if}
            </div>
          {/if}
          {#if error}<p class="auth-error" role="alert" tabindex="-1" bind:this={errorElement}>{error}</p>{/if}
          <button class="button auth-submit" type="submit" disabled={submitting} aria-busy={submitting}>
            {#if submitting}<span class="auth-spinner" aria-hidden="true"></span>{/if}
            {submitting ? isRegistering ? 'Creating account…' : 'Signing in…' : isRegistering ? 'Create account' : 'Sign in'}
          </button>
          {#if !isRegistering && canUsePasskey}
            <p class="auth-or" aria-hidden="true"><span>or</span></p>
            <button class="button button-soft auth-passkey" type="button" on:click={signInWithAPasskey} disabled={submitting}>Sign in with a passkey</button>
          {/if}
        {/if}
      </form>
      {#if !registrationLoading}
        <p class="auth-switch">
          {#if isRegistering}
            Already have an account? <a href="/login">Sign in</a>
          {:else}
            Have an invitation? <a href="/register">Create an account</a>
          {/if}
        </p>
      {/if}
    {/if}
  </div>
</section>
