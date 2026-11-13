<script lang="ts">
  import { onMount } from 'svelte'
  import { APIError } from '../lib/api'
  import { messageOf } from '../lib/errors'
  import { formatBytes, formatDateTime } from '../lib/format'
  import { exportData, loadDiagnostics, loadSystem } from '../lib/owner'
  import { limitationLabel, parseDiagnosticsReport, STATE_LABELS, visibleWarnings, type DiagnosticsReport } from '../lib/system'
  import type { SystemInfo } from '../lib/types'

  let info = $state<SystemInfo | null>(null)
  let loading = $state(true)
  let error = $state('')
  let notice = $state('')
  let exporting = $state(false)
  let diagnostics = $state<DiagnosticsReport | null>(null)
  let diagnosticsLoading = $state(true)
  let diagnosticsNote = $state('')

  const warnings = $derived(info ? visibleWarnings(info, diagnostics) : [])

  async function loadFacts() {
    try {
      info = await loadSystem()
    } catch (reason) {
      error = messageOf(reason)
    } finally {
      loading = false
    }
  }

  async function loadDiagnosticReport() {
    try {
      const parsed = parseDiagnosticsReport(await loadDiagnostics())
      if (parsed) {
        diagnostics = parsed
      } else {
        diagnosticsNote = 'The diagnostics report could not be read. The system information is still shown.'
      }
    } catch (reason) {
      diagnosticsNote = reason instanceof APIError && reason.status === 404
        ? 'This server does not provide the diagnostics report.'
        : 'The diagnostics report could not be loaded. The system information is still shown.'
    } finally {
      diagnosticsLoading = false
    }
  }

  onMount(() => {
    void loadFacts()
    void loadDiagnosticReport()
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
    {#each warnings as warning, index (index)}<p class="message warn" role="status">{warning}</p>{/each}
    <dl class="facts">
      <div><dt>Version</dt><dd>{info.version}{info.commit && info.commit !== 'unknown' ? `, commit ${info.commit.slice(0, 7)}` : ''}</dd></div>
      <div><dt>Database schema</dt><dd>{info.schemaVersion}</dd></div>
      <div><dt>Database size</dt><dd>{formatBytes(info.databaseBytes)}</dd></div>
      <div><dt>Last backup</dt><dd>{info.lastBackupAt ? formatDateTime(info.lastBackupAt) : 'No backup yet'}</dd></div>
      <div><dt>Audit chain</dt><dd class:bad={!info.auditChainOk}>{info.auditChainOk ? 'Intact' : 'Broken'}</dd></div>
    </dl>
  {/if}
  <div class="section diagnostics">
    <h2>Diagnostics</h2>
    <p>The server checks its own configuration and recorded history. It does not test certificates, reachability, proxy paths, host configuration or backups stored elsewhere.</p>
    {#if diagnosticsLoading}
      <p class="diagnostic-note" role="status">Checking the server diagnostics.</p>
    {:else if diagnostics}
      <p class="diagnostics-overall" data-state={diagnostics.overall} role="status">
        <strong>Overall: {STATE_LABELS[diagnostics.overall]}</strong>
        <small>Generated {formatDateTime(diagnostics.generatedAt)}</small>
      </p>
      <ul class="rows diagnostics-list">
        {#each diagnostics.checks as check (check.id)}
          <li class="row diagnostics-row" data-state={check.state}>
            <div class="row-main">
              <strong>{check.label}</strong>
              <small>{check.summary}</small>
              {#if check.next}<small class="diagnostic-next">{check.next}</small>{/if}
            </div>
            <div class="row-side">
              <span class="diagnostic-state" data-state={check.state}>{STATE_LABELS[check.state]}</span>
            </div>
          </li>
        {/each}
      </ul>
      {#if diagnostics.limitations.length > 0}
        <div class="diagnostic-limits">
          <h3>Limits of this check</h3>
          <ul>
            {#each diagnostics.limitations as limitation, index (index)}
              <li>{limitationLabel(limitation)}</li>
            {/each}
          </ul>
        </div>
      {/if}
    {:else}
      <p class="diagnostic-note" role="status">{diagnosticsNote}</p>
    {/if}
    <p class="diagnostic-command">For a wider offline check, run <code>docker compose exec -T sesame /sesame-server doctor --json</code> on the host.</p>
  </div>
  <div class="section">
    <h2>Export</h2>
    <p>The export holds the members, the devices and the audit log as JSON.</p>
    {#if notice}<p class="message success" role="status">{notice}</p>{/if}
    <button type="button" onclick={download} disabled={exporting}>Export data</button>
  </div>
</section>
