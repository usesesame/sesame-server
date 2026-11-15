<script lang="ts">
  import { onMount } from 'svelte'
  import { messageOf } from '../lib/errors'
  import { formatBytes, formatDate, pluralize } from '../lib/format'
  import { loadSync, resetSyncVault, setSyncEnabled } from '../lib/owner'
  import type { SyncHolder, SyncOverview } from '../lib/types'

  let overview = $state<SyncOverview | null>(null)
  let loading = $state(true)
  let busy = $state(false)
  let error = $state('')
  let notice = $state('')
  let confirming = $state('')
  let resetting = $state('')
  let confirmingOff = $state(false)

  const syncingDevices = $derived(overview ? overview.holders.filter((holder) => holder.hasVault).reduce((total, holder) => total + holder.devices, 0) : 0)

  async function load() {
    try {
      overview = await loadSync()
      error = ''
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      loading = false
    }
  }

  onMount(load)

  const keyOf = (holder: SyncHolder) => `${holder.kind}:${holder.id}`

  function facts(holder: SyncHolder): string {
    if (!holder.hasVault) return 'no synced vault yet'
    return `${pluralize(holder.devices, 'device')}, version ${holder.currentRevision}, uses ${formatBytes(holder.storedBytes)}, last changed ${formatDate(holder.updatedAt)}`
  }

  function requestToggle() {
    if (!overview || busy) return
    if (overview.enabled) {
      confirmingOff = true
      return
    }
    void toggle()
  }

  async function toggle() {
    if (!overview || busy) return
    const enabled = !overview.enabled
    busy = true
    error = ''
    notice = ''
    try {
      await setSyncEnabled(enabled)
      notice = enabled ? 'Sync was turned on.' : 'Sync was turned off.'
      confirmingOff = false
      await load()
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      busy = false
    }
  }

  async function reset(holder: SyncHolder) {
    resetting = keyOf(holder)
    error = ''
    notice = ''
    try {
      await resetSyncVault(holder)
      confirming = ''
      notice = `${holder.name}'s synced vault was reset.`
      await load()
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      resetting = ''
    }
  }
</script>

<section class="view">
  <div class="page-head">
    <div>
      <h1>Sync</h1>
    </div>
  </div>
  {#if error}<p class="message error" role="alert">{error}</p>{/if}
  {#if notice}<p class="message success" role="status">{notice}</p>{/if}
  {#if loading}
    <div class="spinner" role="status" aria-label="Loading"></div>
  {:else if overview && !overview.available}
    <p>This server build does not include Sync.</p>
  {:else if overview}
    <ul class="rows">
      <li class="row">
        <div class="row-main">
          <strong>{overview.enabled ? 'Sync is on' : 'Sync is off'}</strong>
          {#if confirmingOff && overview.enabled}
            <small>{syncingDevices === 0 ? 'No device syncs through this server yet.' : `Turning off Sync stops ${pluralize(syncingDevices, 'device')} from syncing.`} The server refuses every Sync request until Sync is turned on again. Synced vaults stay stored, and every device keeps its local vault.</small>
          {/if}
        </div>
        <div class="row-side">
          {#if confirmingOff && overview.enabled}
            <button type="button" class="destructive" onclick={toggle} disabled={busy}>Stop syncing</button>
            <button type="button" onclick={() => { confirmingOff = false }} disabled={busy}>Keep Sync on</button>
          {:else}
            <button type="button" class={overview.enabled ? undefined : 'primary'} onclick={requestToggle} disabled={busy}>
              {overview.enabled ? 'Turn off Sync' : 'Turn on Sync'}
            </button>
          {/if}
        </div>
      </li>
    </ul>
    {#if !overview.enabled}<p>Devices cannot sync until Sync is turned on.</p>{/if}
    <div class="section">
      <p>Sync has not had an independent security review, and the server stores only encrypted data it cannot read.</p>
    </div>
    {#if overview.holders.length === 0}
      <p class="empty">No people yet.</p>
    {:else}
      <ul class="rows">
        {#each overview.holders as holder (keyOf(holder))}
          <li class="row">
            <div class="row-main">
              <strong>{holder.name}</strong>
              <small>{facts(holder)}</small>
              {#if confirming === keyOf(holder)}
                <small>This deletes the stored encrypted copy and history of {holder.name}'s synced vault. Their devices keep their local vaults, and they can start Sync again.</small>
              {/if}
            </div>
            {#if holder.hasVault}
              <div class="row-side">
                {#if confirming === keyOf(holder)}
                  <button type="button" class="destructive" onclick={() => reset(holder)} disabled={resetting === keyOf(holder)}>Reset synced vault</button>
                  <button type="button" onclick={() => { confirming = '' }} disabled={resetting === keyOf(holder)}>Cancel</button>
                {:else}
                  <button type="button" class="quiet danger" onclick={() => { confirming = keyOf(holder) }}>Reset synced vault</button>
                {/if}
              </div>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
    <div class="section">
      <p>The console cannot read, approve or add a device, so a person adds a device from another of their own devices.</p>
    </div>
  {/if}
</section>
