<script lang="ts">
  import { onMount } from 'svelte'
  import { messageOf } from '../lib/errors'
  import { formatBytes, formatDateTime } from '../lib/format'
  import { exportData, loadSystem } from '../lib/owner'
  import type { SystemInfo } from '../lib/types'

  let info = $state<SystemInfo | null>(null)
  let loading = $state(true)
  let error = $state('')
  let notice = $state('')
  let exporting = $state(false)

  onMount(async () => {
    try {
      info = await loadSystem()
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      loading = false
    }
  })

  async function download() {
    exporting = true
    error = ''
    notice = ''
    try {
      const data = await exportData()
      const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' })
      const url = URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.href = url
      link.download = `sesame-export-${new Date().toISOString().slice(0, 10)}.json`
      document.body.append(link)
      link.click()
      link.remove()
      window.setTimeout(() => URL.revokeObjectURL(url), 10_000)
      notice = 'The export was downloaded.'
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      exporting = false
    }
  }
</script>

<section class="view">
  <div class="page-head">
    <div>
      <h1>System</h1>
    </div>
  </div>
  {#if error}<p class="message error" role="alert">{error}</p>{/if}
  {#if loading}
    <div class="spinner" role="status" aria-label="Loading"></div>
  {:else if info}
    {#each info.warnings as warning (warning)}<p class="message warn" role="status">{warning}</p>{/each}
    <dl class="facts">
      <div><dt>Version</dt><dd>{info.version}{info.commit && info.commit !== 'unknown' ? `, commit ${info.commit.slice(0, 7)}` : ''}</dd></div>
      <div><dt>Database schema</dt><dd>{info.schemaVersion}</dd></div>
      <div><dt>Database size</dt><dd>{formatBytes(info.databaseBytes)}</dd></div>
      <div><dt>Last backup</dt><dd>{info.lastBackupAt ? formatDateTime(info.lastBackupAt) : 'No backup yet'}</dd></div>
      <div><dt>Audit chain</dt><dd class:bad={!info.auditChainOk}>{info.auditChainOk ? 'Intact' : 'Broken'}</dd></div>
    </dl>
  {/if}
  <div class="section">
    <h2>Export</h2>
    <p>Members, devices and the audit log, as JSON.</p>
    {#if notice}<p class="message success" role="status">{notice}</p>{/if}
    <button type="button" onclick={download} disabled={exporting}>Export data</button>
  </div>
</section>
