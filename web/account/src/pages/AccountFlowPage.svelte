<script lang="ts">
  import { onMount } from 'svelte'
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
  let confirmPassword = ''
  let showPassword = false
  let submitting = false
  let complete = false
  let error = ''

  let token = ''

  onMount(() => {
    const url = new URL(window.location.href)
    token = new URLSearchParams(url.hash.slice(1)).get('token') || ''
    const hadTokenQuery = url.searchParams.has('token')
    url.searchParams.delete('token')
    if (url.hash || hadTokenQuery) {
      url.hash = ''
      window.history.replaceState(null, '', `${url.pathname}${url.search}`)
    }
  })
  $: title = mode === 'forgot-password'
    ? 'Recover your website account.'
    : mode === 'reset-password'
      ? 'Choose a new account password.'
      : mode === 'verify-email'
        ? 'Verify your email.'
        : 'Confirm your new email.'

  async function submit() {
    error = ''
    if (mode === 'reset-password' && newPassword !== confirmPassword) {
      error = 'The new passwords do not match.'
      return
    }
    if (mode !== 'forgot-password' && !token) {
      error = 'This link is incomplete. Request a new email and use its full link.'
      return
    }
    submitting = true
    try {
      if (mode === 'forgot-password') {
        await requestPasswordRecovery(email)
      } else if (mode === 'reset-password') {
        onAuthenticated(await confirmPasswordRecovery(token, newPassword))
      } else if (mode === 'verify-email') {
        onAuthenticated(await confirmEmailVerification(token))
      } else {
        onAuthenticated(await confirmEmailChange(token))
      }
      complete = true
    } catch (reason) {
      error = reason instanceof Error ? reason.message : 'The account service is temporarily unavailable.'
    } finally {
      newPassword = ''
      confirmPassword = ''
      submitting = false
    }
  }
</script>

<section class="auth-page account-flow-page">
  <div class="auth-copy">
    <h1>{title}</h1>
    <p class="intro">This changes your Sesame website account only. Your local vault and its unlock methods are separate.</p>
  </div>

  <div class="auth-form card">
    {#if complete}
      {#if mode === 'forgot-password'}
        <h2>Check your email.</h2>
        <p>If an eligible account exists for that address, we sent a recovery link. This page never confirms whether an address is registered.</p>
        <a class="button button-soft" href="/login">Back to sign in</a>
      {:else}
        <h2>{mode === 'verify-email' ? 'Email verified.' : mode === 'confirm-email-change' ? 'Email updated.' : 'Password updated.'}</h2>
        <p>{mode === 'reset-password' || mode === 'confirm-email-change' ? 'Other website sessions were revoked.' : 'You can continue to your account.'}</p>
        <a class="button" href="/account">Open account</a>
      {/if}
    {:else}
      <h2>{mode === 'forgot-password' ? 'Send a recovery link' : 'Confirm this change'}</h2>
      <form class="account-form" on:submit|preventDefault={submit}>
        {#if mode === 'forgot-password'}
          <label>Email<input type="email" bind:value={email} autocomplete="email" required maxlength="254" disabled={submitting} /></label>
        {:else if mode === 'reset-password'}
          <label>New password
            <div class="password-field">
              <input type={showPassword ? 'text' : 'password'} bind:value={newPassword} autocomplete="new-password" minlength="12" maxlength="1024" required disabled={submitting} />
              <button type="button" class="password-toggle" on:click={() => (showPassword = !showPassword)} disabled={submitting} aria-pressed={showPassword}>{showPassword ? 'Hide' : 'Show'}</button>
            </div>
          </label>
          <label>Confirm new password<input type={showPassword ? 'text' : 'password'} bind:value={confirmPassword} autocomplete="new-password" required disabled={submitting} /></label>
          <small>Use at least 12 characters. Completing this signs out other website sessions.</small>
        {:else}
          <p>{mode === 'verify-email' ? 'Use this link to verify the email address on your account.' : 'Use this link to replace the email address on your account.'}</p>
        {/if}
        {#if error}<p class="auth-error" role="alert">{error}</p>{/if}
        <button class="button auth-submit" type="submit" disabled={submitting}>
          {submitting ? 'Working…' : mode === 'forgot-password' ? 'Send recovery link' : mode === 'reset-password' ? 'Set new password' : mode === 'verify-email' ? 'Verify email' : 'Update email'}
        </button>
      </form>
    {/if}
  </div>
</section>
