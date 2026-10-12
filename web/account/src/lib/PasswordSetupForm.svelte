<script lang="ts">
  import { setupPassword } from './auth'

  export let onComplete: () => void

  let newPassword = ''
  let confirmPassword = ''
  let showPassword = false
  let submitting = false
  let error = ''
  let sessionTooOld = false

  async function submit() {
    error = ''
    sessionTooOld = false
    if (newPassword !== confirmPassword) {
      error = 'The new passwords do not match.'
      return
    }
    submitting = true
    try {
      await setupPassword(newPassword)
      newPassword = ''
      confirmPassword = ''
      onComplete()
    } catch (reason) {
      if (reason instanceof Error && 'code' in reason && reason.code === 'recent_auth_required') {
        sessionTooOld = true
        error = 'This sign-in is too old to set a password.'
      } else {
        error = reason instanceof Error ? reason.message : 'The account service is temporarily unavailable.'
      }
    } finally {
      submitting = false
    }
  }
</script>

<form class="account-form" on:submit|preventDefault={submit}>
  <label>New password
    <div class="password-field">
      <input type={showPassword ? 'text' : 'password'} bind:value={newPassword} autocomplete="new-password" minlength="12" maxlength="1024" required disabled={submitting} />
      <button type="button" class="password-toggle" on:click={() => (showPassword = !showPassword)} disabled={submitting} aria-pressed={showPassword}>{showPassword ? 'Hide' : 'Show'}</button>
    </div>
  </label>
  <label>Confirm new password<input type={showPassword ? 'text' : 'password'} bind:value={confirmPassword} autocomplete="new-password" required disabled={submitting} /></label>
  <small>Use at least 12 characters.</small>
  {#if error}<p class="auth-error" role="alert">{error}</p>{/if}
  {#if sessionTooOld}<p>You can <a href="/forgot-password">send yourself a recovery link</a> to choose a password instead.</p>{/if}
  <button class="button auth-submit" type="submit" disabled={submitting}>{submitting ? 'Working…' : 'Set password'}</button>
</form>
