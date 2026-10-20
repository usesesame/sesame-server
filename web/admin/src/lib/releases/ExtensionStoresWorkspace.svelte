<script lang="ts">
  import type { ChromeListingState, ExtensionPublication, PublicExtension } from '../types'

  const NEXT: Record<string, string> = { built: 'uploaded', uploaded: 'submitted', submitted: 'approved', approved: 'published', published: 'withdrawn' }
  const NEXT_LABELS: Record<string, string> = { uploaded: 'Mark uploaded', submitted: 'Mark submitted', approved: 'Mark approved', published: 'Mark published', withdrawn: 'Mark withdrawn' }
  const STORE_LABELS: Record<string, string> = { chrome: 'Chrome Web Store', edge: 'Edge Add-ons', firefox: 'Firefox Add-ons' }

  const props = $props<{
    publications: ExtensionPublication[]
    canManage: boolean
    onTransition: (publication: ExtensionPublication, to: string, evidence: Record<string, string>) => void
    onAccept: (input: { store: string; version: string; packageSha256: string; filename: string; packageBytes: number }) => void
    chrome?: ChromeListingState | null
    canShow?: boolean
    onSetVisible?: (visible: boolean, select: HTMLSelectElement) => void
  }>()

  const chromePublic = $derived(props.chrome?.public.find((entry: PublicExtension) => entry.store === 'chrome'))
  const websiteSentence = $derived.by(() => {
    if (chromePublic?.status === 'published') return 'The website links to the store listing.'
    if (props.chrome?.visible && !props.chrome.listingUrl) return 'It stays hidden until the server has the Chrome extension id.'
    if (chromePublic?.status === 'approved') return 'The website says the extension is coming soon.'
    return 'The website does not mention the extension.'
  })

  let pending = $state<{ id: string; to: string } | null>(null)
  let reference = $state('')
  let built = $state({ store: 'chrome', version: '', packageSha256: '', filename: '', packageBytes: 0 })

  function nextState(publication: ExtensionPublication) {
    return NEXT[publication.status] ?? ''
  }

  function evidenceSummary(publication: ExtensionPublication) {
    return Object.entries(publication.evidence)
      .map(([state, values]) => `${state}: ${Object.values(values ?? {}).join(', ')}`)
      .join(' · ')
  }

  function requestTransition(publication: ExtensionPublication) {
    const to = nextState(publication)
    if (!to) return
    pending = { id: publication.id, to }
    reference = ''
  }

  function confirmTransition(publication: ExtensionPublication) {
    if (!pending || pending.id !== publication.id) return
    const key = pending.to === 'withdrawn' ? 'reason' : 'reference'
    const payload = reference.trim() ? { [key]: reference.trim() } : {}
    props.onTransition(publication, pending.to, payload)
    pending = null
  }

  function recordBuilt() {
    if (!/^\d+\.\d+\.\d+$/.test(built.version.trim())) return
    if (!/^[0-9a-f]{64}$/.test(built.packageSha256.trim())) return
    if (!built.filename.trim() || built.packageBytes < 1) return
    props.onAccept({ store: built.store, version: built.version.trim(), packageSha256: built.packageSha256.trim(), filename: built.filename.trim(), packageBytes: built.packageBytes })
    built = { store: built.store, version: '', packageSha256: '', filename: '', packageBytes: 0 }
  }
</script>

<div class="release-stack">
  {#if props.chrome}
    <section class="panel">
      <h2>Chrome Web Store</h2>
      {#if props.chrome.listingUrl}
        <div class="setting-row">
          <div><strong>Store listing</strong><small><a href={props.chrome.listingUrl} target="_blank" rel="noopener noreferrer">Open it in the Chrome Web Store</a></small></div>
        </div>
      {:else}
        <p class="empty">The server has no Chrome extension id, so it cannot link the listing. Set SESAME_CHROME_EXTENSION_ID on the server and redeploy.</p>
      {/if}
      <div class="setting-row">
        <div><strong>Show on the website</strong><small>{websiteSentence}</small></div>
        <select aria-label="Show the Chrome extension on the website" value={props.chrome.visible ? 'true' : 'false'} onchange={(event) => props.onSetVisible?.(event.currentTarget.value === 'true', event.currentTarget)} disabled={!props.canShow}><option value="false">Off</option><option value="true">On</option></select>
      </div>
    </section>
  {/if}
  <details class="package-records" open={!props.chrome}>
  <summary>Package records</summary>
  <div class="release-stack">
  {#each props.publications as publication (publication.id)}
    <article class="panel release-card">
      <header class="release-card-head">
        <div class="release-card-title">
          <strong>{STORE_LABELS[publication.store] ?? publication.store}</strong>
          <span class="badge badge-neutral">{publication.status}</span>
        </div>
        <div class="release-card-meta">
          <span>{publication.version}</span>
          <span>{publication.filename}</span>
          <span>Revision {publication.stateRevision}</span>
        </div>
      </header>
      <div class="release-card-body">
        <section>
          <h3>Reviewed package</h3>
          <dl><div><dt>sha256</dt><dd><code>{publication.packageSha256}</code></dd></div></dl>
          {#if evidenceSummary(publication)}<p class="evidence-summary">{evidenceSummary(publication)}</p>{/if}
          {#if publication.audit.length}
            <h3>Recent activity</h3>
            <ul class="release-activity">
              {#each publication.audit as entry (entry.id)}
                <li>{entry.action.replace('extension_publication.', '').replaceAll('_', ' ')} by {entry.adminEmail}</li>
              {/each}
            </ul>
          {/if}
        </section>
        {#if props.canManage && nextState(publication)}
          <section class="release-actions-area">
            <h3>Actions</h3>
            <div class="release-actions">
              <button class="primary" onclick={() => requestTransition(publication)}>{NEXT_LABELS[nextState(publication)]}</button>
            </div>
            {#if pending?.id === publication.id}
              <div class="confirm-strip">
                <p>Mark {publication.version} as {pending?.to}{pending?.to === 'withdrawn' ? ', which removes it from the store record' : ''}. {pending?.to === 'withdrawn' ? 'Reason' : 'Store listing or submission reference'}:</p>
                <input bind:value={reference} placeholder={pending?.to === 'withdrawn' ? 'Why it was withdrawn' : 'Store listing or submission reference'} />
                <div class="release-actions">
                  <button class="danger" onclick={() => confirmTransition(publication)}>Confirm</button>
                  <button onclick={() => (pending = null)}>Cancel</button>
                </div>
              </div>
            {/if}
          </section>
        {/if}
      </div>
    </article>
  {/each}
  {#if props.publications.length === 0}<p class="empty">No extension store packages recorded yet.</p>{/if}
  {#if props.canManage}
    <section class="panel release-card">
      <header class="release-card-head">
        <div class="release-card-title"><strong>Record a built package</strong></div>
      </header>
      <div class="record-grid">
        <label>Store<select bind:value={built.store}><option value="chrome">Chrome Web Store</option><option value="edge">Edge Add-ons</option><option value="firefox">Firefox Add-ons</option></select></label>
        <label>Version<input bind:value={built.version} placeholder="0.1.0" /></label>
        <label>Package sha256<input bind:value={built.packageSha256} placeholder="64 hex characters" /></label>
        <label>Package bytes<input type="number" min="1" bind:value={built.packageBytes} /></label>
        <label class="span-2">File name<input bind:value={built.filename} placeholder="sesame-extension-0.1.0-chrome.zip" /></label>
      </div>
      <div class="release-actions">
        <button class="primary" onclick={recordBuilt} disabled={!/^\d+\.\d+\.\d+$/.test(built.version.trim()) || !/^[0-9a-f]{64}$/.test(built.packageSha256.trim()) || !built.filename.trim() || built.packageBytes < 1}>Record built package</button>
      </div>
    </section>
  {/if}
  </div>
  </details>
</div>
