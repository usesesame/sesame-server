<script lang="ts">
  import { onDestroy, onMount } from 'svelte'
  import {
    cancelDesktopLink,
    changePassword,
    createDesktopLink,
    deleteAccount,
    getAccountBootstrap,
    getAccountActivity,
    getNotificationPreferences,
    updateNotificationPreferences,
    getAccountDownloads,
	createDownloadTicket,
    getDesktopLink,
    listDesktopDevices,
    listSessions,
    reauthenticate,
    requestEmailChange,
    requestEmailVerification,
    revokeAllSessions,
    revokeDesktopDevice,
    renameDesktopDevice,
    revokeSession,
    signOut,
    type Account,
    type AccountActivityEvent,
    type NotificationPreferences,
    type AuthState,
    type AccountAccess,
    type AccountDownload,
    type AccountSession,
    type DesktopDevice,
    type DesktopLink,
  } from '../lib/auth'
  import { deletePasskey, listPasskeys, passkeysSupported, registerPasskey, type PasskeyInfo } from '../lib/passkey'
  import { siteOrigin } from '../lib/runtime-config'
  import { capabilities, capabilityEnabled, type CapabilityConfig } from '../lib/capabilities'

  export let account: Account | null
  export let authState: AuthState = { state: 'loading' }
  export let onSignedOut: () => void

  type Tab = 'overview' | 'security' | 'sessions' | 'devices' | 'activity' | 'downloads'
  const tabs: { id: Tab; label: string }[] = [
    { id: 'overview', label: 'Overview' },
    { id: 'security', label: 'Security' },
    { id: 'sessions', label: 'Sessions' },
    { id: 'devices', label: 'Desktops' },
    { id: 'activity', label: 'Activity' },
    { id: 'downloads', label: 'Downloads' },
  ]
  let tab: Tab = 'overview'
  const siteHost = new URL(siteOrigin).host
  let now = Date.now()
  let clock: number | undefined
  let poll: number | undefined

  let working = false
  let error = ''
  let notice = ''
  let access: AccountAccess | null = null
  let downloads: AccountDownload[] = []
  let supportUnread = 0
  let capabilityConfig: CapabilityConfig | null = null
  let accessLoaded = false
	let downloadStarting = ''

  let currentPassword = ''
  let newPassword = ''
  let confirmPassword = ''
  let securityPassword = ''
  let newEmail = ''
  let passwordSaving = false
  let securityBusy = false
  let passwordError = ''
  let passkeys: PasskeyInfo[] = []
  let passkeysLoaded = false
  let passkeyError = ''
  let newPasskeyName = ''
  const canUsePasskey = passkeysSupported()

  let sessions: AccountSession[] = []
  let sessionsLoaded = false
  let sessionsLoading = false
  let sessionsPassword = ''
  let sessionError = ''
  let revokingSession = ''

  let link: DesktopLink | null = null
  let linking = false
  let copied = false
  let devicesPassword = ''
  let devices: DesktopDevice[] = []
  let devicesLoaded = false
  let devicesLoading = false
  let deviceError = ''
  let revokingDeviceId = ''
  let renamingDeviceId = ''
  let renameDraftId = ''
  let renameDraftValue = ''

  let activity: AccountActivityEvent[] = []
  let activityLoaded = false
  let activityLoading = false
  let activityError = ''

  let notificationPreferences: NotificationPreferences = { betaReleases: false, supportReplies: false, productAnnouncements: false }
  let notificationPreferencesLoaded = false
  let notificationPreferencesSaving = false
  let notificationPreferencesError = ''

  let deleteOpen = false
  let deletePassword = ''
  let deleteConfirm = ''
  let deleting = false
  let deleteError = ''

  $: linkSeconds = link?.expiresAt ? Math.max(0, Math.ceil((new Date(link.expiresAt).getTime() - now) / 1000)) : 0
  $: linkCountdown = `${Math.floor(linkSeconds / 60)}:${String(linkSeconds % 60).padStart(2, '0')}`
  $: desktopCodeDisplay = link?.code ? formatDesktopCode(link.code) : ''
  $: betaGranted = access?.betaAccess ?? account?.betaAccess ?? false
  $: desktopLinkReady = Boolean(account?.emailVerified && betaGranted && capabilityConfig && capabilityEnabled(capabilityConfig, 'desktopLinking'))
  $: desktopLinkBlocker = !account?.emailVerified
    ? 'Verify your account email before connecting a desktop.'
    : !betaGranted
      ? 'Beta access is required before connecting a desktop.'
      : !capabilityConfig
        ? 'Sesame could not confirm which features are available. Check your connection and reload.'
        : 'Connecting a desktop is turned off at the moment.'

  onMount(() => {
    clock = window.setInterval(() => { now = Date.now() }, 1000)
    poll = window.setInterval(() => {
      if (link?.state === 'pending') void refreshDesktopLink(false)
    }, 5000)
    void loadAccess()
  })

  onDestroy(() => {
    if (clock) window.clearInterval(clock)
    if (poll) window.clearInterval(poll)
  })

  async function loadAccess() {
    try {
      const [bootstrap, configuration] = await Promise.all([getAccountBootstrap(), capabilities()])
      access = bootstrap.access
      capabilityConfig = configuration
      supportUnread = bootstrap.notificationCounts.support
      downloads = await getAccountDownloads().catch(() => [])
    } catch { /* Account can still manage security if entitlement data is unavailable. */ }
    accessLoaded = true
  }

  async function startDownload(release: AccountDownload) {
	if (!capabilityConfig || !capabilityEnabled(capabilityConfig, 'downloads')) { error = 'Verified private-beta downloads are temporarily unavailable.'; return }
    downloadStarting = release.id
    error = ''
    try {
      window.location.assign(await createDownloadTicket(release))
    } catch (reason) {
      error = reason instanceof Error ? reason.message : 'That download could not be started.'
    } finally {
      downloadStarting = ''
    }
  }

  function formatDate(value?: string) {
    if (!value) return 'Unknown'
    const date = new Date(value)
    if (Number.isNaN(date.getTime())) return 'Unknown'
    return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(date)
  }

  function formatDesktopCode(value: string) {
    if (value.length <= 16) return value
    const prefix = value.startsWith('SESAME-') ? 'SESAME-' : ''
    const code = prefix ? value.slice(prefix.length) : value
    return `${prefix}${code.match(/.{1,4}/g)?.join(' ') ?? code}`
  }

  function selectTab(next: Tab) {
    tab = next
    error = ''; notice = ''
    if (next === 'security' && canUsePasskey && !passkeysLoaded) void loadPasskeys()
    if (next === 'security' && !notificationPreferencesLoaded) void loadNotificationPreferences()
    if (next === 'sessions' && !sessionsLoaded) void loadSessions()
    if (next === 'devices' && !devicesLoaded) void loadDevicesAndLink()
    if (next === 'activity' && !activityLoaded) void loadActivity()
    if ((next === 'overview' || next === 'downloads') && !accessLoaded) void loadAccess()
  }

  function tabKeydown(event: KeyboardEvent, index: number) {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
    event.preventDefault()
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length
    selectTab(tabs[next].id)
    document.getElementById(`account-tab-${tabs[next].id}`)?.focus()
  }

  async function confirmSensitive(password: string) {
    if (!password) throw new Error('Enter your website account password to continue.')
    await reauthenticate(password)
  }

  async function leave() {
    working = true; error = ''
    try { await signOut(); onSignedOut(); window.location.assign('/') }
    catch (reason) { error = reason instanceof Error ? reason.message : 'Sign out is temporarily unavailable.' }
    finally { working = false }
  }

  async function resendVerification() {
    working = true; error = ''; notice = ''
    try { await requestEmailVerification(); notice = 'Verification email sent. The link expires for safety.' }
    catch (reason) { error = reason instanceof Error ? reason.message : 'Verification email could not be sent.' }
    finally { working = false }
  }

  async function startEmailChange() {
    securityBusy = true; passwordError = ''; notice = ''
    try {
      await confirmSensitive(securityPassword)
      await requestEmailChange(newEmail)
      securityPassword = ''; newEmail = ''
      notice = 'Confirmation sent to the new address. Your current email stays active until the link is used.'
    } catch (reason) { passwordError = reason instanceof Error ? reason.message : 'Email change could not be started.' }
    finally { securityBusy = false }
  }

  async function submitPassword() {
    passwordError = ''; notice = ''
    if (newPassword.length < 12) { passwordError = 'Use a new password of at least 12 characters.'; return }
    if (newPassword !== confirmPassword) { passwordError = 'The new passwords do not match.'; return }
    passwordSaving = true
    try {
      await confirmSensitive(currentPassword)
      await changePassword(currentPassword, newPassword)
      currentPassword = ''; newPassword = ''; confirmPassword = ''
      notice = 'Password changed. Other website sessions were revoked in the same update.'
      sessionsLoaded = false
    } catch (reason) { passwordError = reason instanceof Error ? reason.message : 'Changing your password is temporarily unavailable.' }
    finally { passwordSaving = false }
  }

  async function loadPasskeys() {
    passkeyError = ''
    try { passkeys = await listPasskeys(); passkeysLoaded = true }
    catch (reason) { passkeyError = reason instanceof Error ? reason.message : 'Passkeys are temporarily unavailable.' }
  }

  async function addPasskey() {
    securityBusy = true; passkeyError = ''
    try {
      await confirmSensitive(securityPassword)
      await registerPasskey(newPasskeyName.trim() || 'Passkey')
      securityPassword = ''; newPasskeyName = ''
      await loadPasskeys()
    } catch (reason) { passkeyError = reason instanceof Error ? reason.message : 'That passkey could not be added.' }
    finally { securityBusy = false }
  }

  async function removePasskey(id: string) {
    securityBusy = true; passkeyError = ''
    try {
      await confirmSensitive(securityPassword)
      await deletePasskey(id)
      securityPassword = ''
      passkeys = passkeys.filter((passkey) => passkey.id !== id)
    } catch (reason) { passkeyError = reason instanceof Error ? reason.message : 'That passkey could not be removed.' }
    finally { securityBusy = false }
  }

  async function loadSessions() {
    sessionsLoading = true; sessionError = ''
    try { sessions = await listSessions(); sessionsLoaded = true }
    catch (reason) { sessionError = reason instanceof Error ? reason.message : 'Website sessions are temporarily unavailable.' }
    finally { sessionsLoading = false }
  }

  async function loadActivity() {
    activityLoading = true; activityError = ''
    try { activity = await getAccountActivity(); activityLoaded = true }
    catch (reason) { activityError = reason instanceof Error ? reason.message : 'Security activity is temporarily unavailable.' }
    finally { activityLoading = false }
  }

  async function loadNotificationPreferences() {
    notificationPreferencesError = ''
    try { notificationPreferences = await getNotificationPreferences(); notificationPreferencesLoaded = true }
    catch (reason) { notificationPreferencesError = reason instanceof Error ? reason.message : 'Notification preferences are temporarily unavailable.' }
  }

  async function saveNotificationPreferences() {
    notificationPreferencesSaving = true; notificationPreferencesError = ''
    try { await updateNotificationPreferences(notificationPreferences); notice = 'Notification preferences updated.' }
    catch (reason) { notificationPreferencesError = reason instanceof Error ? reason.message : 'Notification preferences could not be updated.' }
    finally { notificationPreferencesSaving = false }
  }

  function startRename(device: DesktopDevice) {
    renameDraftId = device.deviceId; renameDraftValue = device.deviceName; deviceError = ''
  }

  function cancelRename() {
    renameDraftId = ''; renameDraftValue = ''
  }

  async function renameDevice(device: DesktopDevice) {
    const name = renameDraftValue.trim()
    if (!name) { deviceError = 'Enter a desktop name.'; return }
    renamingDeviceId = device.deviceId; deviceError = ''
    try {
      await confirmSensitive(devicesPassword)
      await renameDesktopDevice(device.deviceId, name)
      devices = devices.map((item) => item.deviceId === device.deviceId ? { ...item, deviceName: name } : item)
      devicesPassword = ''; renameDraftId = ''; renameDraftValue = ''
      notice = 'Desktop name updated.'
    } catch (reason) { deviceError = reason instanceof Error ? reason.message : 'That desktop name could not be updated.' }
    finally { renamingDeviceId = '' }
  }

  async function removeSession(session: AccountSession) {
    revokingSession = session.id; sessionError = ''
    try {
      await confirmSensitive(sessionsPassword)
      await revokeSession(session.id)
      sessionsPassword = ''
      sessions = sessions.filter((item) => item.id !== session.id)
      if (session.current) { onSignedOut(); window.location.assign('/login') }
    } catch (reason) { sessionError = reason instanceof Error ? reason.message : 'That session could not be revoked.' }
    finally { revokingSession = '' }
  }

  async function removeAllSessions() {
    sessionsLoading = true; sessionError = ''
    try {
      await confirmSensitive(sessionsPassword)
      await revokeAllSessions()
      onSignedOut(); window.location.assign('/login')
    } catch (reason) { sessionError = reason instanceof Error ? reason.message : 'Sessions could not be revoked.'; sessionsLoading = false }
  }

  async function refreshDesktopLink(showError = true) {
    try {
      const state = await getDesktopLink()
      if (link?.code && state.state === 'pending') link = { ...state, code: link.code }
      else link = state
      if (state.state === 'connected') { notice = 'Desktop connected. The one-time code can no longer be used.'; await loadDevices() }
    } catch (reason) { if (showError) deviceError = reason instanceof Error ? reason.message : 'Link status is unavailable.' }
  }

  async function loadDevicesAndLink() { await Promise.all([loadDevices(), refreshDesktopLink(false)]) }

  async function loadDevices() {
    devicesLoading = true; deviceError = ''
    try { devices = await listDesktopDevices(); devicesLoaded = true }
    catch (reason) { deviceError = reason instanceof Error ? reason.message : 'Connected desktops are temporarily unavailable.' }
    finally { devicesLoading = false }
  }

  async function makeDesktopLink() {
    linking = true; deviceError = ''; copied = false; notice = ''
    try {
      await confirmSensitive(devicesPassword)
      link = await createDesktopLink()
      devicesPassword = ''
    } catch (reason) { deviceError = reason instanceof Error ? reason.message : 'Desktop linking is temporarily unavailable.' }
    finally { linking = false }
  }

  async function copyDesktopCode() {
    if (!link?.code) return
    try { await navigator.clipboard.writeText(link.code); copied = true; window.setTimeout(() => { copied = false }, 1800) }
    catch { deviceError = 'Copy failed. Select the code and copy it manually.' }
  }

  async function cancelLink() {
    linking = true; deviceError = ''
    try {
      await confirmSensitive(devicesPassword)
      await cancelDesktopLink()
      devicesPassword = ''; link = { state: 'none' }
    } catch (reason) { deviceError = reason instanceof Error ? reason.message : 'The link could not be cancelled.' }
    finally { linking = false }
  }

  async function removeDevice(device: DesktopDevice) {
    revokingDeviceId = device.deviceId; deviceError = ''
    try {
      await confirmSensitive(devicesPassword)
      await revokeDesktopDevice(device.deviceId)
      devicesPassword = ''
      devices = devices.filter((item) => item.deviceId !== device.deviceId)
    } catch (reason) { deviceError = reason instanceof Error ? reason.message : 'This desktop could not be removed.' }
    finally { revokingDeviceId = '' }
  }

  async function confirmDelete() {
    deleteError = ''; deleting = true
    try {
      await confirmSensitive(deletePassword)
      await deleteAccount(deletePassword)
      onSignedOut()
      window.location.assign('/')
    }
    catch (reason) { deleteError = reason instanceof Error ? reason.message : 'Deleting your account is temporarily unavailable.' }
    finally { deleting = false }
  }
</script>

<section class="account-shell">
  <div class="account-copy account-head">
    <p class="eyebrow">Sesame account</p>
    <h1>Account services.</h1>
    <p class="intro">Beta access, verified private-beta downloads, licences, website sessions, and connected desktops. Your vault is not stored here.</p>
  </div>

  {#if account}
    <div class="account-tabs" role="tablist" aria-label="Account sections">
      {#each tabs as item, index (item.id)}
        <button id={`account-tab-${item.id}`} role="tab" type="button" class:active={tab === item.id} aria-selected={tab === item.id} aria-controls="account-panel" tabindex={tab === item.id ? 0 : -1} on:keydown={(event) => tabKeydown(event, index)} on:click={() => selectTab(item.id)}>{item.label}</button>
      {/each}
    </div>

    <div id="account-panel" class="account-panel card" role="tabpanel" aria-live="polite">
      {#if notice}<p class="account-success" role="status">{notice}</p>{/if}
      {#if error}<p class="auth-error" role="alert">{error}</p>{/if}

      {#if tab === 'overview'}
        <div class="panel-section account-summary">
          <div><p class="account-label">Signed in as</p><strong class="account-email">{account.email}</strong></div>
          <span class:verified={account.emailVerified} class="verification-state">{account.emailVerified ? 'Email verified' : 'Email not verified'}</span>
          {#if !account.emailVerified}<button class="button button-soft button-sm" type="button" on:click={resendVerification} disabled={working}>Send verification email</button>{/if}
        </div>
        <div class="account-purpose-grid">
          <article><span>Beta access</span><strong>{betaGranted ? account.emailVerified ? 'Eligible' : 'Granted, verify email' : 'Not granted'}</strong><p>{betaGranted && !account.emailVerified ? 'Verify your email to activate beta services and desktop linking.' : 'Controls invited builds and feedback access.'}</p></article>
          <article><span>Private-beta downloads</span><strong>{access?.downloadsAllowed ? 'Available' : 'No eligible build'}</strong><p>Only account-gated artifacts with a verified Tauri updater signature appear.</p></article>
          <article><span>Licences</span><strong>{access?.licences.length || 0}</strong><p>Purchases will live here when sales open.</p></article>
          <article><span>Support</span><strong>{supportUnread > 0 ? `${supportUnread} unread` : 'Up to date'}</strong><p><a href="/support">View your support requests and replies.</a></p></article>
          <article><span>Local vault</span><strong>Not stored</strong><p>The website cannot see or unlock it.</p></article>
        </div>
        <div class="panel-section">
          <button class="button button-soft" type="button" on:click={leave} disabled={working}>{working ? 'Signing out…' : 'Sign out'}</button>
        </div>
        <div class="panel-section danger-zone">
          <p class="account-label danger-label">Delete website account</p>
          <p class="danger-copy">Deletes sessions, eligibility, licences, and desktop links. Local vaults are untouched.</p>
          {#if !deleteOpen}
            <button class="danger-button" type="button" on:click={() => { deleteOpen = true; deleteError = '' }}>Delete account</button>
          {:else}
            <form class="account-form" on:submit|preventDefault={confirmDelete}>
              <label>Account password<input type="password" autocomplete="current-password" bind:value={deletePassword} required /></label>
              <label>Type DELETE<input type="text" autocomplete="off" bind:value={deleteConfirm} /></label>
              {#if deleteError}<p class="auth-error" role="alert">{deleteError}</p>{/if}
              <div class="account-actions"><button class="button button-soft" type="button" on:click={() => { deleteOpen = false; deletePassword = ''; deleteConfirm = '' }}>Cancel</button><button class="danger-button" type="submit" disabled={deleting || deleteConfirm !== 'DELETE' || !deletePassword}>{deleting ? 'Deleting…' : 'Delete permanently'}</button></div>
            </form>
          {/if}
        </div>

      {:else if tab === 'security'}
        <div class="recent-auth-note"><strong>Recent confirmation required</strong><span>Enter your website account password before changing email, password, or passkeys.</span></div>
        <div class="panel-section">
          <p class="account-label">Email notifications</p>
          <p class="panel-hint">Security notices such as sign-ins and account changes are always sent. Choose optional updates below.</p>
          {#if notificationPreferencesError}<p class="auth-error" role="alert">{notificationPreferencesError}</p>{/if}
          <div class="notification-options" role="group" aria-label="Optional email notifications">
            <label class="notification-option"><input type="checkbox" bind:checked={notificationPreferences.betaReleases} /><span>New beta releases</span></label>
            <label class="notification-option"><input type="checkbox" bind:checked={notificationPreferences.supportReplies} /><span>Support replies</span></label>
            <label class="notification-option"><input type="checkbox" bind:checked={notificationPreferences.productAnnouncements} /><span>Product announcements</span></label>
          </div>
          <div class="notification-actions"><button class="button button-soft button-sm" type="button" on:click={saveNotificationPreferences} disabled={notificationPreferencesSaving}>{notificationPreferencesSaving ? 'Saving…' : 'Save preferences'}</button></div>
        </div>
        {#if passwordError}<p class="auth-error" role="alert">{passwordError}</p>{/if}
        <div class="panel-section">
          <p class="account-label">Email address</p>
          <form class="account-form" on:submit|preventDefault={startEmailChange}>
            <label>New email<input type="email" autocomplete="email" bind:value={newEmail} required maxlength="254" /></label>
            <label>Account password<input type="password" autocomplete="current-password" bind:value={securityPassword} required /></label>
            <button class="button button-soft" type="submit" disabled={securityBusy}>Send confirmation to new email</button>
          </form>
        </div>
        <div class="panel-section">
          <p class="account-label">Password</p>
          <form class="account-form" on:submit|preventDefault={submitPassword}>
            <label>Current password<input type="password" autocomplete="current-password" bind:value={currentPassword} required /></label>
            <label>New password<input type="password" autocomplete="new-password" bind:value={newPassword} minlength="12" required /></label>
            <label>Confirm new password<input type="password" autocomplete="new-password" bind:value={confirmPassword} required /></label>
            <button class="button" type="submit" disabled={passwordSaving}>{passwordSaving ? 'Saving…' : 'Update password and revoke other sessions'}</button>
          </form>
        </div>
        {#if canUsePasskey}
          <div class="panel-section">
            <p class="account-label">Passkeys</p>
            <p class="panel-hint">Passkeys sign in to this website account. They do not unlock the desktop vault.</p>
            {#if passkeyError}<p class="auth-error" role="alert">{passkeyError}</p>{/if}
            {#if passkeys.length > 0}<div class="device-list">{#each passkeys as passkey (passkey.id)}<div class="device-row"><div class="device-details"><strong>{passkey.name}</strong><span>Added {formatDate(passkey.createdAt)}</span></div><button class="device-remove" type="button" on:click={() => removePasskey(passkey.id)} disabled={securityBusy}>Remove</button></div>{/each}</div>{:else if passkeysLoaded}<p class="device-empty">No passkeys.</p>{/if}
            <form class="account-form compact-form" on:submit|preventDefault={addPasskey}>
              <label>Name<input type="text" bind:value={newPasskeyName} maxlength="64" placeholder="This laptop" /></label>
              <label>Account password<input type="password" autocomplete="current-password" bind:value={securityPassword} required /></label>
              <button class="button button-soft" type="submit" disabled={securityBusy}>Add passkey</button>
            </form>
          </div>
        {/if}

      {:else if tab === 'sessions'}
        <div class="panel-section">
          <div class="device-section-head"><div><p class="account-label">Website sessions</p><p class="panel-hint">Browsers signed in to {siteHost}. Desktop links are listed separately.</p></div><button class="button button-soft button-sm" on:click={loadSessions} disabled={sessionsLoading}>{sessionsLoading ? 'Checking…' : 'Refresh'}</button></div>
          {#if sessionError}<p class="auth-error" role="alert">{sessionError}</p>{/if}
          <label class="inline-password">Account password<input type="password" autocomplete="current-password" bind:value={sessionsPassword} placeholder="Required to revoke" /></label>
          {#if sessions.length > 0}<div class="device-list">{#each sessions as session (session.id)}<div class="device-row"><div class="device-details"><strong>{session.label || 'Browser session'} {#if session.current}<span class="current-pill">Current</span>{/if}</strong><span>Last used {formatDate(session.lastSeenAt)}</span><small>Expires {formatDate(session.expiresAt)}</small></div><button class="device-remove" on:click={() => removeSession(session)} disabled={revokingSession === session.id}>{revokingSession === session.id ? 'Revoking…' : 'Revoke'}</button></div>{/each}</div>{:else if sessionsLoaded}<p class="device-empty">No active website sessions.</p>{/if}
          {#if sessions.length > 1}<button class="danger-button danger-soft" type="button" on:click={removeAllSessions} disabled={sessionsLoading || !sessionsPassword}>Revoke every session</button>{/if}
        </div>

      {:else if tab === 'devices'}
        <div class="recent-auth-note"><strong>Account password required</strong><span>Desktop link and removal actions require a recent confirmation. Linking also requires a verified beta account.</span></div>
        <label class="inline-password">Account password<input type="password" autocomplete="current-password" bind:value={devicesPassword} placeholder="Required for changes" /></label>
        {#if deviceError}<p class="auth-error" role="alert">{deviceError}</p>{/if}
        <div class="panel-section link-section">
          <p class="account-label">Connect a Windows desktop</p>
          {#if !desktopLinkReady}
            <p class="auth-error" role="status">{desktopLinkBlocker}</p>
            {#if !account.emailVerified}<button class="button button-soft button-sm" type="button" on:click={resendVerification} disabled={working}>{working ? 'Sending…' : 'Send verification email'}</button>{/if}
          {:else if link?.state === 'pending' && link.code && linkSeconds > 0}
            <div class="desktop-link-card">
              <div class="desktop-link-code"><span>One-time code</span><code aria-label={link.code}>{desktopCodeDisplay}</code></div>
              <div class="desktop-link-expiry" aria-label={`${linkSeconds} seconds remaining`}><span>Expires in</span><strong>{linkCountdown}</strong></div>
            </div>
            <p class="panel-hint">Enter this in Sesame Settings. It can be used once and expires automatically.</p>
            <div class="account-actions"><button class="button" type="button" on:click={copyDesktopCode}>{copied ? 'Copied' : 'Copy code'}</button><button class="button button-soft" type="button" on:click={makeDesktopLink} disabled={linking || !devicesPassword}>Regenerate</button><button class="device-remove" type="button" on:click={cancelLink} disabled={linking || !devicesPassword}>Cancel</button></div>
          {:else if link?.state === 'connected'}
            <div class="link-success" role="status"><strong>Desktop connected</strong><span>The one-time code is closed. You can manage the desktop below.</span></div>
            <button class="button button-soft" type="button" on:click={makeDesktopLink} disabled={linking || !devicesPassword}>Connect another desktop</button>
          {:else}
            <p class="panel-hint">Create a one-time code, then enter it in Sesame Settings. Creating a new code cancels the old one.</p>
            <button class="button" type="button" on:click={makeDesktopLink} disabled={linking || !devicesPassword}>{linking ? 'Creating…' : link?.state === 'expired' ? 'Create a new code' : 'Create desktop code'}</button>
          {/if}
        </div>
        <div class="panel-section">
          <div class="device-section-head"><div><p class="account-label">Connected desktops</p><p class="panel-hint">Removing a link does not remove or delete the local vault.</p></div><button class="button button-soft button-sm" type="button" on:click={loadDevices} disabled={devicesLoading}>{devicesLoading ? 'Checking…' : 'Refresh'}</button></div>
          {#if devices.length > 0}<div class="device-list">{#each devices as device (device.deviceId)}<div class="device-row"><div class="device-mark" aria-hidden="true"></div><div class="device-details">{#if renameDraftId === device.deviceId}<label class="device-rename-label" for="device-rename-{device.deviceId}">Desktop name</label><input id="device-rename-{device.deviceId}" class="device-rename-input" type="text" bind:value={renameDraftValue} maxlength="64" disabled={renamingDeviceId === device.deviceId} />{:else}<strong>{device.deviceName || 'Sesame desktop'}</strong>{/if}<span>Connected {formatDate(device.connectedAt)}</span><small>Authorization expires {formatDate(device.expiresAt)}</small></div>{#if renameDraftId === device.deviceId}<button class="device-remove" on:click={() => renameDevice(device)} disabled={renamingDeviceId === device.deviceId}>{renamingDeviceId === device.deviceId ? 'Saving…' : 'Save name'}</button><button class="device-remove" on:click={cancelRename} disabled={renamingDeviceId === device.deviceId}>Cancel</button>{:else}<button class="device-remove" on:click={() => startRename(device)} disabled={revokingDeviceId === device.deviceId}>Rename</button><button class="device-remove" on:click={() => removeDevice(device)} disabled={revokingDeviceId === device.deviceId}>{revokingDeviceId === device.deviceId ? 'Removing…' : 'Remove'}</button>{/if}</div>{/each}</div>{:else if devicesLoaded}<p class="device-empty">No desktops connected.</p>{/if}
        </div>

      {:else if tab === 'activity'}
        <div class="panel-section">
          <div class="device-section-head"><div><p class="account-label">Security activity</p><p class="panel-hint">A retained history of account-security changes. It never includes passwords, tokens, vault data, or IP addresses.</p></div><button class="button button-soft button-sm" type="button" on:click={loadActivity} disabled={activityLoading}>{activityLoading ? 'Checking…' : 'Refresh'}</button></div>
          {#if activityError}<p class="auth-error" role="alert">{activityError}</p>{/if}
          {#if activity.length > 0}<div class="device-list">{#each activity as event (event.id)}<div class="device-row"><div class="device-details"><strong>{event.type.replaceAll('_', ' ')}</strong><span>{event.label || 'Sesame account'}</span><small>{formatDate(event.createdAt)}</small></div></div>{/each}</div>{:else if activityLoaded}<p class="device-empty">No retained security activity.</p>{/if}
        </div>

      {:else}
        <div class="panel-section">
          <p class="account-label">Eligible downloads</p>
          <p class="panel-hint">Only account-gated builds with verified Tauri updater signatures and Sigstore publisher evidence appear here. Early-access installers are not Windows publisher-signed, so Windows may show an unknown-publisher warning.</p>
		  {#if downloads.length > 0}<div class="download-list">{#each downloads as release (release.id)}<article><div><strong>Sesame {release.version}</strong><span>{release.platform} · {release.updaterVerified ? 'Tauri updater signature verified' : 'Updater signature unavailable'} · {release.sigstoreVerified ? 'Sigstore release workflow verified' : 'Release workflow evidence unavailable'} · {release.authenticodeVerified ? 'Windows publisher verified' : 'Unsigned Windows early-access build'}</span><code>SHA-256: {release.sha256}</code></div>{#if release.updaterVerified && release.sigstoreVerified}<button class="button button-sm" type="button" on:click={() => startDownload(release)} disabled={downloadStarting === release.id}>{downloadStarting === release.id ? 'Preparing download…' : 'Download'}</button>{:else}<span class="release-unavailable">Withheld</span>{/if}</article>{/each}</div>{:else}<div class="empty-account-state"><strong>No build assigned</strong><p>When your beta access includes a build, its installer and verification evidence will appear here.</p></div>{/if}
          <a class="account-roadmap" href="/releases">Public release notes and supported Windows versions</a>
        </div>
      {/if}
    </div>
  {:else if authState.state === 'loading'}
    <div class="account-panel card account-loading" role="status"><span class="session-spinner" aria-hidden="true"></span><div><h2>Preparing your account</h2><p>Loading your account controls.</p></div></div>
  {:else if authState.state === 'offline' || authState.state === 'error'}
    <div class="account-panel card signed-out"><h2>Account service unavailable.</h2><p>{authState.state === 'offline' ? 'Reconnect and try again. Your session has not been signed out.' : authState.error.message}</p><button class="button button-soft" type="button" on:click={() => window.location.reload()}>Try again</button></div>
  {:else}
    <div class="account-panel card signed-out"><h2>You are signed out.</h2><p>Sign in to manage website services.</p><a class="button" href="/login">Sign in</a></div>
  {/if}
</section>
