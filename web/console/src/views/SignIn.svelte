<script lang="ts">
  import Brand from '../components/Brand.svelte'
  import { messageOf } from '../lib/errors'
  import { signIn } from '../lib/owner'

  type Props = { notice?: string; setupRequired?: boolean; onDone: () => void }
  const { notice = '', setupRequired = false, onDone }: Props = $props()

  let name = $state('')
  let password = $state('')
  let code = $state('')
  let busy = $state(false)
  let error = $state('')
  const ready = $derived(name.trim() !== '' && password !== '' && /^\d{6}$/.test(code))

  async function submit(event: SubmitEvent) {
    event.preventDefault()
    if (!ready || busy) return
    busy = true
    error = ''
    try {
      await signIn({ name: name.trim(), password, code })
      password = ''
      code = ''
      onDone()
    } catch (reason) {
      error = messageOf(reason)
      code = ''
    } finally {
      busy = false
    }
  }
</script>

<main class="auth-shell">
  <div class="auth-panel">
    <Brand />
    <h1>Sign in</h1>
    {#if setupRequired}
      <p>No owner exists yet. Open the setup link from the server log.</p>
    {:else}
      <form onsubmit={submit}>
        {#if notice}<p class="message warn" role="status">{notice}</p>{/if}
        <label>Name<input bind:value={name} autocomplete="username" maxlength="64" /></label>
        <label>Password<input type="password" bind:value={password} autocomplete="current-password" maxlength="1024" /></label>
        <label>Six-digit code<input bind:value={code} inputmode="numeric" maxlength="6" autocomplete="one-time-code" /></label>
        {#if error}<p class="message error" role="alert">{error}</p>{/if}
        <button type="submit" class="primary" disabled={busy || !ready}>Sign in</button>
      </form>
    {/if}
  </div>
</main>
