<script lang="ts">
  import { onDestroy, onMount } from 'svelte'
  import OneTimeSecret from '../components/OneTimeSecret.svelte'
  import { messageOf } from '../lib/errors'
  import { formatRelative } from '../lib/format'
  import { cancelPairing, createPairing, listMembers, listPairings } from '../lib/owner'
  import type { IssuedPairing, Member, PendingPairing } from '../lib/types'

  type Props = { initialTarget?: string }
  const { initialTarget = 'self' }: Props = $props()

  let members = $state<Member[]>([])
  let pending = $state<PendingPairing[]>([])
  let target = $derived(initialTarget)
  let deviceName = $state('')
  let issued = $state<IssuedPairing | null>(null)
  let loading = $state(true)
  let busy = $state(false)
  let error = $state('')
  let notice = $state('')

  async function refresh() {
    try {
      const [loadedMembers, loadedPairings] = await Promise.all([listMembers(), listPairings()])
      members = loadedMembers
      pending = loadedPairings
      if (target !== 'self' && !members.some((member) => member.id === target)) target = 'self'
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      loading = false
    }
  }

  onMount(refresh)

  onDestroy(() => {
    issued = null
  })

  async function create(event: SubmitEvent) {
    event.preventDefault()
    if (busy) return
    busy = true
    error = ''
    notice = ''
    issued = null
    try {
      const member = members.find((candidate) => candidate.id === target)
      issued = await createPairing(member ? { memberId: member.id } : { self: true }, deviceName.trim())
      deviceName = ''
      await refresh()
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      busy = false
    }
  }

  async function cancel(id: string) {
    error = ''
    notice = ''
    try {
      await cancelPairing(id)
      if (issued?.pairingId === id) issued = null
      notice = 'The pairing code was cancelled.'
      await refresh()
    } catch (reason) {
      error = messageOf(reason)
    }
  }

  function expire() {
    issued = null
    notice = 'The pairing code expired.'
    void refresh()
  }
</script>

<section class="view">
  <div class="page-head">
    <div>
      <h1>Pairing</h1>
    </div>
  </div>
  <form class="form-stack" onsubmit={create}>
    <label>
      Connect a device for
      <select bind:value={target} disabled={loading}>
        <option value="self">Me</option>
        {#each members as member (member.id)}<option value={member.id}>{member.name}</option>{/each}
      </select>
    </label>
    <label>Device name <small>Optional.</small><input bind:value={deviceName} maxlength="64" autocomplete="off" /></label>
    <button type="submit" class="primary" disabled={busy || loading}>Create pairing code</button>
  </form>
  {#if error}<p class="message error" role="alert">{error}</p>{/if}
  {#if notice}<p class="message success" role="status">{notice}</p>{/if}
  {#if issued}
    <OneTimeSecret
      title="Pairing code for {issued.holder.name}"
      note="Shown once. Copy it before you leave this page."
      fields={[{ label: 'Link', value: issued.link }, { label: 'Code', value: issued.code }]}
      expiresAt={issued.expiresAt}
      onExpire={expire}
      onDismiss={() => { issued = null }}
      onCancel={() => issued && cancel(issued.pairingId)}
      cancelLabel="Cancel code"
    />
  {/if}
  <div class="section">
    <h2>Waiting to be used</h2>
    {#if pending.length === 0}
      <p class="empty">No pending codes.</p>
    {:else}
      <ul class="rows">
        {#each pending as pairing (pairing.id)}
          <li class="row">
            <div class="row-main">
              <strong>{pairing.holder.name}</strong>
              <small>{pairing.deviceName ? `${pairing.deviceName} · ` : ''}Expires {formatRelative(pairing.expiresAt)}</small>
            </div>
            <div class="row-side"><button type="button" class="quiet" onclick={() => cancel(pairing.id)}>Cancel</button></div>
          </li>
        {/each}
      </ul>
    {/if}
  </div>
</section>
