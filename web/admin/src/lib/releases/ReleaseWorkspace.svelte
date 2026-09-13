<script lang="ts">
  import type { Release } from '../types'

  type Command = 'publish' | 'rollout' | 'emergency-stop' | 'withdraw'

  const props = $props<{ releases: Release[]; canManage: boolean; onCommand: (release: Release, command: Command) => void }>()
  let pending = $state<{ releaseID: string; command: Command } | null>(null)

  function platformLabel(platform: string) {
    return platform === 'linux' ? 'Linux' : 'Windows'
  }

  function commandLabel(command: Command) {
    return command === 'emergency-stop' ? 'emergency stop' : command.replace('-', ' ')
  }

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

<div class="release-stack">
  {#each props.releases as release (release.id)}
    <article class="panel release-card">
      <header class="release-card-head">
        <div class="release-card-title">
          <strong>Sesame {release.version}</strong>
          <span class="badge" data-status={release.status}>{release.status}</span>
        </div>
        <div class="release-card-meta">
          <span>{platformLabel(release.platform)}</span>
          <span>{release.channel} channel</span>
          <span>Revision {release.manifestRevision}</span>
        </div>
      </header>
      <div class="release-card-body">
        <section>
          <h3>Verified artifact set</h3>
          {#if release.artifacts.length > 0}
            <dl>
              {#each release.artifacts as artifact (artifact.id)}
                <div><dt>{artifact.format} ({artifact.architecture})</dt><dd><code>{artifact.sha256}</code>{artifact.updaterCapable ? ' · updater capable' : ''}</dd></div>
              {/each}
            </dl>
          {:else}
            <p class="empty">No eligible artifact evidence.</p>
          {/if}
          {#if release.audit.length}
            <h3>Recent activity</h3>
            <ul class="release-activity">
              {#each release.audit as entry (entry.id)}
                <li>{entry.action.replace('release.', '').replaceAll('_', ' ')} by {entry.adminEmail || 'release pipeline'}</li>
              {/each}
            </ul>
          {/if}
        </section>
        {#if props.canManage}
          <section class="release-actions-area">
            <h3>Actions</h3>
            {#if release.publicationBlockers.length > 0}
              <p class="release-blockers">Publishing needs {release.publicationBlockers.join(', ')}.</p>
            {/if}
            {#if release.status === 'draft'}
              <div class="release-actions">
                <button class="primary" onclick={() => requestCommand(release, 'publish')} disabled={release.publicationBlockers.length > 0}>Publish</button>
              </div>
            {:else if release.status === 'published'}
              <div class="release-rollout">
                <label>Rollout percentage<input type="number" min="0" max="100" bind:value={release.rolloutPercent} /></label>
                <button onclick={() => requestCommand(release, 'rollout')}>Set rollout</button>
              </div>
              <div class="release-actions">
                <button class="danger" onclick={() => requestCommand(release, 'emergency-stop')}>Emergency stop</button>
                <button class="danger" onclick={() => requestCommand(release, 'withdraw')}>Withdraw</button>
              </div>
            {:else}
              <p class="empty">No commands for a release in this state.</p>
            {/if}
            {#if pending?.releaseID === release.id}
              <div class="confirm-strip">
                <p>Confirm {commandLabel(pending?.command ?? 'publish')} for {release.version}. This is written to the audit log.</p>
                <div class="release-actions">
                  <button class="danger" onclick={() => confirm(release)}>Confirm</button>
                  <button onclick={() => (pending = null)}>Cancel</button>
                </div>
              </div>
            {/if}
          </section>
        {/if}
      </div>
    </article>
  {/each}
  {#if props.releases.length === 0}<p class="empty">No verified release candidates have been accepted yet.</p>{/if}
</div>
