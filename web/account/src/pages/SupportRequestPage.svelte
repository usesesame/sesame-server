<script lang="ts">
  import { onMount, tick } from 'svelte'
  import { ApiError } from '../lib/api'
  import type { Account } from '../lib/auth'
  import {
    attachSupportTicket,
    findSecretShapedText,
    openSupportAccess,
    replyToSupportAccess,
    supportCategoryLabel,
    type SupportTicketDetail,
    type SupportTicketStatus,
  } from '../lib/support'

  export let account: Account | null = null
  export let authLoading = false

  const PENDING_ATTACH_KEY = 'sesame-support-pending-attach'
  const statusLabels: Record<SupportTicketStatus, string> = {
    open: 'Open',
    in_progress: 'In review',
    waiting: 'Reply sent',
    closed: 'Closed',
  }

  let state: 'loading' | 'invalid' | 'unavailable' = 'loading'
  let token = ''
  let ticket: SupportTicketDetail | null = null
  let pendingAttachID = ''
  let attachState: 'idle' | 'working' | 'attached' | 'failed' = 'idle'
  let attachStarted = false
  let attachError = ''
  let reply = ''
  let replySending = false
  let replyError = ''
  let headingElement: HTMLElement | null = null
  let replyErrorElement: HTMLElement | null = null

  $: replySecretSignal = findSecretShapedText(reply)
  $: if (pendingAttachID && account && !authLoading && !attachStarted) {
    attachStarted = true
    void attachByID(pendingAttachID)
  }

  onMount(() => {
    const fragment = new URLSearchParams(window.location.hash.replace(/^#/, ''))
    const fragmentToken = (fragment.get('token') || '').trim()
    if (fragmentToken) {
      token = fragmentToken
      window.history.replaceState({}, '', window.location.pathname + window.location.search)
      void loadTicket()
      return
    }
    pendingAttachID = sessionStorage.getItem(PENDING_ATTACH_KEY) || ''
    if (!pendingAttachID) state = 'invalid'
  })

  async function focusHeading() {
    await tick()
    headingElement?.focus()
  }

  async function focusReplyError() {
    await tick()
    replyErrorElement?.focus()
  }

  function formatDate(value: string) {
    return new Intl.DateTimeFormat('en', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
  }

  async function loadTicket() {
    state = 'loading'
    try {
      ticket = await openSupportAccess(token)
    } catch (reason) {
      if (reason instanceof ApiError && (reason.status === 400 || reason.status === 403)) {
        state = 'invalid'
        void focusHeading()
        return
      }
      state = 'unavailable'
      void focusHeading()
    }
  }

  async function sendReply() {
    if (!ticket || replySending) return
    replyError = ''
    if (replySecretSignal) {
      replyError = `Remove ${replySecretSignal} before sending.`
      void focusReplyError()
      return
    }
    replySending = true
    try {
      ticket = await replyToSupportAccess(token, reply.trim())
      reply = ''
    } catch (reason) {
      replyError = reason instanceof Error ? reason.message : 'Your reply could not be sent.'
      void focusReplyError()
    } finally {
      replySending = false
    }
  }

  async function attach() {
    if (!ticket || attachState === 'working') return
    if (!account) {
      sessionStorage.setItem(PENDING_ATTACH_KEY, ticket.id)
      window.location.assign('/login?next=' + encodeURIComponent('/support/request'))
      return
    }
    await attachByID(ticket.id)
  }

  async function attachByID(id: string) {
    attachState = 'working'
    attachError = ''
    try {
      ticket = await attachSupportTicket(id)
      attachState = 'attached'
      sessionStorage.removeItem(PENDING_ATTACH_KEY)
    } catch (reason) {
      attachError = reason instanceof Error ? reason.message : 'This request could not be attached to your account.'
      attachState = 'failed'
      void focusHeading()
    }
  }
</script>

{#if ticket}
  <section class="section support-request-section">
    <article class="support-request-thread card" aria-labelledby="support-request-heading">
      <div class="support-thread" aria-live="polite">
        <header class="support-thread-head">
          <div>
            <h1 id="support-request-heading" tabindex="-1" bind:this={headingElement}>{ticket.subject}</h1>
            <p>{supportCategoryLabel(ticket.category)} &middot; Request {ticket.id} &middot; opened {formatDate(ticket.createdAt)}</p>
          </div>
          <span class={`ticket-status ${ticket.status}`}>{statusLabels[ticket.status]}</span>
        </header>
        <div class="support-messages">
          {#each ticket.messages as item (item.id)}
            <article class:staff={item.authorRole === 'staff'} aria-label={item.authorRole === 'staff' ? 'Sesame support' : 'You'}>
              <div><strong>{item.authorRole === 'staff' ? 'Sesame support' : 'You'}</strong><time datetime={item.createdAt}>{formatDate(item.createdAt)}</time></div>
              <p>{item.body}</p>
            </article>
          {/each}
        </div>
        {#if ticket.status !== 'closed'}
          <form class="support-reply" on:submit|preventDefault={sendReply}>
            <label for="support-request-reply">Add a follow-up</label>
            <textarea id="support-request-reply" bind:value={reply} required minlength="2" maxlength="4000" rows="4" aria-describedby="support-request-reply-hint"></textarea>
            <p id="support-request-reply-hint" class:unsafe={replySecretSignal} class="support-safety">{replySecretSignal ? `This looks like ${replySecretSignal}. Remove it before sending.` : 'Do not include credentials, codes, vault exports, or screenshots.'}</p>
            {#if replyError}<p class="auth-error" role="alert" tabindex="-1" bind:this={replyErrorElement}>{replyError}</p>{/if}
            <button class="button button-sm" type="submit" disabled={replySending || reply.trim().length < 2 || !!replySecretSignal}>{replySending ? 'Sending…' : 'Send follow-up'}</button>
          </form>
        {/if}
      </div>
    </article>

    <aside class="support-request-attach card" aria-labelledby="support-request-attach-heading">
      <h2 id="support-request-attach-heading">Keep this request on your account</h2>
      {#if attachState === 'attached'}
        <p role="status">This request is now in your account history. <a href="/support">Open your support requests</a>.</p>
      {:else}
        <p>Attach this request to a signed-in account that uses the same verified address it was sent from. Attaching moves it to your account and stops this link working.</p>
        {#if attachError}<p class="auth-error" role="alert">{attachError}</p>{/if}
        <button class="button button-sm" type="button" on:click={attach} disabled={attachState === 'working'}>{attachState === 'working' ? 'Attaching…' : 'Keep this request on your account'}</button>
        {#if !account && !authLoading}
          <p class="support-request-note">Already have an account? <a href={`/login?next=${encodeURIComponent('/support/request')}`}>Sign in</a>, then attach it.</p>
        {/if}
      {/if}
    </aside>
  </section>
{:else if pendingAttachID && attachState === 'failed'}
  <section class="section support-request-section">
    <div class="support-request-message card">
      <h1 tabindex="-1" bind:this={headingElement}>This request could not be attached</h1>
      <p>{attachError}</p>
      <p><a href="/support">Send a new request</a> or <a href="/login">sign in</a> with the address that sent this request.</p>
    </div>
  </section>
{:else if pendingAttachID && !authLoading && !account}
  <section class="section support-request-section">
    <div class="support-request-message card">
      <h1 tabindex="-1" bind:this={headingElement}>Sign in to keep this request on your account</h1>
      <p>Attaching needs a signed-in account with the same verified address this request was sent from.</p>
      <p><a class="button button-sm" href={`/login?next=${encodeURIComponent('/support/request')}`}>Sign in</a></p>
    </div>
  </section>
{:else if state === 'invalid'}
  <section class="section support-request-section">
    <div class="support-request-message card">
      <h1 tabindex="-1" bind:this={headingElement}>This link is no longer active</h1>
      <p>Support links expire after 7 days and stop working when a request is closed, moved to an account, or replaced by a newer reply. <a href="/support">Send a new request</a> or <a href="/login">sign in</a> if you still need help.</p>
    </div>
  </section>
{:else if state === 'unavailable'}
  <section class="section support-request-section">
    <div class="support-request-message card">
      <h1 tabindex="-1" bind:this={headingElement}>Support is temporarily unavailable</h1>
      <p role="alert">This support link could not be opened right now. Try again in a moment.</p>
    </div>
  </section>
{:else}
  <section class="section support-request-section">
    <div class="support-request-message card">
      <p role="status">Checking this support link…</p>
    </div>
  </section>
{/if}
