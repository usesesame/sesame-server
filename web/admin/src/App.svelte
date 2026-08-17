<script lang="ts">
  import { onMount } from 'svelte'
  import { apiURL, mutate, request } from './lib/api'
  import { TICKET_CATEGORY_LABELS } from './lib/types'
  import type { AdminAccount, AuditEntry, Flag, Overview, Plan, RateMetric, Release, Role, TicketDetail, TicketNote, TicketSummary, TicketStatus, TicketPriority, User } from './lib/types'

  type Page = 'overview' | 'support' | 'users' | 'flags' | 'releases' | 'plans' | 'admins' | 'audit' | 'system'
  const pageNames: Record<Page, string> = { overview: 'Overview', support: 'Support', users: 'Users', flags: 'Feature flags', releases: 'Releases', plans: 'Product plans', admins: 'Administrators', audit: 'Audit log', system: 'System' }
  const roles: Role[] = ['super', 'support', 'ops', 'billing', 'readonly']
  let me: AdminAccount | null = null
  let loading = true
  let page: Page = 'overview'
  let error = ''
  let notice = ''
  let email = ''
  let password = ''
  let code = ''
  let setupToken = ''
  let setupSecret = ''
  let setupURI = ''
  let setupEmail = ''
  let overview: Overview | null = null
  let users: User[] = []
  let userTotal = 0
  let userQuery = ''
  let selectedUser: User | null = null
  let flags: Flag[] = []
  let plans: Plan[] = []
  let releases: Release[] = []
  let admins: AdminAccount[] = []
  let audit: AuditEntry[] = []
  let auditAction = ''
  let auditAdmin = ''
  let auditFrom = ''
  let auditTo = ''
  let metrics: RateMetric[] = []
  let system: Record<string, unknown> | null = null
  let inviteEmail = ''
  let inviteRole: Role = 'support'
  let inviteURL = ''
  let busy = false
  let tickets: TicketSummary[] = []
  let ticketTotal = 0
  let ticketStatusFilter = ''
  let ticketPriorityFilter = ''
  let ticketCategoryFilter = ''
  let ticketAssignedFilter = ''
  let ticketQuery = ''
  let selectedTicket: TicketDetail | null = null
  let replyBody = ''
  let noteBody = ''
  const PAGE_SIZE = 100
  let usersPage = 1
  let ticketsPage = 1
  let assignees: { id: string; email: string }[] = []
  let assignWorking = false
  let userSearchTimer: ReturnType<typeof setTimeout> | undefined
  let ticketSearchTimer: ReturnType<typeof setTimeout> | undefined

  $: isSetup = setupToken.length > 0
  $: canUsers = me?.role === 'super' || me?.role === 'support' || me?.role === 'billing' || me?.role === 'readonly'
  $: canFlags = me?.role === 'super' || me?.role === 'ops' || me?.role === 'readonly'
  $: canPlans = me?.role === 'super' || me?.role === 'billing' || me?.role === 'readonly'
  $: canViewAdmins = me?.role === 'super' || me?.role === 'readonly'
  $: canEditAdmins = me?.role === 'super'
  $: canAuditAll = me?.role === 'super' || me?.role === 'readonly'
  $: canSystem = me?.role === 'super' || me?.role === 'ops' || me?.role === 'readonly'
  $: canEditUsers = me?.role === 'super' || me?.role === 'support'
  $: canEditFlags = me?.role === 'super' || me?.role === 'ops'
  $: canEditPlans = me?.role === 'super' || me?.role === 'billing'
  $: canSupport = me?.role === 'super' || me?.role === 'support'
  $: canViewSupport = canSupport || me?.role === 'readonly'

  onMount(async () => {
    setupToken = new URLSearchParams(location.search).get('token') || ''
    if (setupToken) {
      try {
        const result = await mutate<{ email: string; secret: string; uri: string }>('/v1/admin/auth/setup/begin', 'POST', { token: setupToken })
        setupEmail = result.email; setupSecret = result.secret; setupURI = result.uri
      } catch (reason) { showError(reason) }
      loading = false
      return
    }
    try {
      me = (await request<{ admin: AdminAccount }>('/v1/admin/auth/me')).admin
      await openPage('overview')
    } catch { me = null }
    loading = false
  })

  function showError(reason: unknown) { error = reason instanceof Error ? reason.message : 'The request failed.'; notice = '' }
  function showNotice(message: string) { notice = message; error = '' }

  async function login() {
    busy = true; error = ''
    try {
      me = (await mutate<{ admin: AdminAccount }>('/v1/admin/auth/login', 'POST', { email, password, code })).admin
      password = ''; code = ''; await openPage('overview')
    } catch (reason) { showError(reason) } finally { busy = false }
  }

  async function completeSetup() {
    busy = true; error = ''
    try {
      me = (await mutate<{ admin: AdminAccount }>('/v1/admin/auth/setup/complete', 'POST', { token: setupToken, password, code })).admin
      history.replaceState({}, '', '/'); setupToken = ''; password = ''; code = ''; await openPage('overview')
    } catch (reason) { showError(reason) } finally { busy = false }
  }

  async function logout() { await mutate('/v1/admin/auth/logout', 'POST'); me = null; page = 'overview' }

  async function openPage(next: Page) {
    page = next; selectedUser = null; error = ''; notice = ''
    try {
      if (next === 'overview') overview = (await request<{ overview: Overview }>('/v1/admin/overview')).overview
      if (next === 'users') await loadUsers()
      if (next === 'support') { await loadTickets(); void loadAssignees() }
      if (next === 'flags') flags = (await request<{ flags: Flag[] }>('/v1/admin/flags')).flags
      if (next === 'plans') plans = (await request<{ plans: Plan[] }>('/v1/admin/plans')).plans
      if (next === 'releases') releases = (await request<{ releases: Release[] }>('/v1/admin/releases')).releases
      if (next === 'admins') admins = (await request<{ admins: AdminAccount[] }>('/v1/admin/admins')).admins
      if (next === 'audit') await loadAudit()
      if (next === 'system') {
        system = await request('/v1/admin/system/config')
        metrics = (await request<{ metrics: RateMetric[] }>('/v1/admin/system/rate-limits')).metrics
      }
    } catch (reason) { showError(reason) }
  }

  async function loadUsers() {
    const result = await request<{ users: User[]; total: number }>(`/v1/admin/users?query=${encodeURIComponent(userQuery)}&size=${PAGE_SIZE}&page=${usersPage}`)
    users = result.users; userTotal = result.total
  }

  function ticketQueryParams() {
    const params = new URLSearchParams({ size: String(PAGE_SIZE), page: String(ticketsPage) })
    if (ticketStatusFilter) params.set('status', ticketStatusFilter)
    if (ticketPriorityFilter) params.set('priority', ticketPriorityFilter)
    if (ticketCategoryFilter) params.set('category', ticketCategoryFilter)
    if (ticketAssignedFilter) params.set('assigned', ticketAssignedFilter)
    if (ticketQuery) params.set('query', ticketQuery)
    return params.toString()
  }

  async function loadTickets() {
    const result = await request<{ tickets: TicketSummary[]; total: number }>(`/v1/admin/support?${ticketQueryParams()}`)
    tickets = result.tickets; ticketTotal = result.total
  }

  function usersPages() { return Math.max(1, Math.ceil(userTotal / PAGE_SIZE)) }
  function ticketsPages() { return Math.max(1, Math.ceil(ticketTotal / PAGE_SIZE)) }
  function prevUsersPage() { if (usersPage > 1) { usersPage -= 1; void loadUsers() } }
  function nextUsersPage() { if (usersPage < usersPages()) { usersPage += 1; void loadUsers() } }
  function prevTicketsPage() { if (ticketsPage > 1) { ticketsPage -= 1; void loadTickets() } }
  function nextTicketsPage() { if (ticketsPage < ticketsPages()) { ticketsPage += 1; void loadTickets() } }

  function onUserSearch() {
    clearTimeout(userSearchTimer)
    userSearchTimer = setTimeout(() => {
      if (usersPage !== 1) usersPage = 1
      void loadUsers()
    }, 350)
  }

  function onTicketSearch() {
    clearTimeout(ticketSearchTimer)
    ticketSearchTimer = setTimeout(() => {
      if (ticketsPage !== 1) ticketsPage = 1
      void loadTickets()
    }, 350)
  }

  async function loadAssignees() {
    try { assignees = (await request<{ assignees: { id: string; email: string }[] }>('/v1/admin/support/assignees')).assignees } catch { /* the readout falls back to raw ids if the list is unavailable */ }
  }

  function assigneeEmail(id?: string) { return id ? (assignees.find((assignee) => assignee.id === id)?.email || id) : '' }

  async function setTicketAssignment(adminId: string) {
    if (!selectedTicket) return
    assignWorking = true
    try {
      await mutate(`/v1/admin/support/${selectedTicket.id}/assign`, 'POST', { adminId })
      await loadTickets(); await inspectTicket(selectedTicket.id)
      showNotice(adminId ? 'Ticket assigned.' : 'Ticket unassigned.')
    } catch (reason) { showError(reason) } finally { assignWorking = false }
  }

  async function inspectTicket(id: string) {
    try { selectedTicket = (await request<{ ticket: TicketDetail }>(`/v1/admin/support/${id}`)).ticket } catch (reason) { showError(reason) }
  }

  async function sendReply() {
    if (!selectedTicket || !replyBody.trim()) return
    busy = true
    try {
      const result = await mutate<{ ticket: TicketDetail }>(`/v1/admin/support/${selectedTicket.id}/reply`, 'POST', { body: replyBody })
      selectedTicket = result.ticket; replyBody = ''
      await loadTickets(); showNotice('Reply added to the user\'s support portal.')
    } catch (reason) { showError(reason) } finally { busy = false }
  }

  async function saveNote() {
    if (!selectedTicket || !noteBody.trim()) return
    busy = true
    try {
      const result = await mutate<{ note: TicketNote }>(`/v1/admin/support/${selectedTicket.id}/notes`, 'POST', { body: noteBody })
      if (selectedTicket) selectedTicket = { ...selectedTicket, notes: [...selectedTicket.notes, result.note] }
      noteBody = ''; showNotice('Internal note added.')
    } catch (reason) { showError(reason) } finally { busy = false }
  }

  async function setTicketStatus(status: TicketStatus) {
    if (!selectedTicket) return
    try { await mutate(`/v1/admin/support/${selectedTicket.id}/status`, 'POST', { status }); await loadTickets(); await inspectTicket(selectedTicket.id); showNotice(`Status changed to ${status}.`) } catch (reason) { showError(reason) }
  }

  async function setTicketPriority(priority: TicketPriority) {
    if (!selectedTicket) return
    try { await mutate(`/v1/admin/support/${selectedTicket.id}/priority`, 'POST', { priority }); await loadTickets(); await inspectTicket(selectedTicket.id); showNotice(`Priority changed to ${priority}.`) } catch (reason) { showError(reason) }
  }

  async function inspectUser(id: string) {
    try { selectedUser = (await request<{ user: User }>(`/v1/admin/users/${id}`)).user } catch (reason) { showError(reason) }
  }

  async function userAction(action: string, method: 'POST' | 'DELETE', body?: unknown) {
    if (!selectedUser) return
    busy = true
    try {
      await mutate(`/v1/admin/users/${selectedUser.id}/${action}`, method, body)
      showNotice('User account updated and the action was audited.')
      await loadUsers(); await inspectUser(selectedUser.id)
    } catch (reason) { showError(reason) } finally { busy = false }
  }

  async function deleteUser() {
    if (!selectedUser || !confirm(`Permanently delete ${selectedUser.email}? This cannot be undone.`)) return
    try { await mutate(`/v1/admin/users/${selectedUser.id}`, 'DELETE'); selectedUser = null; await loadUsers(); showNotice('User account deleted.') } catch (reason) { showError(reason) }
  }

  async function toggleUserSuspension() {
    if (!selectedUser) return
    if (selectedUser.suspendedAt) {
      await userAction('suspend', 'DELETE')
      return
    }
    const reason = prompt('Reason for suspending this account (shown only to staff):', '')
    if (reason === null) return
    await userAction('suspend', 'POST', { reason })
  }

  async function setFlag(flag: Flag, value: string) {
    try { await mutate(`/v1/admin/flags/${flag.key}`, 'PATCH', { value }); await openPage('flags'); showNotice('Feature flag updated live.') } catch (reason) { showError(reason) }
  }

  async function savePlan(plan: Plan) {
    try { await mutate(`/v1/admin/plans/${plan.id}`, 'PATCH', plan); showNotice(`${plan.name} saved.`) } catch (reason) { showError(reason) }
  }

  async function saveRelease(release: Release) {
    try { await mutate(`/v1/admin/releases/${release.platform}`, 'PUT', release); await openPage('releases'); showNotice('Release metadata saved.') } catch (reason) { showError(reason) }
  }

  async function publishToOwnerDevices() {
    if (!selectedUser) return
    try {
      await mutate(`/v1/admin/users/${selectedUser.id}/owner-release`, 'POST')
      showNotice('This verified beta account is now in the owner update ring.')
    } catch (reason) { showError(reason) }
  }


  async function inviteAdmin() {
    try {
      const result = await mutate<{ setupUrl: string }>('/v1/admin/admins', 'POST', { email: inviteEmail, role: inviteRole })
      inviteURL = result.setupUrl; inviteEmail = ''; await openPage('admins'); showNotice('Administrator invited. Share the one-time link through a trusted channel.')
    } catch (reason) { showError(reason) }
  }

  async function updateAdmin(admin: AdminAccount, role = admin.role, suspended = admin.suspended) {
    try { await mutate(`/v1/admin/admins/${admin.id}`, 'PATCH', { role, suspended }); await openPage('admins'); showNotice('Administrator updated.') } catch (reason) { showError(reason) }
  }

  function auditQuery() {
    const query = new URLSearchParams({ size: '100' })
    if (auditAction) query.set('action', auditAction)
    if (auditAdmin && canAuditAll) query.set('admin', auditAdmin)
    if (auditFrom) query.set('from', new Date(auditFrom).toISOString())
    if (auditTo) query.set('to', new Date(auditTo).toISOString())
    return query.toString()
  }

  async function loadAudit() {
    try { audit = (await request<{ entries: AuditEntry[] }>(`${canAuditAll ? '/v1/admin/audit' : '/v1/admin/audit/me'}?${auditQuery()}`)).entries } catch (reason) { showError(reason) }
  }

  async function deleteAdmin(admin: AdminAccount) {
    if (!confirm(`Delete the administrator ${admin.email}? Their audit entries will remain.`)) return
    try { await mutate(`/v1/admin/admins/${admin.id}`, 'DELETE'); await openPage('admins'); showNotice('Administrator deleted.') } catch (reason) { showError(reason) }
  }

  async function exportAudit() {
    try {
      const response = await fetch(`${apiURL}/v1/admin/audit/export?${auditQuery()}`, { credentials: 'include' })
      if (!response.ok) throw new Error('The audit export could not be created.')
      const objectURL = URL.createObjectURL(await response.blob())
      const link = document.createElement('a'); link.href = objectURL; link.download = 'sesame-admin-audit.csv'; link.click()
      URL.revokeObjectURL(objectURL)
    } catch (reason) { showError(reason) }
  }

  function date(value?: string) { return value ? new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : 'Never' }
</script>

{#if loading}
  <main class="center"><span class="spinner" aria-label="Loading"></span></main>
{:else if !me}
  <main class="auth-shell">
    <section class="auth-panel">
      <div class="brand"><img src="/favicon.svg" alt="" /><span>Sesame</span><small>Administration</small></div>
      {#if isSetup}
        <h1>Secure your admin account</h1><p>Use an authenticator app to add this key, then confirm a current code. MFA is required for every administrator.</p>
        <label>Admin email<input value={setupEmail} disabled /></label>
        <label>Authenticator secret<input value={setupSecret} readonly onfocus={(event) => event.currentTarget.select()} /></label>
        <details><summary>Authenticator URI</summary><code class="wrap">{setupURI}</code></details>
        <label>New password<input type="password" bind:value={password} minlength="12" maxlength="1024" autocomplete="new-password" /></label>
        <label>Six-digit code<input bind:value={code} inputmode="numeric" pattern="[0-9]{6}" maxlength="6" autocomplete="one-time-code" /></label>
        <button class="primary" onclick={completeSetup} disabled={busy || password.length < 12 || code.length !== 6}>Finish setup</button>
      {:else}
        <h1>Admin sign in</h1><p>This is separate from a Sesame website account. Password and MFA are both required.</p>
        <label>Email<input type="email" bind:value={email} autocomplete="username" /></label>
        <label>Password<input type="password" bind:value={password} autocomplete="current-password" /></label>
        <label>Six-digit code<input bind:value={code} inputmode="numeric" pattern="[0-9]{6}" maxlength="6" autocomplete="one-time-code" /></label>
        <button class="primary" onclick={login} disabled={busy || !email || !password || code.length !== 6}>Sign in</button>
      {/if}
      {#if error}<p class="message error" role="alert">{error}</p>{/if}
    </section>
  </main>
{:else}
  <div class="app-shell">
    <aside class="sidebar">
      <div class="brand"><img src="/favicon.svg" alt="" /><span>Sesame</span><small>Administration</small></div>
      <nav aria-label="Admin sections">
        <button class:active={page === 'overview'} onclick={() => openPage('overview')}>Overview</button>
        {#if canViewSupport}<button class:active={page === 'support'} onclick={() => openPage('support')}>Support</button>{/if}
        {#if canUsers}<button class:active={page === 'users'} onclick={() => openPage('users')}>Users</button>{/if}
        {#if canFlags}<button class:active={page === 'flags'} onclick={() => openPage('flags')}>Feature flags</button>{/if}
        {#if canFlags}<button class:active={page === 'releases'} onclick={() => openPage('releases')}>Releases</button>{/if}
        {#if canPlans}<button class:active={page === 'plans'} onclick={() => openPage('plans')}>Product plans</button>{/if}
        {#if canViewAdmins}<button class:active={page === 'admins'} onclick={() => openPage('admins')}>Administrators</button>{/if}
        <button class:active={page === 'audit'} onclick={() => openPage('audit')}>Audit log</button>
        {#if canSystem}<button class:active={page === 'system'} onclick={() => openPage('system')}>System</button>{/if}
      </nav>
      <div class="admin-self"><strong>{me.email}</strong><span>{me.role}</span><button onclick={logout}>Sign out</button></div>
    </aside>
    <main class="content">
      <header class="page-head"><h1>{pageNames[page]}</h1></header>
      {#if error}<p class="message error" role="alert">{error}</p>{/if}
      {#if notice}<p class="message success" role="status">{notice}</p>{/if}

      {#if page === 'overview'}
        <div class="metric-grid">
          {#each [['Users', overview?.users], ['New this week', overview?.newUsersThisWeek], ['Beta grants', overview?.betaUsers], ['Unverified', overview?.unverifiedUsers], ['Suspended', overview?.suspendedUsers], ['Admin sessions', overview?.activeAdminSessions], ['Open tickets', overview?.openTickets], ['Unassigned', overview?.unassignedTickets], ['Urgent', overview?.urgentTickets]] as metric (metric[0])}
            <article><span>{metric[0]}</span><strong>{metric[1] ?? 'Not available'}</strong></article>
          {/each}
        </div>
        <section class="panel"><h2>Operating boundary</h2><p>Administration manages account metadata, releases, plans, flags and sessions. It cannot receive vault records, passwords stored in a vault, TOTP seeds, backup codes or vault keys.</p></section>
      {:else if page === 'support'}
        <div class="toolbar">
          <input class="search" aria-label="Search tickets" placeholder="Search email or subject" bind:value={ticketQuery} oninput={onTicketSearch} />
          <select bind:value={ticketStatusFilter} onchange={loadTickets}><option value="">All statuses</option><option value="open">Open</option><option value="in_progress">In progress</option><option value="waiting">Waiting</option><option value="closed">Closed</option></select>
          <select bind:value={ticketPriorityFilter} onchange={loadTickets}><option value="">All priorities</option><option value="urgent">Urgent</option><option value="high">High</option><option value="normal">Normal</option><option value="low">Low</option></select>
          <select bind:value={ticketCategoryFilter} onchange={loadTickets}><option value="">All categories</option>{#each Object.entries(TICKET_CATEGORY_LABELS) as [value, label] (value)}<option {value}>{label}</option>{/each}</select>
          <select bind:value={ticketAssignedFilter} onchange={loadTickets}><option value="">All assignments</option><option value="unassigned">Unassigned</option><option value={me?.id}>Assigned to me</option></select>
          <span>{ticketTotal} tickets{#if ticketsPages() > 1} · page {ticketsPage} of {ticketsPages()}{/if}</span>
          {#if ticketsPages() > 1}<button class="page-button" onclick={prevTicketsPage} disabled={ticketsPage <= 1}>Prev</button><button class="page-button" onclick={nextTicketsPage} disabled={ticketsPage >= ticketsPages()}>Next</button>{/if}
        </div>
        <div class="split-view support-split" class:has-detail={selectedTicket}>
          <section class="table-panel">
            <table>
              <thead><tr><th>Subject</th><th>Requester</th><th>Category</th><th>Status</th><th>Priority</th><th>SLA</th><th>Updated</th></tr></thead>
              <tbody>
                {#each tickets as ticket (ticket.id)}
                  <tr tabindex="0" class:active={selectedTicket?.id === ticket.id} onclick={() => inspectTicket(ticket.id)} onkeydown={(event) => event.key === 'Enter' && inspectTicket(ticket.id)}>
                    <td><strong>{ticket.subject}</strong><small>{ticket.messageCount} message{ticket.messageCount === 1 ? '' : 's'}</small></td>
                    <td>{ticket.email}</td>
                    <td><span class="badge badge-neutral">{TICKET_CATEGORY_LABELS[ticket.category]}</span></td>
                    <td><span class="badge" data-status={ticket.status}>{ticket.status.replace('_', ' ')}</span></td>
                    <td><span class="badge" data-priority={ticket.priority}>{ticket.priority}</span></td>
                    <td><span class:overdue={ticket.slaBreached}>{ticket.firstResponseAt ? 'Met' : ticket.slaBreached ? 'Overdue' : `Due ${date(ticket.slaDueAt)}`}</span></td>
                    <td>{date(ticket.updatedAt)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
            {#if tickets.length === 0}<p class="empty">No tickets match these filters.</p>{/if}
          </section>
          {#if selectedTicket}
            <aside class="detail support-detail">
              <div class="detail-head"><h2>{selectedTicket.subject}</h2><button aria-label="Close details" onclick={() => selectedTicket = null}>×</button></div>
              <dl>
                <div><dt>Requester</dt><dd>{selectedTicket.email}</dd></div>
                <div><dt>Assigned to</dt><dd>{assigneeEmail(selectedTicket.assignedAdminId) || 'Unassigned'}</dd></div>
                <div><dt>Category</dt><dd>{TICKET_CATEGORY_LABELS[selectedTicket.category]}</dd></div>
                {#if selectedTicket.accountId}<div><dt>Account</dt><dd>{selectedTicket.linkedDevices?.length ? `Linked desktop: ${selectedTicket.linkedDevices.map((device) => device.name).join(', ')}` : 'Signed in, no desktop currently linked'}</dd></div>{:else}<div><dt>Account</dt><dd>Guest request</dd></div>{/if}
                <div><dt>Created</dt><dd>{date(selectedTicket.createdAt)}</dd></div>
                <div><dt>Updated</dt><dd>{date(selectedTicket.updatedAt)}</dd></div>
                {#if selectedTicket.appVersion}<div><dt>App version</dt><dd>{selectedTicket.appVersion}</dd></div>{/if}
                {#if selectedTicket.diagnosticCode}<div><dt>Diagnostic</dt><dd><code>{selectedTicket.diagnosticCode}</code></dd></div>{/if}
                {#if selectedTicket.browserIntegration}<div><dt>Browser integration</dt><dd>{selectedTicket.browserIntegration}</dd></div>{/if}
                {#if selectedTicket.requestId}<div><dt>Request ID</dt><dd><code>{selectedTicket.requestId}</code></dd></div>{/if}
                {#if selectedTicket.firstResponseAt}<div><dt>First response</dt><dd>{date(selectedTicket.firstResponseAt)}</dd></div>{/if}
                <div><dt>First response SLA</dt><dd class:overdue={selectedTicket.slaBreached}>{selectedTicket.firstResponseAt ? 'Met' : selectedTicket.slaBreached ? 'Overdue' : `Due ${date(selectedTicket.slaDueAt)}`}</dd></div>
                {#if selectedTicket.queuePosition}<div><dt>Queue position</dt><dd>{selectedTicket.queuePosition}</dd></div>{/if}
              </dl>
              {#if canSupport}
                <div class="action-grid">
                  <select value={selectedTicket.status} onchange={(event) => setTicketStatus(event.currentTarget.value as TicketStatus)}>
                    <option value="open">Open</option><option value="in_progress">In progress</option><option value="waiting">Waiting</option><option value="closed">Closed</option>
                  </select>
                  <select value={selectedTicket.priority} onchange={(event) => setTicketPriority(event.currentTarget.value as TicketPriority)}>
                    <option value="low">Low</option><option value="normal">Normal</option><option value="high">High</option><option value="urgent">Urgent</option>
                  </select>
                  <select class="assign-select" value={selectedTicket.assignedAdminId || ''} onchange={(event) => setTicketAssignment(event.currentTarget.value)} disabled={assignWorking} aria-label="Assigned administrator">
                    <option value="">Unassigned</option>
                    {#each assignees as assignee (assignee.id)}
                      <option value={assignee.id}>{assignee.email}</option>
                    {/each}
                  </select>
                </div>
              {/if}
              <h3>Conversation</h3>
              <div class="thread">
                {#each selectedTicket.messages as message (message.id)}
                  <article class="message" data-role={message.authorRole}>
                    <div class="message-meta"><strong>{message.authorRole === 'staff' ? message.adminEmail : selectedTicket.email}</strong><small>{date(message.createdAt)}{#if message.sentViaEmail} · email {message.emailDeliveryStatus || 'queueing'}{#if message.emailAttempts} · {message.emailAttempts} attempt{message.emailAttempts === 1 ? '' : 's'}{/if}{#if message.emailNextAttemptAt} · retry {date(message.emailNextAttemptAt)}{/if}{/if}</small></div>
                    <p>{message.body}</p>
                  </article>
                {/each}
              </div>
              {#if canSupport && selectedTicket.status !== 'closed'}
                <div class="reply-composer">
                  <textarea aria-label="Reply to user" bind:value={replyBody} placeholder="Reply to the user. Do not include passwords, codes, or vault data." maxlength="8000"></textarea>
                  <div class="reply-actions">
                    <span class="field-help">Visible in the signed-in support portal. An email is queued only when the account opted in to support replies.</span>
                    <button class="primary" onclick={sendReply} disabled={busy || !replyBody.trim()}>Post reply</button>
                  </div>
                </div>
              {/if}
              <h3>Internal notes</h3>
              <div class="thread">
                {#each selectedTicket.notes as note (note.id)}
                  <article class="message note">
                    <div class="message-meta"><strong>{note.adminEmail}</strong><small>{date(note.createdAt)}</small></div>
                    <p>{note.body}</p>
                  </article>
                {/each}
                {#if selectedTicket.notes.length === 0}<p class="empty">No internal notes.</p>{/if}
              </div>
              {#if canSupport}
                <div class="reply-composer">
                  <textarea aria-label="Internal note" bind:value={noteBody} placeholder="Internal note, visible only to staff." maxlength="4000"></textarea>
                  <div class="reply-actions"><button class="primary" onclick={saveNote} disabled={busy || !noteBody.trim()}>Add note</button></div>
                </div>
              {/if}
            </aside>
          {/if}
        </div>
      {:else if page === 'users'}
        <div class="toolbar"><input class="search" aria-label="Search users" placeholder="Search email" bind:value={userQuery} oninput={onUserSearch} /><span>{userTotal} users{#if usersPages() > 1} · page {usersPage} of {usersPages()}{/if}</span>{#if usersPages() > 1}<button class="page-button" onclick={prevUsersPage} disabled={usersPage <= 1}>Prev</button><button class="page-button" onclick={nextUsersPage} disabled={usersPage >= usersPages()}>Next</button>{/if}</div>
        <div class="split-view"><section class="table-panel"><table><thead><tr><th>Email</th><th>Beta</th><th>Sessions</th><th>Devices</th><th>Status</th></tr></thead><tbody>{#each users as user (user.id)}<tr tabindex="0" onclick={() => inspectUser(user.id)} onkeydown={(event) => event.key === 'Enter' && inspectUser(user.id)}><td><strong>{user.email}</strong><small>{user.emailVerified ? 'Verified' : 'Unverified'}</small></td><td>{user.betaAccess ? user.emailVerified ? 'Eligible' : 'Pending verification' : 'Not granted'}</td><td>{user.sessionCount}</td><td>{user.deviceCount}</td><td>{user.suspendedAt ? 'Suspended' : 'Active'}</td></tr>{/each}</tbody></table></section>
          {#if selectedUser}<aside class="detail"><div class="detail-head"><h2>{selectedUser.email}</h2><button aria-label="Close details" onclick={() => selectedUser = null}>×</button></div><dl><div><dt>Created</dt><dd>{date(selectedUser.createdAt)}</dd></div><div><dt>Email</dt><dd>{selectedUser.emailVerified ? 'Verified' : 'Not verified'}</dd></div></dl>
            {#if selectedUser.betaAccess && !selectedUser.emailVerified}<p class="message error">Beta is granted but inactive until this email address is verified. Downloads and desktop linking remain blocked.</p>{/if}
            {#if canEditUsers}<div class="action-grid"><button onclick={() => userAction('beta', selectedUser!.betaAccess ? 'DELETE' : 'POST')}>{selectedUser.betaAccess ? 'Revoke beta' : 'Grant beta'}</button><button onclick={() => userAction('sessions', 'DELETE')}>Revoke sessions</button><button onclick={toggleUserSuspension}>{selectedUser.suspendedAt ? 'Unsuspend' : 'Suspend'}</button>{#if me.role === 'super'}<button class="danger" onclick={deleteUser}>Delete account</button>{/if}</div>{/if}
            {#if canEditFlags}<div class="owner-release-action"><button class="primary" onclick={publishToOwnerDevices}>Add to owner update ring</button><small>Grants this verified beta account access to owner-channel updates as well as beta updates. It does not publish a release.</small></div>{/if}
            <h3>Website sessions</h3>{#if selectedUser.sessions?.length}{#each selectedUser.sessions as session (session.id)}<div class="compact-row"><div><strong>{session.label}</strong><small>{date(session.lastSeenAt)}</small></div></div>{/each}{:else}<p class="empty">No active sessions.</p>{/if}
            <h3>Connected devices</h3>{#if selectedUser.devices?.length}{#each selectedUser.devices as device (device.id)}<div class="compact-row"><div><strong>{device.name}</strong><small>{date(device.connectedAt)}</small></div>{#if canEditUsers}<button onclick={() => userAction(`devices/${device.id}`, 'DELETE')}>Revoke</button>{/if}</div>{/each}{:else}<p class="empty">No connected devices.</p>{/if}
          </aside>{/if}
        </div>
      {:else if page === 'flags'}
        <section class="panel"><h2>Runtime feature flags</h2><p>Changes take effect without a deployment and are written to the audit log.</p>{#each flags as flag (flag.key)}<div class="setting-row"><div><strong>{flag.key.replaceAll('_', ' ')}</strong><small>Updated {date(flag.updatedAt)}</small></div>{#if flag.key === 'registration_mode'}<select value={flag.value} onchange={(event) => setFlag(flag, event.currentTarget.value)} disabled={!canEditFlags}><option value="closed">Closed</option><option value="invite">Invitation only</option><option value="public">Public</option></select>{:else}<select value={flag.value} onchange={(event) => setFlag(flag, event.currentTarget.value)} disabled={!canEditFlags}><option value="false">Off</option><option value="true">On</option></select>{/if}</div>{/each}</section>
      {:else if page === 'plans'}
        <div class="form-grid">{#each plans as plan (plan.id)}<section class="panel form-card"><h2>{plan.name}</h2><label>Name<input bind:value={plan.name} disabled={!canEditPlans} /></label><div class="two"><label>Price<input bind:value={plan.price} disabled={!canEditPlans} /></label><label>Billing<select bind:value={plan.billing} disabled={!canEditPlans}><option value="none">None</option><option value="one_time">One time</option><option value="monthly">Monthly</option><option value="yearly">Yearly</option></select></label></div><label>Description<textarea bind:value={plan.description} disabled={!canEditPlans}></textarea></label><label class="check"><input type="checkbox" bind:checked={plan.available} disabled={!canEditPlans} /> Available</label>{#if canEditPlans}<button class="primary" onclick={() => savePlan(plan)}>Save plan</button>{/if}</section>{/each}</div>
      {:else if page === 'releases'}
        <section class="panel"><div class="section-head"><div><h2>Windows release manifests</h2><p>Artifacts are accepted only from a cryptographically verified release candidate. Tauri updater verification and exact-workflow Sigstore evidence are mandatory. Authenticode remains mandatory for production, but not for clearly labelled early access.</p></div></div>{#each releases as release (release.id)}<div class="release-edit"><div class="two"><label>Version<input value={release.version} readonly /></label><label>Status<select bind:value={release.status} disabled={!canEditFlags}><option value="draft">Draft</option><option value="published">Published</option><option value="withdrawn">Withdrawn</option></select></label></div><div class="two"><label>Channel<input value={release.channel} readonly /></label><label>Architecture<input value={release.architecture} readonly /></label></div>{#if release.artifact}<div class="release-evidence"><strong>Verified updater artifact</strong><dl><div><dt>SHA-256</dt><dd><code>{release.artifact.sha256}</code></dd></div><div><dt>Updater signing key</dt><dd>{release.artifact.updaterSigningKeyId}</dd></div><div><dt>Distribution</dt><dd>{release.artifact.distributionClass}</dd></div><div><dt>Sigstore publisher</dt><dd>{release.artifact.sigstoreVerified ? 'Exact workflow identity verified' : 'Not verified'}</dd></div><div><dt>Windows publisher</dt><dd>{release.artifact.authenticodeVerified ? `Authenticode verified${release.artifact.authenticodeSubject ? `: ${release.artifact.authenticodeSubject}` : ''}` : 'Unsigned Windows early-access build'}</dd></div><div><dt>Verified</dt><dd>{date(release.artifact.verifiedAt)}</dd></div></dl></div>{:else}<p class="empty">Legacy release without immutable artifact evidence. It cannot be published again.</p>{/if}<div class="two"><label>Supported Windows<input bind:value={release.supportedWindows} disabled={!canEditFlags} /></label><label>Release notes URL<input bind:value={release.releaseNotesUrl} disabled={!canEditFlags} /></label></div><label>Rollback notice<textarea bind:value={release.rollbackNotice} disabled={!canEditFlags}></textarea></label><div class="two"><label>Rollout percentage<input type="number" min="0" max="100" bind:value={release.rolloutPercent} disabled={!canEditFlags} /></label><div class="check-group"><label class="check"><input type="checkbox" bind:checked={release.updateEnabled} disabled={!canEditFlags} /> Update enabled</label><label class="check"><input type="checkbox" bind:checked={release.killSwitch} disabled={!canEditFlags} /> Kill switch</label></div></div>{#if canEditFlags}<button class="primary" onclick={() => saveRelease(release)} disabled={!release.artifact}>Save release controls</button>{/if}</div>{/each}{#if releases.length === 0}<p class="empty">No verified release candidates have been accepted yet.</p>{/if}</section>
      {:else if page === 'admins'}
        {#if canEditAdmins}<section class="panel"><h2>Invite an administrator</h2><div class="toolbar"><input type="email" placeholder="name@example.com" bind:value={inviteEmail} /><select bind:value={inviteRole}>{#each roles as role (role)}<option value={role}>{role}</option>{/each}</select><button class="primary" onclick={inviteAdmin} disabled={!inviteEmail}>Create setup link</button></div>{#if inviteURL}<label>One-time setup link<input value={inviteURL} readonly onfocus={(event) => event.currentTarget.select()} /></label>{/if}</section>{/if}
        <section class="panel"><h2>Administrators</h2>{#each admins as admin (admin.id)}<div class="setting-row"><div><strong>{admin.email}</strong><small>{admin.mfaVerified ? 'MFA verified' : 'Setup pending'} · last sign-in {date(admin.lastLoginAt)}</small></div><select value={admin.role} onchange={(event) => updateAdmin(admin, event.currentTarget.value as Role)} disabled={!canEditAdmins}>{#each roles as role (role)}<option value={role}>{role}</option>{/each}</select>{#if canEditAdmins}<button onclick={() => updateAdmin(admin, admin.role, !admin.suspended)} disabled={admin.id === me.id}>{admin.suspended ? 'Unsuspend' : 'Suspend'}</button><button class="danger" onclick={() => deleteAdmin(admin)} disabled={admin.id === me.id}>Delete</button>{/if}</div>{/each}</section>
      {:else if page === 'audit'}
        <section class="panel audit-filters"><div><label>Action<input placeholder="user.suspend" bind:value={auditAction} /></label>{#if canAuditAll}<label>Admin ID<input placeholder="Optional" bind:value={auditAdmin} /></label>{/if}<label>From<input type="datetime-local" bind:value={auditFrom} /></label><label>To<input type="datetime-local" bind:value={auditTo} /></label></div><div class="toolbar"><button onclick={loadAudit}>Apply filters</button>{#if canAuditAll}<button onclick={exportAudit}>Export CSV</button>{/if}<span>{audit.length} results on this page</span></div></section><section class="table-panel"><table><thead><tr><th>Time</th><th>Administrator</th><th>Action</th><th>Target</th></tr></thead><tbody>{#each audit as entry (entry.id)}<tr><td>{date(entry.createdAt)}</td><td>{entry.adminEmail || 'Deleted admin'}</td><td><code>{entry.action}</code></td><td>{entry.targetType} {entry.targetId || ''}</td></tr>{/each}</tbody></table></section>
      {:else if page === 'system'}
        <section class="panel"><h2>Configuration</h2><pre>{JSON.stringify(system, null, 2)}</pre></section><section class="panel"><h2>Rate-limit activity</h2>{#each metrics as metric (metric.operation)}<div class="setting-row"><div><strong>{metric.operation}</strong><small>Last activity {date(metric.updatedAt)}</small></div><span>{metric.attempts} attempts in {metric.buckets} buckets</span></div>{/each}{#if metrics.length === 0}<p class="empty">No recent rate-limit activity.</p>{/if}</section>
      {/if}
    </main>
  </div>
{/if}
