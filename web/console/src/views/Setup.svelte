<script lang="ts">
  import { onDestroy, onMount } from 'svelte'
  import Brand from '../components/Brand.svelte'
  import CheckRow from '../components/CheckRow.svelte'
  import { messageOf } from '../lib/errors'
  import { beginSetup, completeSetup } from '../lib/owner'
  import { encodeQr, qrPath, qrViewSize } from '../lib/qr'
  import type { SetupDetails } from '../lib/types'

  type Props = { token: string; onDone: () => void }
  const { token, onDone }: Props = $props()

  let details = $state<SetupDetails | null>(null)
  let loading = $state(true)
  let loadError = $state('')
  let name = $state('')
  let password = $state('')
  let confirm = $state('')
  let code = $state('')
  let updateChecks = $state(false)
  let busy = $state(false)
  let error = $state('')

  const nameLocked = $derived(details !== null && !details.firstOwner && details.ownerName !== '')
  const qr = $derived(details ? encodeQr(details.totpUri) : null)
  const passwordProblem = $derived(
    confirm.length > 0 && confirm !== password ? 'The two passwords do not match.' : '',
  )
  const ready = $derived(name.trim() !== '' && password.length >= 12 && password === confirm && /^\d{6}$/.test(code))

  onMount(async () => {
    if (!token) {
      loading = false
      return
    }
    try {
      details = await beginSetup(token)
      name = details.ownerName
    } catch (reason) {
      loadError = messageOf(reason, 'This setup link could not be used.')
    } finally {
      loading = false
    }
  })

  onDestroy(() => {
    details = null
    password = ''
    confirm = ''
    code = ''
  })

  async function submit(event: SubmitEvent) {
    event.preventDefault()
    if (!ready || busy) return
    busy = true
    error = ''
    try {
      await completeSetup({ token, name: name.trim(), password, code, ...(updateChecks ? { updateChecks: true } : {}) })
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
    {#if loading}
      <h1>Setting up</h1>
      <div class="spinner" role="status" aria-label="Loading"></div>
    {:else if !token}
      <h1>Open your setup link</h1>
      <p>Open the setup link from the server log in this browser.</p>
    {:else if loadError || !details}
      <h1>This setup link does not work</h1>
      <p class="message error" role="alert">{loadError}</p>
      <p>To get a new link, run <code>sesame-server owner reset NAME</code> on the server.</p>
    {:else}
      <h1>{details.firstOwner ? 'Create the first owner' : 'Finish your owner sign-in'}</h1>
      <form onsubmit={submit}>
        <label>Name<input bind:value={name} readonly={nameLocked} autocomplete="username" maxlength="64" /></label>
        <label>Password<input type="password" bind:value={password} autocomplete="new-password" maxlength="1024" /><small>At least 12 characters.</small></label>
        <label>Repeat the password<input type="password" bind:value={confirm} autocomplete="new-password" maxlength="1024" /></label>
        {#if passwordProblem}<p class="message error" role="alert">{passwordProblem}</p>{/if}
        {#if qr}
          <div class="setup-key">
            <svg class="qr" viewBox="0 0 {qrViewSize(qr)} {qrViewSize(qr)}" role="img" aria-label="Authenticator setup code" shape-rendering="crispEdges">
              <path d={qrPath(qr)} fill="currentColor" />
            </svg>
            <small>Scan with an authenticator app.</small>
            <details>
              <summary>Cannot scan? Enter the key by hand</summary>
              <code class="key-text">{details.totpSecret}</code>
            </details>
          </div>
        {/if}
        <label>Six-digit code<input bind:value={code} inputmode="numeric" maxlength="6" autocomplete="one-time-code" /></label>
        {#if details.firstOwner}
          <CheckRow label="Check for updates" hint="Only this server's address is visible to Sesame." checked={updateChecks} onchange={(next) => { updateChecks = next }} />
        {/if}
        {#if error}<p class="message error" role="alert">{error}</p>{/if}
        <button type="submit" class="primary" disabled={busy || !ready}>Create owner</button>
      </form>
    {/if}
  </div>
</main>
