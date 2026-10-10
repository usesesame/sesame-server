<script lang="ts">
  import { onMount } from 'svelte'
  import { messageOf } from '../lib/errors'
  import { formatDateTime, pluralize } from '../lib/format'
  import { loadAudit } from '../lib/owner'
  import type { AuditChain, AuditEntry } from '../lib/types'

  let entries = $state<AuditEntry[]>([])
  let chain = $state<AuditChain | null>(null)
  let cursor = $state<number | null>(null)
  let loading = $state(true)
  let loadingMore = $state(false)
  let error = $state('')

  async function load(next?: number | null) {
    const page = await loadAudit(next)
    entries = next ? [...entries, ...page.entries] : page.entries
    chain = page.chain
    cursor = page.nextCursor ?? null
  }

  onMount(async () => {
    try {
      await load()
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      loading = false
    }
  })

  async function more() {
    loadingMore = true
    error = ''
    try {
      await load(cursor)
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      loadingMore = false
    }
  }

  function detailText(detail: Record<string, string>): string {
    return Object.entries(detail).map(([key, value]) => `${key}: ${value}`).join(', ')
  }
</script>

<section class="view">
  <div class="page-head">
    <div>
      <h1>Audit log</h1>
    </div>
  </div>
  {#if error}<p class="message error" role="alert">{error}</p>{/if}
  {#if chain}
    <p class="chain" data-ok={chain.ok} role="status">
      {#if chain.ok}
        Chain intact, {pluralize(chain.rows, 'entry', 'entries')} checked
      {:else}
        Chain broken{chain.firstBreak ? ` at entry ${chain.firstBreak.seq}` : ''}
      {/if}
      {#if !chain.ok && chain.firstBreak}<small>{chain.firstBreak.reason.charAt(0).toUpperCase()}{chain.firstBreak.reason.slice(1)}.</small>{/if}
    </p>
  {/if}
  {#if loading}
    <div class="spinner" role="status" aria-label="Loading"></div>
  {:else if entries.length === 0}
    <p class="empty">No entries yet.</p>
  {:else}
    <ul class="rows">
      {#each entries as entry (entry.seq)}
        <li class="row audit-row">
          <time datetime={entry.at}>{formatDateTime(entry.at)}</time>
          <div class="row-main">
            <strong>{entry.action}</strong>
            <small>{entry.actor}{entry.target ? ` · ${entry.target}` : ''}{Object.keys(entry.detail ?? {}).length ? ` · ${detailText(entry.detail)}` : ''}</small>
          </div>
        </li>
      {/each}
    </ul>
    {#if cursor}<div class="more"><button type="button" onclick={more} disabled={loadingMore}>Show older entries</button></div>{/if}
  {/if}
</section>
