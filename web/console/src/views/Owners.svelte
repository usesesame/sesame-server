<script lang="ts">
  import { onDestroy, onMount } from 'svelte'
  import OneTimeSecret from '../components/OneTimeSecret.svelte'
  import { messageOf } from '../lib/errors'
  import { formatDateTime } from '../lib/format'
  import { inviteOwner, listOwners, removeOwner } from '../lib/owner'
  import { endSession } from '../lib/session.svelte'
  import type { Owner, OwnerInvite } from '../lib/types'

  let owners = $state<Owner[]>([])
  let loading = $state(true)
  let error = $state('')
  let notice = $state('')
  let name = $state('')
  let inviting = $state(false)
  let invite = $state<OwnerInvite | null>(null)
  let confirming = $state('')
  let busy = $state('')

  const activeCount = $derived(owners.filter((owner) => !owner.setupPending).length)

  async function load() {
    try {
      owners = await listOwners()
      error = ''
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      loading = false
    }
  }

  onMount(load)

  onDestroy(() => {
    invite = null
  })

  async function create(event: SubmitEvent) {
    event.preventDefault()
    const trimmed = name.trim()
    if (!trimmed || inviting) return
    inviting = true
    error = ''
    notice = ''
    invite = null
    try {
      invite = await inviteOwner(trimmed)
      name = ''
      await load()
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      inviting = false
    }
  }

  async function remove(owner: Owner) {
    busy = owner.id
    error = ''
    notice = ''
    try {
      await removeOwner(owner.id)
      confirming = ''
      if (owner.current) {
        endSession('You removed your own owner account.')
        return
      }
      notice = `${owner.name} was removed and signed out.`
      await load()
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      busy = ''
    }
  }

  function expire() {
    invite = null
    notice = 'The setup link expired. Invite the owner again.'
  }
</script>

<section class="view">
  <div class="page-head">
    <div>
      <h1>Owners</h1>
    </div>
  </div>
  <form class="toolbar" onsubmit={create}>
    <label>Name<input bind:value={name} maxlength="64" autocomplete="off" /></label>
    <button type="submit" class="primary" disabled={inviting || name.trim() === ''}>Invite owner</button>
  </form>
  {#if error}<p class="message error" role="alert">{error}</p>{/if}
  {#if notice}<p class="message success" role="status">{notice}</p>{/if}
  {#if invite}
    <OneTimeSecret
      title="Setup link for {invite.owner.name}"
      note="Send this link to {invite.owner.name}. It works once and is shown only here."
      fields={[{ label: 'Setup link', value: invite.link }]}
      expiresAt={invite.expiresAt}
      onExpire={expire}
      onDismiss={() => { invite = null }}
    />
  {/if}
  {#if loading}
    <div class="spinner" role="status" aria-label="Loading"></div>
  {:else}
    <ul class="rows">
      {#each owners as owner (owner.id)}
        <li class="row">
          <div class="row-main">
            <strong>{owner.name}</strong>
            <small>{owner.current ? 'You · ' : ''}{owner.setupPending ? 'Setup not finished' : `Last signed in ${formatDateTime(owner.lastLoginAt)}`}</small>
          </div>
          <div class="row-side">
            {#if confirming === owner.id}
              <small>{owner.current ? 'You will be signed out.' : 'They will be signed out.'}</small>
              <button type="button" class="danger" onclick={() => remove(owner)} disabled={busy === owner.id}>Remove owner</button>
              <button type="button" onclick={() => { confirming = '' }} disabled={busy === owner.id}>Keep</button>
            {:else}
              <button type="button" class="quiet" onclick={() => { confirming = owner.id }} disabled={activeCount <= 1 && !owner.setupPending}>Remove</button>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
    {#if activeCount <= 1}<p class="empty">The last active owner cannot be removed.</p>{/if}
  {/if}
</section>
