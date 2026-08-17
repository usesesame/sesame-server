<script lang="ts">
  import { onMount } from 'svelte'
  import type { Account } from '../lib/auth'
  import {
    findSecretShapedText,
    getSupportTicket,
    getSupportTickets,
    closeSupportTicket,
    reopenSupportTicket,
    replyToSupportTicket,
    submitSupportRequest,
    supportCategoryLabel,
    SUPPORT_CATEGORIES,
    type SupportCategory,
    type SupportTicketDetail,
    type SupportTicketStatus,
    type SupportTicketSummary,
  } from '../lib/support'

  export let account: Account | null = null

  let email = ''
  let subject = ''
  let message = ''
  let category: SupportCategory = 'general'
  let appVersion = ''
  let diagnosticCode = ''
  let browserIntegration = ''
  let requestId = ''
  let sending = false
  let receipt = ''
  let error = ''
  let tickets: SupportTicketSummary[] = []
  let selectedTicket: SupportTicketDetail | null = null
  let ticketsLoading = false
  let ticketLoading = false
  let portalError = ''
  let reply = ''
  let replySending = false
  let lifecycleWorking = false
  let loadedAccountID = ''

  $: secretSignal = findSecretShapedText(`${subject}\n${message}`)
  $: replySecretSignal = findSecretShapedText(reply)
  $: if (account && loadedAccountID !== account.id) {
    loadedAccountID = account.id
    email = account.email
    void loadTickets()
  }

  onMount(() => {
    const query = new URLSearchParams(window.location.search)
    const prefill = (name: string) => (query.get(name) || '').trim()
    const prefilledCategory = prefill('category')
    if (SUPPORT_CATEGORIES.some((option) => option.value === prefilledCategory)) category = prefilledCategory as SupportCategory
    if (prefill('intent') === 'founding') {
      category = 'billing'
      subject = 'Founding Pro interest'
      message = 'Please tell me when Founding Pro and beta access are available.'
    }
    appVersion = prefill('appVersion').slice(0, 40)
    diagnosticCode = prefill('diagnosticCode').slice(0, 64)
    browserIntegration = prefill('browserIntegration').slice(0, 64)
    requestId = prefill('requestId').slice(0, 64)
    if (query.size > 0) window.history.replaceState({}, '', window.location.pathname + window.location.hash)
  })

  const statusLabels: Record<SupportTicketStatus, string> = {
    open: 'Open',
    in_progress: 'In review',
    waiting: 'Reply sent',
    closed: 'Closed',
  }

  function formatDate(value: string) {
    return new Intl.DateTimeFormat('en', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
  }

  async function loadTickets(selectID = '') {
    if (!account) return
    ticketsLoading = true
    portalError = ''
    try {
      tickets = await getSupportTickets()
      const nextID = selectID || selectedTicket?.id
      if (nextID && tickets.some((ticket) => ticket.id === nextID)) await selectTicket(nextID)
      else if (selectedTicket && !tickets.some((ticket) => ticket.id === selectedTicket?.id)) selectedTicket = null
    } catch (reason) {
      portalError = reason instanceof Error ? reason.message : 'Your support requests could not be loaded.'
    } finally {
      ticketsLoading = false
    }
  }

  async function selectTicket(id: string) {
    ticketLoading = true
    portalError = ''
    try {
      selectedTicket = await getSupportTicket(id)
      reply = ''
    } catch (reason) {
      portalError = reason instanceof Error ? reason.message : 'That support request could not be loaded.'
    } finally {
      ticketLoading = false
    }
  }

  async function submit() {
    error = ''; receipt = ''
    if (secretSignal) { error = `Remove ${secretSignal} before sending.`; return }
    sending = true
    try {
      const result = await submitSupportRequest({
        email, subject, message, category,
        ...(appVersion.trim() ? { appVersion: appVersion.trim() } : {}),
        ...(diagnosticCode.trim() ? { diagnosticCode: diagnosticCode.trim() } : {}),
        ...(browserIntegration.trim() ? { browserIntegration: browserIntegration.trim() } : {}),
        ...(requestId.trim() ? { requestId: requestId.trim() } : {}),
      })
      receipt = result.requestId
      subject = ''; message = ''; category = 'general'; diagnosticCode = ''; browserIntegration = ''; requestId = ''
      if (account) await loadTickets(result.requestId)
    } catch (reason) {
      error = reason instanceof Error ? reason.message : 'Support intake is temporarily unavailable.'
    } finally { sending = false }
  }

  async function sendReply() {
    if (!selectedTicket || replySending) return
    portalError = ''
    if (replySecretSignal) { portalError = `Remove ${replySecretSignal} before sending.`; return }
    replySending = true
    try {
      selectedTicket = await replyToSupportTicket(selectedTicket.id, reply.trim())
      reply = ''
      await loadTickets(selectedTicket.id)
    } catch (reason) {
      portalError = reason instanceof Error ? reason.message : 'Your reply could not be sent.'
    } finally {
      replySending = false
    }
  }

  async function changeTicketLifecycle(action: 'close' | 'reopen') {
    if (!selectedTicket || lifecycleWorking) return
    lifecycleWorking = true
    portalError = ''
    try {
      selectedTicket = action === 'close'
        ? await closeSupportTicket(selectedTicket.id)
        : await reopenSupportTicket(selectedTicket.id)
      await loadTickets(selectedTicket.id)
    } catch (reason) {
      portalError = reason instanceof Error ? reason.message : 'That request could not be updated.'
    } finally {
      lifecycleWorking = false
    }
  }
</script>

<section class="page-hero compact-page-hero">
  <p class="eyebrow">Support</p>
  <h1>How can we help?</h1>
  <p class="intro">Send a question, beta request, or bug report. Never include passwords or vault files.</p>
</section>

<section class="section support-section safe-support">
  <aside class="support-guide card">
    <div><h2>Helpful</h2><ul><li>App and Windows version</li><li>What you clicked</li><li>The exact error</li></ul></div>
    <div><h2>Never send</h2><ul><li>Passwords or codes</li><li>Vault or export files</li><li>Recovery kits or keys</li></ul></div>
  </aside>

  {#if account}
    <section class="support-portal card" aria-labelledby="support-requests-heading">
      <div class="support-portal-head">
        <div><h2 id="support-requests-heading">Your requests</h2><p>Signed in as {account.email}</p></div>
        <button class="button button-soft button-sm" type="button" on:click={() => loadTickets()} disabled={ticketsLoading}>{ticketsLoading ? 'Refreshing…' : 'Refresh'}</button>
      </div>
      {#if portalError}<p class="auth-error" role="alert">{portalError}</p>{/if}
      {#if ticketsLoading && tickets.length === 0}
        <p class="support-empty" role="status">Loading your requests…</p>
      {:else if tickets.length === 0}
        <p class="support-empty">No requests from this account yet. Use the form below when you need help.</p>
      {:else}
        <div class="support-workspace">
          <div class="support-ticket-list" aria-label="Your support requests">
            {#each tickets as ticket (ticket.id)}
              <button type="button" class:active={selectedTicket?.id === ticket.id} on:click={() => selectTicket(ticket.id)}>
                <span><strong>{ticket.subject}</strong><small>{supportCategoryLabel(ticket.category)} · {formatDate(ticket.updatedAt)}</small></span>
                <span class="ticket-list-end"><span class={`ticket-status ${ticket.status}`}>{statusLabels[ticket.status]}</span>{#if ticket.unreadCount > 0}<span class="ticket-unread" aria-label={`${ticket.unreadCount} unread support ${ticket.unreadCount === 1 ? 'reply' : 'replies'}`}>{ticket.unreadCount}</span>{/if}</span>
              </button>
            {/each}
          </div>
          <div class="support-thread" aria-live="polite">
            {#if ticketLoading}
              <p class="support-empty">Loading request…</p>
            {:else if selectedTicket}
              <header class="support-thread-head">
                <div><h3>{selectedTicket.subject}</h3><p>{supportCategoryLabel(selectedTicket.category)} · Request {selectedTicket.id} · opened {formatDate(selectedTicket.createdAt)}</p></div>
                <span class={`ticket-status ${selectedTicket.status}`}>{statusLabels[selectedTicket.status]}</span>
              </header>
              {#if selectedTicket.appVersion || selectedTicket.diagnosticCode || selectedTicket.browserIntegration || selectedTicket.requestId}<p class="support-ticket-meta">{selectedTicket.appVersion ? `Sesame ${selectedTicket.appVersion}` : ''}{selectedTicket.diagnosticCode ? `${selectedTicket.appVersion ? ' · ' : ''}Diagnostic ${selectedTicket.diagnosticCode}` : ''}{selectedTicket.browserIntegration ? `${selectedTicket.appVersion || selectedTicket.diagnosticCode ? ' · ' : ''}Browser ${selectedTicket.browserIntegration}` : ''}{selectedTicket.requestId ? `${selectedTicket.appVersion || selectedTicket.diagnosticCode || selectedTicket.browserIntegration ? ' · ' : ''}Request ${selectedTicket.requestId}` : ''}</p>{/if}
              <div class="support-messages">
                {#each selectedTicket.messages as item (item.id)}
                  <article class:staff={item.authorRole === 'staff'}>
                    <div><strong>{item.authorRole === 'staff' ? 'Sesame support' : 'You'}</strong><time datetime={item.createdAt}>{formatDate(item.createdAt)}</time></div>
                    <p>{item.body}</p>
                  </article>
                {/each}
              </div>
              <div class="support-lifecycle-actions">
                {#if selectedTicket.canClose}<button class="button button-soft button-sm" type="button" disabled={lifecycleWorking} on:click={() => changeTicketLifecycle('close')}>{lifecycleWorking ? 'Updating...' : 'Close request'}</button>
                {:else if selectedTicket.canReopen}<button class="button button-soft button-sm" type="button" disabled={lifecycleWorking} on:click={() => changeTicketLifecycle('reopen')}>{lifecycleWorking ? 'Updating...' : 'Reopen request'}</button>{/if}
              </div>
              {#if selectedTicket.status === 'closed'}
                <p class="support-closed-note">This request is closed. You can reopen it for 30 days, then start a new request if the problem returned.</p>
              {:else}
                <form class="support-reply" on:submit|preventDefault={sendReply}>
                  <label>Add a follow-up<textarea bind:value={reply} required minlength="2" maxlength="4000" rows="4" aria-describedby="support-reply-safety"></textarea></label>
                  <p id="support-reply-safety" class:unsafe={replySecretSignal} class="support-safety">{replySecretSignal ? `This looks like ${replySecretSignal}. Remove it before sending.` : 'Do not include credentials, codes, vault exports, or screenshots.'}</p>
                  <button class="button button-sm" type="submit" disabled={replySending || reply.trim().length < 2 || !!replySecretSignal}>{replySending ? 'Sending…' : 'Send follow-up'}</button>
                </form>
              {/if}
            {:else}
              <p class="support-empty">Choose a request to read its conversation.</p>
            {/if}
          </div>
        </div>
      {/if}
    </section>
  {:else}
    <aside class="support-account-note card"><div><strong>Want a request history?</strong><p><a href="/login">Sign in</a> before sending. Guest requests still enter the same staff queue, but a reference number alone never grants access to their contents.</p></div></aside>
  {/if}

  <div class="support-intake card" id="new-request">
    {#if receipt}
      <div class="support-receipt" role="status"><h2>Request {receipt}</h2><p>Added to the queue. {account ? 'The request is now in your support history. You can follow replies above.' : 'Keep this reference for any follow-up. Guest request contents are not exposed through the public website.'}</p><div class="support-receipt-actions">{#if account}<button class="button" type="button" on:click={() => selectTicket(receipt)}>View request</button>{/if}<button class="button button-soft" type="button" on:click={() => (receipt = '')}>Send another report</button></div></div>
    {:else}
      <div class="support-form-head"><div><p class="eyebrow">Contact</p><h2>Send a request</h2></div><span>No attachments</span></div>
      <form class="support-form" on:submit|preventDefault={submit}>
        <label>Email<input type="email" bind:value={email} autocomplete="email" required maxlength="254" readonly={!!account} /></label>
        <label>Topic<select bind:value={category}>{#each SUPPORT_CATEGORIES as option (option.value)}<option value={option.value}>{option.label}</option>{/each}</select></label>
        <label>Short title<input type="text" bind:value={subject} required minlength="3" maxlength="120" /></label>
        <label>Message<textarea bind:value={message} required minlength="20" maxlength="4000" rows="7" aria-describedby="support-safety"></textarea></label>
        <details class="support-advanced">
          <summary>Add technical details <span>optional</span></summary>
          <div class="support-advanced-fields">
            <label>App version<input type="text" bind:value={appVersion} maxlength="40" placeholder="0.1.0-beta.3" /></label>
            <label>Diagnostic code<input type="text" bind:value={diagnosticCode} maxlength="64" pattern="[A-Za-z0-9._-]+" placeholder="UI-7F2A" /></label>
            <label>Browser integration<input type="text" bind:value={browserIntegration} maxlength="64" pattern="[A-Za-z0-9._-]+" placeholder="ready" /></label>
            <label>Request ID<input type="text" bind:value={requestId} maxlength="64" pattern="[A-Za-z0-9._-]+" placeholder="req-1234" /></label>
          </div>
        </details>
        <p id="support-safety" class:unsafe={secretSignal} class="support-safety">{secretSignal ? `This looks like ${secretSignal}. Remove it before sending.` : 'We check for common secret patterns before sending.'}</p>
        {#if error}<p class="auth-error" role="alert">{error}</p>{/if}
        <button class="button" type="submit" disabled={sending || !!secretSignal}>{sending ? 'Sending…' : 'Send request'}</button>
      </form>
    {/if}
  </div>
</section>

<section class="section support-faq">
  <div class="faq-block">
    <p class="eyebrow">Quick answers</p>
    <h2>Common questions</h2>
    <details><summary>Can I download Sesame now?</summary><p>Not yet. Sesame is an invite-only Windows beta.</p></details>
    <details><summary>Can support unlock my vault?</summary><p>No. Use your recovery kit or PIN. Without an unlock method, the vault cannot be recovered.</p></details>
    <details><summary>What can I import?</summary><p>15 formats from Bitwarden, 1Password, major browsers, and other password managers.</p></details>
    <details><summary>Does Sync work yet?</summary><p>No. It stays disabled until independent review and operating gates pass.</p></details>
    <details><summary>Is there a mobile app or browser extension?</summary><p>No mobile app yet. The browser helper is not in stores.</p></details>
    <details><summary>Where is my vault stored?</summary><p>On your Windows PC. The website and API never receive it.</p></details>
    <details><summary>Has Sesame been independently reviewed?</summary><p>Not yet. Use test data and keep an encrypted backup.</p></details>
  </div>
</section>
