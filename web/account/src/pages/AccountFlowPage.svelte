<script lang="ts">
  import { onMount, tick } from 'svelte'
  import { SvelteURL } from 'svelte/reactivity'
  import PasswordField from '../lib/PasswordField.svelte'
  import PasswordSetupForm from '../lib/PasswordSetupForm.svelte'
  import {
    confirmEmailChange,
    confirmEmailVerification,
    confirmPasswordRecovery,
    requestPasswordRecovery,
    type Account,
  } from '../lib/auth'

  export let mode: 'forgot-password' | 'reset-password' | 'verify-email' | 'confirm-email-change'
  export let onAuthenticated: (account: Account) => void

  let email = ''
  let newPassword = ''
  let verifyPassword = ''
  let confirmPassword = ''
  let showPassword = false
  let submitting = false
  let complete = false
  let setupRequired = false
  let passwordSet = false
  let error = ''
  let errorElement: HTMLElement | undefined

  let token = ''

  onMount(() => {
    const url = new SvelteURL(window.location.href)
    token = new URLSearchParams(url.hash.slice(1)).get('token') || ''
    const hadTokenQuery = url.searchParams.has('token')
    url.searchParams.delete('token')
    if (url.hash || hadTokenQuery) {
      url.hash = ''
      window.history.replaceState(null, '', `${url.pathname}${url.search}`)
    }
  })
  $: title = complete
    ? mode === 'forgot-password'
      ? 'Check your email'
      : mode === 'verify-email'
        ? setupRequired && !passwordSet ? 'Choose a password' : passwordSet ? 'Password set' : 'Email verified'
        : mode === 'confirm-email-change' ? 'Email updated' : 'Password updated'
    : mode === 'forgot-password'
      ? 'Recover your account'
      : mode === 'reset-password'
        ? 'Choose a new password'
        : mode === 'verify-email'
          ? 'Verify your email'
          : 'Confirm your new email'

  async function showError(message: string) {
    error = message
    await tick()
    errorElement?.focus()
  }

  async function submit(chooseNewPassword = false) {
    error = ''
    if (mode === 'verify-email' && !chooseNewPassword && !verifyPassword) {
      await showError('Enter the password you chose when you signed up, or choose a new one instead.')
      return
    }
    if (mode === 'reset-password' && newPassword !== confirmPassword) {
      await showError('The new passwords do not match.')
      return
    }
    if (mode !== 'forgot-password' && !token) {
      await showError('This link is incomplete. Request a new email and use its full link.')
      return
    }
    submitting = true
    let failure = ''
    try {
      if (mode === 'forgot-password') {
        await requestPasswordRecovery(email)
      } else if (mode === 'reset-password') {
        onAuthenticated(await confirmPasswordRecovery(token, newPassword))
      } else if (mode === 'verify-email') {
        const verification = await confirmEmailVerification(token, chooseNewPassword ? undefined : verifyPassword)
        setupRequired = verification.credentialSetupRequired
        onAuthenticated(verification.account)
      } else {
        onAuthenticated(await confirmEmailChange(token))
      }
      complete = true
    } catch (reason) {
      failure = reason instanceof Error ? reason.message : 'The account service is temporarily unavailable.'
    } finally {
      newPassword = ''
      confirmPassword = ''
      verifyPassword = ''
      submitting = false
    }
    if (failure) await showError(failure)
  }
</script>

<section class="auth-page account-flow-page">
  <div class="auth-column">
    <h1>{title}</h1>
    {#if !complete && mode === 'forgot-password'}
      <p class="auth-lede">Enter the email address on your website account and we will send a link to choose a new password.</p>
    {/if}
    <div class="auth-form card">
      {#if complete}
        {#if mode === 'forgot-password'}
          <p>If an eligible account exists for that address, we sent a recovery link. This page never confirms whether an address is registered.</p>
          <a class="button button-soft" href="/login">Back to sign in</a>
        {:else if mode === 'verify-email' && setupRequired && !passwordSet}
          <p>Your email address is verified. This account has no password yet, so choose one to sign in again. You can also <a href="/account#security">add a passkey</a> from your account security settings.</p>
          <PasswordSetupForm onComplete={() => (passwordSet = true)} />
        {:else}
          <p>{mode === 'reset-password' || mode === 'confirm-email-change' ? 'Other website sessions were revoked.' : passwordSet ? 'You can sign in with your email address and this password.' : 'You can now use your account.'}</p>
          <a class="button" href="/account">Open account</a>
        {/if}
      {:else}
        <form class="account-form" on:submit|preventDefault={() => submit()}>
          {#if mode === 'forgot-password'}
            <div class="field">
              <label for="flow-email">Email</label>
              <input id="flow-email" type="email" bind:value={email} autocomplete="email" autocapitalize="none" spellcheck="false" inputmode="email" required maxlength="254" disabled={submitting} />
            </div>
          {:else if mode === 'reset-password'}
            <PasswordField id="flow-password" label="New password" bind:value={newPassword} bind:revealed={showPassword} autocomplete="new-password" minlength={12} describedby="flow-password-note" disabled={submitting} />
            <PasswordField id="flow-password-confirm" label="Confirm new password" bind:value={confirmPassword} revealed={showPassword} toggle={false} autocomplete="new-password" disabled={submitting} />
            <small id="flow-password-note">12 characters or more. Completing this signs out other website sessions.</small>
          {:else if mode === 'verify-email'}
            <p>Enter the password you chose when you signed up to finish verifying your email.</p>
            <PasswordField id="flow-verify-password" label="Your password" bind:value={verifyPassword} bind:revealed={showPassword} autocomplete="current-password" required={false} disabled={submitting} />
          {:else}
            <p>Use this link to replace the email address on your account.</p>
          {/if}
          {#if error}<p class="auth-error" role="alert" tabindex="-1" bind:this={errorElement}>{error}</p>{/if}
          <button class="button auth-submit" type="submit" disabled={submitting}>
            {submitting ? 'Working…' : mode === 'forgot-password' ? 'Send recovery link' : mode === 'reset-password' ? 'Set password' : mode === 'verify-email' ? 'Verify email' : 'Update email'}
          </button>
          {#if mode === 'verify-email'}
            <button class="button button-soft" type="button" disabled={submitting} on:click={() => submit(true)}>Choose a new password instead</button>
          {/if}
        </form>
      {/if}
    </div>
    {#if !complete && mode === 'forgot-password'}<p class="auth-switch">Remembered it? <a href="/login">Sign in</a></p>{/if}
  </div>
</section>
