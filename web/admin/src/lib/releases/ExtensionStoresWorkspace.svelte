<script lang="ts">
  import type { ExtensionPublication } from '../types'

  const NEXT: Record<string, string> = { built: 'uploaded', uploaded: 'submitted', submitted: 'approved', approved: 'published', published: 'withdrawn' }
  const NEXT_LABELS: Record<string, string> = { uploaded: 'Mark uploaded', submitted: 'Mark submitted', approved: 'Mark approved', published: 'Mark published', withdrawn: 'Mark withdrawn' }

  const props = $props<{
    publications: ExtensionPublication[]
    canManage: boolean
    onTransition: (publication: ExtensionPublication, to: string, evidence: Record<string, string>) => void
    onAccept: (input: { store: string; version: string; packageSha256: string; filename: string; packageBytes: number }) => void
  }>()

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

<section class="panel">
  <h2>Extension stores</h2>
  {#each props.publications as publication (publication.id)}
    <div class="release-edit">
      <div class="two"><label>Store<input value={publication.store} readonly /></label><label>Version<input value={publication.version} readonly /></label></div>
      <div class="two"><label>Status<input value={publication.status} readonly /></label><label>Package<input value={publication.filename} readonly /></label></div>
      <div class="release-evidence">
        <strong>Reviewed package</strong>
        <dl><div><dt>sha256</dt><dd><code>{publication.packageSha256}</code></dd></div></dl>
        {#if evidenceSummary(publication)}<p>{evidenceSummary(publication)}</p>{/if}
      </div>
      <p>Revision {publication.stateRevision}</p>
      {#if publication.audit.length}<h3>Recent activity</h3><ul>{#each publication.audit as entry (entry.id)}<li>{entry.action.replace('extension_publication.', '').replaceAll('_', ' ')} by {entry.adminEmail}</li>{/each}</ul>{/if}
      {#if props.canManage && nextState(publication)}
        <button class="primary" onclick={() => requestTransition(publication)}>{NEXT_LABELS[nextState(publication)]}</button>
        {#if pending?.id === publication.id}
          <div class="release-blockers">
            <p>Mark {publication.version} as {pending?.to}{pending?.to === 'withdrawn' ? ' (this removes it from the store record)' : ''}. {pending?.to === 'withdrawn' ? 'Reason' : 'Reference (store URL or ID)'}:</p>
            <input bind:value={reference} placeholder={pending?.to === 'withdrawn' ? 'Why it was withdrawn' : 'Store listing or submission reference'} />
            <button class="danger" onclick={() => confirmTransition(publication)}>Confirm</button>
            <button onclick={() => (pending = null)}>Cancel</button>
          </div>
        {/if}
      {/if}
    </div>
  {/each}
  {#if props.publications.length === 0}<p class="empty">No extension store packages recorded yet.</p>{/if}
  {#if props.canManage}
    <h3>Record a built package</h3>
    <div class="two"><label>Store<select bind:value={built.store}><option value="chrome">Chrome Web Store</option><option value="edge">Edge Add-ons</option><option value="firefox">Firefox Add-ons</option></select></label><label>Version<input bind:value={built.version} placeholder="0.1.0" /></label></div>
    <div class="two"><label>Package sha256<input bind:value={built.packageSha256} placeholder="64 hex characters" /></label><label>Package bytes<input type="number" min="1" bind:value={built.packageBytes} /></label></div>
    <label>File name<input bind:value={built.filename} placeholder="sesame-extension-0.1.0-chrome.zip" /></label>
    <button class="primary" onclick={recordBuilt} disabled={!/^\d+\.\d+\.\d+$/.test(built.version.trim()) || !/^[0-9a-f]{64}$/.test(built.packageSha256.trim()) || !built.filename.trim() || built.packageBytes < 1}>Record built package</button>
  {/if}
</section>
