<script lang="ts">
  import { cancelStepUp, stepUpDialog, submitStepUp } from '../lib/stepup.svelte'

  let password = $state('')
  let code = $state('')
  let passwordInput = $state<HTMLInputElement>()
  const ready = $derived(password !== '' && /^\d{6}$/.test(code))
  const actions = $derived(stepUpDialog.actions.length > 0 ? stepUpDialog.actions : ['continue'])

  function sentence(action: string) {
    return action.charAt(0).toUpperCase() + action.slice(1)
  }

  $effect(() => {
    if (stepUpDialog.open) {
      password = ''
      code = ''
      passwordInput?.focus()
    }
  })

  async function submit(event: SubmitEvent) {
    event.preventDefault()
    if (!ready) return
    const approved = await submitStepUp(password, code)
    if (approved || !stepUpDialog.open) {
      password = ''
      code = ''
    }
  }

  function keydown(event: KeyboardEvent) {
    if (event.key === 'Escape' && !stepUpDialog.busy) cancelStepUp()
  }
</script>

{#if stepUpDialog.open}
  <div class="stepup-backdrop" role="presentation" onkeydown={keydown}>
    <div class="stepup-panel" role="dialog" aria-modal="true" aria-labelledby="stepup-title">
      <h2 id="stepup-title">Confirm it is you</h2>
      {#if actions.length === 1}
        <p>Enter your password and a current code to {actions[0]}.</p>
      {:else}
        <p>Enter your password and a current code to approve all of these actions:</p>
        <ul class="stepup-list">
          {#each actions as action, index (index)}<li>{sentence(action)}</li>{/each}
        </ul>
      {/if}
      <form onsubmit={submit}>
        <label>Password<input bind:this={passwordInput} type="password" bind:value={password} autocomplete="current-password" /></label>
        <label>Six-digit code<input bind:value={code} inputmode="numeric" maxlength="6" autocomplete="one-time-code" /></label>
        {#if stepUpDialog.error}<p class="message error" role="alert">{stepUpDialog.error}</p>{/if}
        <div class="stepup-actions">
          <button type="button" onclick={cancelStepUp} disabled={stepUpDialog.busy}>Cancel</button>
          <button type="submit" class="primary" disabled={stepUpDialog.busy || !ready}>Confirm</button>
        </div>
      </form>
    </div>
  </div>
{/if}
