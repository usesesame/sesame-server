<script lang="ts">
  import { onMount, tick } from 'svelte'
  import { getNotificationPreferences, type Account } from '../lib/auth'
  import { siteOrigin } from '../lib/runtime-config'
  import PageHeader from '../lib/PageHeader.svelte'
  import Notice from '../lib/Notice.svelte'
  import Icon from '../lib/Icon.svelte'
  import {
    findSecretShapedText,
    getSupportMetadata,
    getSupportTicket,
    getSupportTickets,
    closeSupportTicket,
    reopenSupportTicket,
    replyToSupportTicket,
    submitSupportRequest,
    supportCategoryLabel,
    SUPPORT_CATEGORIES,
    type SupportCategory,
    type SupportMetadata,
    type SupportTicketDetail,
    type SupportTicketStatus,
    type SupportTicketSummary,
  } from '../lib/support'
  import { CATEGORY_GUIDANCE, HELP_ENTRIES } from '../lib/support-answers'
  import { clearSupportDraft, loadSupportDraft, matchHelp, relatedOpenTickets, saveSupportDraft } from '../lib/support-help'

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
  let supportMetadata: SupportMetadata | null = null
  let supportReplyEmail: boolean | null = null
  let ticketsLoaded = false
  let firstTicketOpened = false
  let formOpen = false
  let formFromLink = false
  let ticketSelection = 0

  let draftReady = false
  let dismissedHelp: string[] = []

  $: guidance = CATEGORY_GUIDANCE[category]
  $: help = matchHelp(HELP_ENTRIES, category, `${subject} ${message}`).filter((entry) => !dismissedHelp.includes(entry.id))
  $: related = relatedOpenTickets(tickets, category)
  $: if (draftReady) saveSupportDraft({ category, subject, message })
  $: secretSignal = findSecretShapedText(`${subject}\n${message}`)
  $: replySecretSignal = findSecretShapedText(reply)
  $: mailUnavailable = supportMetadata !== null && supportMetadata.receiptEmail === false
  $: showEmailSettingsLink = account !== null && supportMetadata?.receiptEmail === true
  $: showForm = !account || formOpen || formFromLink || Boolean(receipt) || (ticketsLoaded && tickets.length === 0) || (Boolean(portalError) && !ticketsLoaded)
  $: unreadTotal = tickets.reduce((total, ticket) => total + ticket.unreadCount, 0)
  $: if (account && loadedAccountID !== account.id) {
    loadedAccountID = account.id
    email = account.email
    void loadTickets()
  }

  function supportEmailNotice(metadataUnavailable: boolean, metadata: SupportMetadata | null, currentAccount: Account | null, replyEmail: boolean | null) {
    if (metadataUnavailable) {
      return currentAccount
        ? 'Email is unavailable on this deployment. Replies from support appear here in the portal.'
        : 'Email is unavailable on this deployment, so no receipt will be sent. Keep the reference shown after you send.'
    }
    if (!metadata) return 'Replies appear in this portal. Whether email is sent depends on this deployment\'s mail configuration.'
    if (!currentAccount) return 'We will email a receipt to this address. Guest requests do not receive reply email.'
    if (replyEmail === false) return 'We will email a receipt. Reply email is turned off for this account.'
    if (replyEmail === true) return `We will email a receipt, and replies from support will be emailed to ${currentAccount.email}.`
    return 'We will email a receipt. Reply email follows this account\'s notification setting.'
  }

  $: emailNotice = supportEmailNotice(mailUnavailable, supportMetadata, account, supportReplyEmail)

  async function loadEmailExpectations() {
    try { supportMetadata = await getSupportMetadata() } catch { supportMetadata = null }
    if (!account) return
    try { supportReplyEmail = (await getNotificationPreferences()).supportReplies } catch { supportReplyEmail = null }
  }

  onMount(() => {
    const query = new URLSearchParams(window.location.search)
    const prefill = (name: string) => (query.get(name) || '').trim()
    const draft = loadSupportDraft()
    if (draft) {
      formFromLink = Boolean(draft.subject || draft.message)
      category = draft.category
      subject = draft.subject
      message = draft.message
    }
    const prefilledCategory = prefill('category')
    if (SUPPORT_CATEGORIES.some((option) => option.value === prefilledCategory)) category = prefilledCategory as SupportCategory
    if (prefill('intent') === 'sync') {
      category = 'billing'
      subject = 'I want Sesame Sync'
      message = 'I would like to use Sesame Sync when it opens. Please tell me when I can pay for it.'
    }
    appVersion = prefill('appVersion').slice(0, 40)
    diagnosticCode = prefill('diagnosticCode').slice(0, 64)
    browserIntegration = prefill('browserIntegration').slice(0, 64)
    requestId = prefill('requestId').slice(0, 64)
    if (query.size > 0) formFromLink = true
    if (query.size > 0) window.history.replaceState({}, '', window.location.pathname + window.location.hash)
    draftReady = true
    void loadEmailExpectations()
  })

  const statusLabels: Record<SupportTicketStatus, string> = {
    open: 'Open',
    in_progress: 'In review',
    waiting: 'Reply sent',
    closed: 'Closed',
  }
  const statusKinds: Record<SupportTicketStatus, 'ok' | 'warn' | 'idle'> = {
    open: 'ok',
    in_progress: 'ok',
    waiting: 'warn',
    closed: 'idle',
  }

  function formatDate(value: string) {
    return new Intl.DateTimeFormat('en', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
  }

  async function loadTickets(selectID = '') {
    if (!account) return
    ticketsLoading = true
    portalError = ''
    const selectionAtStart = ticketSelection
    try {
      tickets = await getSupportTickets()
      ticketsLoaded = true
      const firstID = firstTicketOpened ? '' : (tickets.find((ticket) => ticket.unreadCount > 0) ?? tickets[0])?.id ?? ''
      firstTicketOpened = true
      const nextID = ticketSelection === selectionAtStart && !ticketLoading ? selectID || selectedTicket?.id || firstID : ''
      if (nextID && tickets.some((ticket) => ticket.id === nextID)) await selectTicket(nextID)
      else if (selectedTicket && !tickets.some((ticket) => ticket.id === selectedTicket?.id)) selectedTicket = null
    } catch (reason) {
      portalError = reason instanceof Error ? reason.message : 'Your support requests could not be loaded.'
    } finally {
      ticketsLoading = false
    }
  }

  async function selectTicket(id: string) {
    const selection = ++ticketSelection
    ticketLoading = true
    portalError = ''
    try {
      const ticket = await getSupportTicket(id)
      if (selection !== ticketSelection) return false
      selectedTicket = ticket
      reply = ''
      return true
    } catch (reason) {
      if (selection === ticketSelection) portalError = reason instanceof Error ? reason.message : 'That support request could not be loaded.'
      return false
    } finally {
      if (selection === ticketSelection) ticketLoading = false
    }
  }

  async function viewReceipt(id: string) {
    if (!(await selectTicket(id))) return
    await tick()
    document.getElementById('support-thread-title')?.focus()
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
      clearSupportDraft()
      subject = ''; message = ''; category = 'general'; diagnosticCode = ''; browserIntegration = ''; requestId = ''
      if (account) await loadTickets(result.requestId)
    } catch (reason) {
      error = reason instanceof Error ? reason.message : 'Support intake is temporarily unavailable.'
    } finally { sending = false }
  }

  function openRelated(id: string) {
    void selectTicket(id)
    document.getElementById('support-requests-heading')?.scrollIntoView?.({ block: 'start' })
  }

  function plural(count: number, word: string) {
    return `${count} ${word}${count === 1 ? '' : 's'}`
  }

  async function openForm() {
    formOpen = true
    await tick()
    document.getElementById('new-request-title')?.focus()
  }

  function helpHref(link: { path: string; site?: boolean }) {
    return link.site ? `${siteOrigin}${link.path}` : link.path
  }

  async function sendReply() {
    if (!selectedTicket || replySending) return
    portalError = ''
    if (replySecretSignal) { portalError = `Remove ${replySecretSignal} before sending.`; return }
    replySending = true
    const selection = ticketSelection
    try {
      const updated = await replyToSupportTicket(selectedTicket.id, reply.trim())
      if (selection !== ticketSelection) { await loadTickets(); return }
      selectedTicket = updated
      reply = ''
      await loadTickets(updated.id)
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
    const selection = ticketSelection
    try {
      const updated = action === 'close'
        ? await closeSupportTicket(selectedTicket.id)
        : await reopenSupportTicket(selectedTicket.id)
      if (selection !== ticketSelection) { await loadTickets(); return }
      selectedTicket = updated
      await loadTickets(updated.id)
    } catch (reason) {
      portalError = reason instanceof Error ? reason.message : 'That request could not be updated.'
    } finally {
      lifecycleWorking = false
    }
  }
</script>

<div class="page">
  <PageHeader title="Support" fact={account && ticketsLoaded ? plural(tickets.length, 'request') : ''} detail={account && unreadTotal > 0 ? `${unreadTotal} unread` : ''} />

  {#if account}
    <section class="group" aria-labelledby="support-requests-heading">
      <div class="group-head">
        <h2 class="section-title" id="support-requests-heading" tabindex="-1">Your requests</h2>
        <div class="button-row button-row-end">
          <button class="button button-secondary button-sm" type="button" on:click={() => loadTickets()} disabled={ticketsLoading}>{ticketsLoading ? 'Refreshing…' : 'Refresh'}</button>
          {#if !showForm}<button class="button button-sm" type="button" on:click={openForm}>New request</button>{/if}
        </div>
      </div>
      {#if portalError}<p class="inline-notice inline-danger group-alert" role="alert"><Icon name="error" size={15} />{portalError}</p>{/if}
      {#if ticketsLoading && tickets.length === 0}
        <p class="rows-empty" role="status">Loading your requests…</p>
      {:else if tickets.length === 0}
        <p class="rows-empty">No requests from this account yet. Use the form below when you need help.</p>
      {:else}
        <div class="support-workspace">
          <div class="support-ticket-list" aria-label="Your support requests">
            {#each tickets as ticket (ticket.id)}
              <button type="button" class:active={selectedTicket?.id === ticket.id} aria-current={selectedTicket?.id === ticket.id ? 'true' : undefined} on:click={() => selectTicket(ticket.id)}>
                <span class="ticket-main"><strong>{ticket.subject}</strong><small>{supportCategoryLabel(ticket.category)}, {formatDate(ticket.updatedAt)}</small></span>
                <span class="ticket-end"><span class={`status status-${statusKinds[ticket.status]}`}>{statusLabels[ticket.status]}</span>{#if ticket.unreadCount > 0}<span class="tag tag-warn ticket-unread" aria-label={`${ticket.unreadCount} unread support ${ticket.unreadCount === 1 ? 'reply' : 'replies'}`}>{ticket.unreadCount}</span>{/if}</span>
              </button>
            {/each}
          </div>
          <div class="support-thread" aria-live="polite">
            {#if ticketLoading}
              <p class="rows-empty">Loading request…</p>
            {:else if selectedTicket}
              <header class="support-thread-head">
                <div><h3 id="support-thread-title" tabindex="-1">{selectedTicket.subject}</h3><p class="row-meta row-facts"><span>{supportCategoryLabel(selectedTicket.category)}</span><span>Request {selectedTicket.id}</span><span>Opened {formatDate(selectedTicket.createdAt)}</span></p></div>
                <span class={`status status-${statusKinds[selectedTicket.status]}`}>{statusLabels[selectedTicket.status]}</span>
              </header>
              <p class="support-email-note">{emailNotice}{#if showEmailSettingsLink}&nbsp;<a href="/account/security">Change your reply email setting</a>.{/if}</p>
              {#if selectedTicket.appVersion || selectedTicket.diagnosticCode || selectedTicket.browserIntegration || selectedTicket.requestId}<p class="row-meta row-facts support-ticket-meta">{#if selectedTicket.appVersion}<span>Sesame <span class="version">{selectedTicket.appVersion}</span></span>{/if}{#if selectedTicket.diagnosticCode}<span>Diagnostic {selectedTicket.diagnosticCode}</span>{/if}{#if selectedTicket.browserIntegration}<span>Browser {selectedTicket.browserIntegration}</span>{/if}{#if selectedTicket.requestId}<span>Request {selectedTicket.requestId}</span>{/if}</p>{/if}
              <div class="support-messages">
                {#each selectedTicket.messages as item (item.id)}
                  <article class:staff={item.authorRole === 'staff'}>
                    <div><strong>{item.authorRole === 'staff' ? 'Sesame support' : 'You'}</strong><time datetime={item.createdAt}>{formatDate(item.createdAt)}</time></div>
                    <p>{item.body}</p>
                  </article>
                {/each}
              </div>
              {#if selectedTicket.status === 'closed'}
                <p class="support-closed-note">{selectedTicket.autoClosed ? 'This request closed automatically after 14 days without a reply. You can reopen it for 30 days, then start a new request if the problem returned.' : 'This request is closed. You can reopen it for 30 days, then start a new request if the problem returned.'}</p>
              {:else}
                <form class="support-reply" on:submit|preventDefault={sendReply}>
                  <label>Add a follow-up<textarea bind:value={reply} required minlength="2" maxlength="4000" rows="4" aria-describedby="support-reply-safety" aria-invalid={replySecretSignal ? 'true' : undefined}></textarea></label>
                  <p id="support-reply-safety" class={replySecretSignal ? 'inline-notice inline-danger' : 'field-hint'}>{#if replySecretSignal}<Icon name="error" size={15} />{/if}{replySecretSignal ? `This looks like ${replySecretSignal}. Remove it before sending.` : 'Do not include credentials, codes, vault exports, or screenshots.'}</p>
                  <div class="button-row"><button class="button button-secondary button-sm" type="submit" disabled={replySending || reply.trim().length < 2 || !!replySecretSignal}>{replySending ? 'Sending…' : 'Send follow-up'}</button></div>
                </form>
              {/if}
              <div class="support-lifecycle-actions">
                {#if selectedTicket.canClose}<button class="button button-text button-sm button-flush" type="button" disabled={lifecycleWorking} on:click={() => changeTicketLifecycle('close')}>{lifecycleWorking ? 'Updating...' : 'Close request'}</button>
                {:else if selectedTicket.canReopen}<button class="button button-secondary button-sm" type="button" disabled={lifecycleWorking} on:click={() => changeTicketLifecycle('reopen')}>{lifecycleWorking ? 'Updating...' : 'Reopen request'}</button>{/if}
              </div>
            {:else}
              <p class="rows-empty">Choose a request to read its conversation.</p>
            {/if}
          </div>
        </div>
      {/if}
    </section>
  {:else}
    <Notice title="Want a request history?">
      <p class="notice-body"><a href="/login">Sign in</a> before sending. Guest requests still enter the same staff queue, but a reference number alone never grants access to their contents.</p>
    </Notice>
  {/if}

  {#if showForm}
    <section class="group support-new" id="new-request" aria-labelledby="new-request-title">
      <div class="support-columns">
        <div class="support-intake">
          {#if receipt}
            <div class="support-receipt" role="status"><h2 class="section-title" id="new-request-title" tabindex="-1">Request {receipt}</h2><p class="group-note">Added to the queue. {account ? 'The request is now in your support history. You can follow replies above.' : 'Keep this reference for any follow-up. Guest request contents are not exposed through the public website.'}</p><div class="button-row">{#if account}<button class="button" type="button" on:click={() => viewReceipt(receipt)}>View request</button>{/if}<button class="button button-soft" type="button" on:click={() => { receipt = ''; formOpen = true }}>Send another report</button></div></div>
          {:else}
            <div class="support-form-head"><h2 class="section-title" id="new-request-title" tabindex="-1">New request</h2><span class="tag">No attachments</span></div>
            <p class="support-response-expectation">We aim to reply within 3 business days.</p>
            {#if related.length > 0}
              <Notice title="You already have an open request on this topic" role="status" label="Existing request">
                <p class="notice-body"><strong>{related[0].subject}</strong>. Adding to it keeps the history together.</p>
                <button slot="action" class="button button-secondary button-sm" type="button" on:click={() => openRelated(related[0].id)}>Open request</button>
              </Notice>
            {/if}
            <form class="support-form" on:submit|preventDefault={submit}>
              <label>Email<input type="email" bind:value={email} autocomplete="email" required maxlength="254" readonly={!!account} /></label>
              <label class="select-field">Topic<select bind:value={category}>{#each SUPPORT_CATEGORIES as option (option.value)}<option value={option.value}>{option.label}</option>{/each}</select><span class="select-chevron" aria-hidden="true"><Icon name="chevron" /></span></label>
              <label>Short title<input type="text" bind:value={subject} required minlength="3" maxlength="120" /></label>
              <label>Message<textarea bind:value={message} required minlength="20" maxlength="4000" rows="7" placeholder={guidance.placeholder} aria-describedby="support-safety" aria-invalid={secretSignal ? 'true' : undefined}></textarea></label>
              <p id="support-safety" class={secretSignal ? 'inline-notice inline-danger' : 'field-hint'}>{#if secretSignal}<Icon name="error" size={15} />{/if}{secretSignal ? `This looks like ${secretSignal}. Remove it before sending.` : 'We check for common secret patterns before sending.'}</p>
              {#if help.length > 0}
                <section class="support-help" aria-label="Answers that may help">
                  <h3>These may already answer it</h3>
                  {#each help as entry (entry.id)}
                    <article>
                      <h4>{entry.title}</h4>
                      <p>{entry.answer}</p>
                      <div class="button-row">
                        {#if entry.link}<a class="button button-text button-sm button-flush" href={helpHref(entry.link)}>{entry.link.label}</a>{/if}
                        <button class="button button-text button-sm" type="button" on:click={() => (dismissedHelp = [...dismissedHelp, entry.id])}>Not what I need</button>
                      </div>
                    </article>
                  {/each}
                </section>
              {/if}
              <details class="support-advanced">
                <summary><Icon name="chevron" />Add technical details <span>optional</span></summary>
                <div class="support-advanced-fields">
                  <label>App version<input type="text" bind:value={appVersion} maxlength="40" placeholder="0.4.1" /></label>
                  <label>Diagnostic code<input type="text" bind:value={diagnosticCode} maxlength="64" pattern="[A-Za-z0-9._-]+" placeholder="UI-7F2A" /></label>
                  <label>Browser integration<input type="text" bind:value={browserIntegration} maxlength="64" pattern="[A-Za-z0-9._-]+" placeholder="ready" /></label>
                  <label>Request ID<input type="text" bind:value={requestId} maxlength="64" pattern="[A-Za-z0-9._-]+" placeholder="req-1234" /></label>
                </div>
              </details>
              <p class="support-email-note">{emailNotice}{#if showEmailSettingsLink}&nbsp;<a href="/account/security">Change your reply email setting</a>.{/if}</p>
              {#if error}<p class="inline-notice inline-danger" role="alert"><Icon name="error" size={15} />{error}</p>{/if}
              <div class="button-row"><button class="button" type="submit" disabled={sending || !!secretSignal}>{sending ? 'Sending…' : 'Send request'}</button></div>
            </form>
          {/if}
        </div>
        <aside class="support-guide" aria-label="What to include">
          <div><h3>Helpful</h3><ul>{#each guidance.include as item (item)}<li>{item}</li>{/each}</ul></div>
          <div><h3>Never send</h3><ul><li>Passwords or codes</li><li>Vault or export files</li><li>Recovery kits or keys</li></ul></div>
        </aside>
      </div>
    </section>
  {/if}

  <section class="group support-faq" aria-labelledby="faq-title">
    <h2 class="section-title" id="faq-title">Common questions</h2>
    <div class="faq-block">
      <details><summary>Can I download Sesame now?</summary><p>Yes. Sesame is a free download for Windows and Linux.</p></details>
      <details><summary>Can support unlock my vault?</summary><p>No. Use your recovery kit, PIN, or Windows Hello. Without an unlock method, the vault cannot be recovered.</p></details>
      <details><summary>What can I import?</summary><p>Bitwarden, 1Password, major browsers, other password managers and authenticator apps. The import screen lists every format.</p></details>
      <details><summary>Does Sync work yet?</summary><p>Yes, on a server you run. Sync is in the latest desktop release and is off until you turn it on. Sesame Sync, which is Sync on our servers, is not open yet, and the <a href={`${siteOrigin}/roadmap#sync`}>roadmap</a> has the details.</p></details>
      <details><summary>Is there a mobile app or browser extension?</summary><p>There is no mobile app yet. The Chrome extension is in the Chrome Web Store and needs Sesame 0.4.1 or newer.</p></details>
      <details><summary>Where is my vault stored?</summary><p>On your computer.</p></details>
      <details><summary>Has Sesame been independently reviewed?</summary><p>Not yet. The <a href={`${siteOrigin}/security`}>security page</a> shows what that means for the current release.</p></details>
    </div>
  </section>
</div>
