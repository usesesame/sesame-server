<script lang="ts">
  import type { Release } from '../types'

  type Command = 'publish' | 'rollout' | 'emergency-stop' | 'withdraw'

  const props = $props<{ releases: Release[]; canManage: boolean; onCommand: (release: Release, command: Command) => void }>()
  let pending = $state<{ releaseID: string; command: Command } | null>(null)

  function requestCommand(release: Release, command: Command) {
    if (command === 'rollout') {
      props.onCommand(release, command)
      return
    }
    pending = { releaseID: release.id, command }
  }

  function confirm(release: Release) {
    if (!pending || pending.releaseID !== release.id) return
    props.onCommand(release, pending.command)
    pending = null
  }
</script>

<section class="panel">
  <h2>Desktop releases</h2>
  {#each props.releases as release (release.id)}
    <div class="release-edit">
      <div class="two"><label>Version<input value={release.version} readonly /></label><label>Status<input value={release.status} readonly /></label></div>
      <div class="two"><label>Platform<input value={release.platform} readonly /></label><label>Channel<input value={release.channel} readonly /></label></div>
      {#if release.artifact}<div class="release-evidence"><strong>Verified artifact</strong><dl><div><dt>SHA-256</dt><dd><code>{release.artifact.sha256}</code></dd></div><div><dt>Distribution</dt><dd>{release.artifact.distributionClass}</dd></div></dl></div>{:else}<p class="empty">No eligible artifact evidence.</p>{/if}
      <p>Revision {release.manifestRevision}</p>
      {#if release.audit.length}<h3>Recent release activity</h3><ul>{#each release.audit as entry (entry.id)}<li>{entry.action.replace('release.', '').replaceAll('_', ' ')} by {entry.adminEmail || 'release pipeline'}</li>{/each}</ul>{/if}
      {#if props.canManage}
        {#if release.publicationBlockers.length > 0}<p class="release-blockers">Publishing needs {release.publicationBlockers.join(', ')}.</p>{/if}
        {#if release.status === 'draft'}
          <button class="primary" onclick={() => requestCommand(release, 'publish')} disabled={release.publicationBlockers.length > 0}>Publish</button>
        {:else if release.status === 'published'}
          <label>Rollout percentage<input type="number" min="0" max="100" bind:value={release.rolloutPercent} /></label>
          <div class="toolbar"><button onclick={() => requestCommand(release, 'rollout')}>Set rollout</button><button class="danger" onclick={() => requestCommand(release, 'emergency-stop')}>Emergency stop</button><button class="danger" onclick={() => requestCommand(release, 'withdraw')}>Withdraw</button></div>
        {/if}
        {#if pending?.releaseID === release.id}<div class="release-blockers"><p>Confirm {pending?.command?.replace('-', ' ')} for {release.version}.</p><button class="danger" onclick={() => confirm(release)}>Confirm</button><button onclick={() => pending = null}>Cancel</button></div>{/if}
      {/if}
    </div>
  {/each}
  {#if props.releases.length === 0}<p class="empty">No verified release candidates have been accepted yet.</p>{/if}
</section>
