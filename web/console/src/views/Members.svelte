<script lang="ts">
  import { onMount } from 'svelte'
  import { messageOf } from '../lib/errors'
  import { formatDate, pluralize } from '../lib/format'
  import { addMember, listMembers, removeMember, renameMember } from '../lib/owner'
  import type { Member } from '../lib/types'

  type Props = { onPair: (memberId: string) => void }
  const { onPair }: Props = $props()

  let members = $state<Member[]>([])
  let loading = $state(true)
  let error = $state('')
  let notice = $state('')
  let newName = $state('')
  let adding = $state(false)
  let renaming = $state('')
  let renameValue = $state('')
  let confirming = $state('')
  let busy = $state('')

  async function load() {
    try {
      members = await listMembers()
      error = ''
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      loading = false
    }
  }

  onMount(load)

  async function add(event: SubmitEvent) {
    event.preventDefault()
    const name = newName.trim()
    if (!name || adding) return
    adding = true
    error = ''
    notice = ''
    try {
      await addMember(name)
      newName = ''
      notice = `${name} was added.`
      await load()
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      adding = false
    }
  }

  async function rename(member: Member, event: SubmitEvent) {
    event.preventDefault()
    const name = renameValue.trim()
    if (!name) return
    busy = member.id
    error = ''
    notice = ''
    try {
      await renameMember(member.id, name)
      renaming = ''
      await load()
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      busy = ''
    }
  }

  async function remove(member: Member) {
    busy = member.id
    error = ''
    notice = ''
    try {
      const result = await removeMember(member.id)
      confirming = ''
      notice = `${member.name} was removed and ${pluralize(result.revokedDevices, 'device')} ${result.revokedDevices === 1 ? 'was' : 'were'} revoked.`
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
      <h1>Members</h1>
    </div>
  </div>
  <form class="toolbar" onsubmit={add}>
    <label>Name<input bind:value={newName} maxlength="64" autocomplete="off" /></label>
    <button type="submit" class="primary" disabled={adding || newName.trim() === ''}>Add member</button>
  </form>
  {#if error}<p class="message error" role="alert">{error}</p>{/if}
  {#if notice}<p class="message success" role="status">{notice}</p>{/if}
  {#if loading}
    <div class="spinner" role="status" aria-label="Loading"></div>
  {:else if members.length === 0}
    <p class="empty">No members yet.</p>
  {:else}
    <ul class="rows">
      {#each members as member (member.id)}
        <li class="row">
          {#if renaming === member.id}
            <form class="inline-edit" onsubmit={(event) => rename(member, event)}>
              <input bind:value={renameValue} maxlength="64" aria-label="Member name" />
              <button type="submit" class="primary" disabled={busy === member.id || renameValue.trim() === ''}>Save</button>
              <button type="button" onclick={() => { renaming = '' }}>Cancel</button>
            </form>
          {:else}
            <div class="row-main">
              <strong>{member.name}</strong>
              <small>{pluralize(member.deviceCount, 'device')} · added {formatDate(member.createdAt)}</small>
            </div>
            <div class="row-side">
              {#if confirming === member.id}
                <small>This revokes {pluralize(member.deviceCount, 'device')}.</small>
                <button type="button" class="danger" onclick={() => remove(member)} disabled={busy === member.id}>Remove member</button>
                <button type="button" onclick={() => { confirming = '' }} disabled={busy === member.id}>Keep</button>
              {:else}
                <button type="button" class="quiet" onclick={() => onPair(member.id)}>Add a device</button>
                <button type="button" class="quiet" onclick={() => { renaming = member.id; renameValue = member.name }}>Rename</button>
                <button type="button" class="quiet" onclick={() => { confirming = member.id }}>Remove</button>
              {/if}
            </div>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</section>
