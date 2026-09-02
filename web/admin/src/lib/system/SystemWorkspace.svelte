<script lang="ts">
  import type { OperationalSnapshot, OperationalStatus } from '../types'

  type Failure = '' | 'unavailable' | 'unauthorized'
  type State = { label: string; status: OperationalStatus; detail: string; action: string }

  const props = $props<{ snapshot: OperationalSnapshot | null; failure: Failure }>()

  function statusText(status: OperationalStatus) {
    return status.replace('_', ' ')
  }

  function maintenanceAge(value?: string) {
    if (!value) return 'Never run'
    const elapsedMinutes = Math.max(0, Math.floor((Date.now() - new Date(value).getTime()) / 60000))
    if (elapsedMinutes < 60) return `${elapsedMinutes} minute${elapsedMinutes === 1 ? '' : 's'} ago`
    const elapsedHours = Math.floor(elapsedMinutes / 60)
    if (elapsedHours < 48) return `${elapsedHours} hour${elapsedHours === 1 ? '' : 's'} ago`
    const elapsedDays = Math.floor(elapsedHours / 24)
    return `${elapsedDays} day${elapsedDays === 1 ? '' : 's'} ago`
  }

  function states(snapshot: OperationalSnapshot): State[] {
    return [
      { label: 'API', status: snapshot.api.status, detail: 'Administration API', action: 'Check API readiness and recent service logs.' },
      { label: 'Database', status: snapshot.database.status, detail: snapshot.database.timedOut ? 'Readiness check timed out' : 'PostgreSQL readiness', action: 'Check PostgreSQL availability from protected operations.' },
      { label: 'Schema', status: snapshot.schema.status, detail: snapshot.schema.version ? `Migration ${snapshot.schema.version}` : 'Schema version unavailable', action: 'Check database readiness and the migration job.' },
      { label: 'Release pipeline', status: snapshot.releasePipeline.status, detail: 'Candidate verification', action: 'Configure release verification in the protected server environment.' },
      { label: 'Artifact delivery', status: snapshot.artifactDelivery.status, detail: 'Signed download delivery', action: 'Check protected artifact storage and delivery configuration.' },
    ]
  }

  function needsAttention(snapshot: OperationalSnapshot) {
    return states(snapshot).some((state) => state.status !== 'ready')
      || snapshot.emailOutbox.status !== 'ready'
      || snapshot.maintenance.status !== 'ready'
  }
</script>

{#if props.failure === 'unauthorized'}
  <section class="panel system-message" role="alert">
    <h2>System access changed</h2>
    <p>Your session no longer has permission to view operational status. Sign in again or ask an administrator to review your role.</p>
  </section>
{:else if props.failure === 'unavailable'}
  <section class="panel system-message" role="alert">
    <h2>System status is unavailable</h2>
    <p>The operational snapshot could not be loaded. Check API readiness, then try again.</p>
  </section>
{:else if !props.snapshot}
  <p class="empty" role="status">Loading system status.</p>
{:else}
  <p class="system-summary" role="status" data-health={needsAttention(props.snapshot) ? 'attention' : 'ready'}>
    {needsAttention(props.snapshot) ? 'System needs attention.' : 'System is operating normally.'}
  </p>

  <div class="system-grid">
    <section class="panel system-panel">
      <h2>Service identity</h2>
      <dl>
        <div><dt>Version</dt><dd>{props.snapshot.version.version || 'Unavailable'}</dd></div>
        <div><dt>Commit</dt><dd><code>{props.snapshot.version.commit || 'Unavailable'}</code></dd></div>
      </dl>
    </section>

    <section class="panel system-panel">
      <h2>Email queue</h2>
      <span class="system-status" data-health={props.snapshot.emailOutbox.status}>{statusText(props.snapshot.emailOutbox.status)}</span>
      <p>{props.snapshot.emailOutbox.pending} pending, {props.snapshot.emailOutbox.failed} failed</p>
      {#if props.snapshot.emailOutbox.status !== 'ready'}<p class="system-action">Check the email worker and failed deliveries.</p>{/if}
    </section>

    <section class="panel system-panel">
      <h2>Maintenance</h2>
      <span class="system-status" data-health={props.snapshot.maintenance.status}>{statusText(props.snapshot.maintenance.status)}</span>
      <p>Last ran {maintenanceAge(props.snapshot.maintenance.lastRunAt)}</p>
      {#if props.snapshot.maintenance.status !== 'ready'}<p class="system-action">Check the latest maintenance job result.</p>{/if}
    </section>
  </div>

  <section class="panel system-panel">
    <h2>Dependencies</h2>
    {#each states(props.snapshot) as state (state.label)}
      <div class="system-row" data-health={state.status}>
        <div><strong>{state.label}</strong><small>{state.detail}</small></div>
        <span class="system-status">{statusText(state.status)}</span>
        {#if state.status !== 'ready'}<p>{state.action}</p>{/if}
      </div>
    {/each}
  </section>

  <section class="panel system-panel">
    <h2>Deployment</h2>
    <p>Deployment is managed through protected automation. This page is read-only.</p>
  </section>
{/if}
