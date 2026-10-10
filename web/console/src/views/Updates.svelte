<script lang="ts">
  import { onMount } from 'svelte'
  import CheckRow from '../components/CheckRow.svelte'
  import CopyCode from '../components/CopyCode.svelte'
  import { messageOf } from '../lib/errors'
  import { APIError } from '../lib/api'
  import { formatRelative, isOlderVersion } from '../lib/format'
  import { checkUpdates, loadUpdates, saveUpdates } from '../lib/owner'
  import { updateNotice } from '../lib/updates.svelte'
  import type { UpdateCommand, UpdateInfo, UpdateRelease } from '../lib/types'

  type Product = { id: string; name: string; current: string; latest: UpdateRelease | null; available: boolean; commands: UpdateCommand[] }

  const productNames: Record<string, string> = { 'sesame-server': 'Sesame server' }
  const feedProblems: Record<string, string> = {
    feed_unreachable: 'The update feed could not be reached.',
    feed_invalid: 'The update feed could not be verified.',
    feed_rollback: 'The update feed offered an older release, so it was ignored.',
    feed_expired: 'The update feed has expired.',
    not_configured: 'Updates are not set up for this build.',
  }

  const refusals: Record<string, string> = {
    updates_off: 'Update checks are off.',
    invalid_updates: 'The update settings were not accepted.',
  }

  let info = $state<UpdateInfo | null>(null)
  let loading = $state(true)
  let busy = $state(false)
  let error = $state('')
  let checking = $state(false)
  let enabled = $state(false)

  const products = $derived<Product[]>(info ? [{
    id: info.current.product,
    name: productNames[info.current.product] ?? info.current.product,
    current: info.current.version,
    latest: info.latest,
    available: info.available,
    commands: info.commands,
  }] : [])
  const problem = $derived(info && info.error ? (feedProblems[info.error] ?? 'The update check failed.') : '')

  function apply(next: UpdateInfo) {
    info = next
    enabled = next.enabled === true
    updateNotice.available = next.available
  }

  onMount(async () => {
    try {
      apply(await loadUpdates())
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      loading = false
    }
  })

  async function choose(next: boolean) {
    if (!info || busy) return
    busy = true
    error = ''
    try {
      apply(await saveUpdates({ enabled: next, channel: info.channel }))
    } catch (reason) {
      error = refusal(reason)
      enabled = info.enabled === true
    } finally {
      busy = false
    }
  }

  async function checkNow() {
    if (checking) return
    checking = true
    error = ''
    try {
      apply(await checkUpdates())
    } catch (reason) {
      error = refusal(reason)
      if (reason instanceof APIError && reason.code === 'updates_off') void reload()
    } finally {
      checking = false
    }
  }

  function refusal(reason: unknown): string {
    return reason instanceof APIError && refusals[reason.code] ? refusals[reason.code] : messageOf(reason)
  }

  async function reload() {
    try {
      apply(await loadUpdates())
    } catch {
      return
    }
  }

  function safeLink(url: string): string {
    try {
      const parsed = new URL(url)
      return parsed.protocol === 'https:' || parsed.protocol === 'http:' ? parsed.href : ''
    } catch {
      return ''
    }
  }
</script>

<section class="view">
  <div class="page-head">
    <div>
      <h1>Updates</h1>
    </div>
  </div>
  {#if error}<p class="message error" role="alert">{error}</p>{/if}
  {#if loading}
    <div class="spinner" role="status" aria-label="Loading"></div>
  {:else if info}
    {#if !info.configured}
      <p class="message warn" role="status">Updates are not set up for this build.</p>
    {:else if problem}
      <p class="message warn" role="status">{problem}</p>
    {/if}
    <ul class="rows">
      {#each products as product (product.id)}
        {@const notes = product.latest ? safeLink(product.latest.notesUrl) : ''}
        <li class="row row-wrap">
          <div class="row-main">
            <strong>{product.name}</strong>
            <small>Version {product.current}</small>
          </div>
          {#if info.configured && info.enabled === true}
            <div class="row-status">
              {#if product.available && product.latest}
                <span>{product.latest.version} is available</span>
                <small>
                  {#if product.latest.security}<span class="mark">Security update</span>{/if}
                  {#if notes}<a href={notes} target="_blank" rel="noopener noreferrer">Release notes</a>{/if}
                </small>
              {:else if product.latest || info.checkedAt}
                <span>Up to date</span>
                {#if info.checkedAt}<small>Checked {formatRelative(info.checkedAt)}</small>{/if}
              {:else}
                <span>Not checked yet</span>
              {/if}
            </div>
            {#if product.available}
              <div class="row-detail">
                {#if product.latest && product.latest.minimumFrom && isOlderVersion(product.current, product.latest.minimumFrom)}
                  <p>Direct updates need {product.latest.minimumFrom} or later.</p>
                {/if}
                {#each product.commands as command, index (index)}
                  <CopyCode label={command.label} value={command.text} />
                {/each}
              </div>
            {/if}
          {/if}
        </li>
      {/each}
    </ul>
    {#if info.configured && info.enabled === null}
      <div class="section">
        <h2>Check for updates</h2>
        <p>Only this server's address is visible to Sesame.</p>
        <div class="actions">
          <button type="button" class="primary" onclick={() => choose(true)} disabled={busy}>Turn on update checks</button>
          <button type="button" onclick={() => choose(false)} disabled={busy}>Keep them off</button>
        </div>
      </div>
    {:else if info.configured}
      <div class="section">
        <CheckRow label="Check for updates" hint="Only this server's address is visible to Sesame." checked={enabled} disabled={busy} onchange={(next) => { enabled = next; void choose(next) }} />
        {#if info.enabled === true}
          <div class="actions"><button type="button" onclick={checkNow} disabled={checking}>Check now</button></div>
        {/if}
      </div>
    {/if}
  {/if}
</section>
