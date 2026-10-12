<script lang="ts">
  import { onMount } from 'svelte'
  import { messageOf } from '../lib/errors'
  import { deviceDetail, formatRelative } from '../lib/format'
  import { listDevices, revokeDevice } from '../lib/owner'
  import type { Device } from '../lib/types'

  let devices = $state<Device[]>([])
  let loading = $state(true)
  let error = $state('')
  let notice = $state('')
  let confirming = $state('')
  let busy = $state('')

  async function load() {
    try {
      devices = await listDevices()
      error = ''
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      loading = false
    }
  }

  onMount(load)

  async function revoke(device: Device) {
    busy = device.id
    error = ''
    notice = ''
    try {
      await revokeDevice(device.id)
      confirming = ''
      notice = `${device.name} was revoked.`
      await load()
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      busy = ''
    }
  }
</script>

<section class="view">
  <div class="page-head">
    <div>
      <h1>Devices</h1>
    </div>
    <a class="primary button-link" href="#/pairing">Add a device</a>
  </div>
  {#if error}<p class="message error" role="alert">{error}</p>{/if}
  {#if notice}<p class="message success" role="status">{notice}</p>{/if}
  {#if loading}
    <div class="spinner" role="status" aria-label="Loading"></div>
  {:else if devices.length === 0}
    <p class="empty">No devices yet.</p>
  {:else}
    <ul class="rows">
      {#each devices as device (device.id)}
        <li class="row">
          <div class="row-main">
            <strong>{device.name}</strong>
            <small>{device.holder.name}{deviceDetail(device) ? ` · ${deviceDetail(device)}` : ''}</small>
          </div>
          <div class="row-side">
            <small>Last seen {formatRelative(device.lastSeenAt)}</small>
            {#if confirming === device.id}
              <button type="button" class="danger" onclick={() => revoke(device)} disabled={busy === device.id}>Revoke device</button>
              <button type="button" onclick={() => { confirming = '' }} disabled={busy === device.id}>Keep</button>
            {:else}
              <button type="button" class="quiet" onclick={() => { confirming = device.id }}>Revoke</button>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</section>
