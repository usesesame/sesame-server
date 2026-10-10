<script lang="ts">
  import { onMount } from 'svelte'
  import { messageOf } from '../lib/errors'
  import { loadSettings, saveSettings } from '../lib/owner'
  import { session } from '../lib/session.svelte'

  let name = $state('')
  let publicUrl = $state('')
  let saved = $state({ name: '', publicUrl: '', fingerprint: '', publicUrlSet: true })
  let loading = $state(true)
  let busy = $state(false)
  let error = $state('')
  let notice = $state('')
  const changed = $derived(name.trim() !== saved.name || publicUrl.trim() !== saved.publicUrl)

  onMount(async () => {
    try {
      const settings = await loadSettings()
      name = settings.name
      publicUrl = settings.publicUrl
      saved = { name: settings.name, publicUrl: settings.publicUrl, fingerprint: settings.fingerprint, publicUrlSet: settings.publicUrlSet }
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      loading = false
    }
  })

  async function save(event: SubmitEvent) {
    event.preventDefault()
    if (busy || !changed) return
    busy = true
    error = ''
    notice = ''
    try {
      const change: { name?: string; publicUrl?: string } = {}
      if (name.trim() !== saved.name) change.name = name.trim()
      if (publicUrl.trim() !== saved.publicUrl) change.publicUrl = publicUrl.trim()
      const updated = await saveSettings(change)
      name = updated.name
      publicUrl = updated.publicUrl
      saved = { name: updated.name, publicUrl: updated.publicUrl, fingerprint: updated.fingerprint, publicUrlSet: updated.publicUrlSet }
      session.instanceName = updated.name
      notice = 'Settings saved.'
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      busy = false
    }
  }
</script>

<section class="view">
  <div class="page-head">
    <div>
      <h1>Settings</h1>
    </div>
  </div>
  {#if loading}
    <div class="spinner" role="status" aria-label="Loading"></div>
  {:else}
    <form class="form-stack" onsubmit={save}>
      <label>Instance name<input bind:value={name} maxlength="64" autocomplete="off" /></label>
      <label>
        Public URL
        <small>To use another address, change SESAME_PUBLIC_URL and restart the server.</small>
        <input type="url" bind:value={publicUrl} autocomplete="off" spellcheck="false" />
      </label>
      {#if error}<p class="message error" role="alert">{error}</p>{/if}
      {#if notice}<p class="message success" role="status">{notice}</p>{/if}
      <button type="submit" class="primary" disabled={busy || !changed || name.trim() === ''}>Save settings</button>
    </form>
    <div class="section">
      <h2>Server fingerprint</h2>
      <p>Check that the desktop app shows this before you pair.</p>
      <code class="key-text">{saved.fingerprint}</code>
    </div>
  {/if}
</section>
