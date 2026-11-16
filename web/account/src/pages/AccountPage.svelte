<script lang="ts">
  import { onDestroy, onMount } from 'svelte'
  import {
    cancelDesktopLink,
    changePassword,
    createDesktopLink,
    deleteAccount,
    deleteSyncVault,
    getAccountBootstrap,
    getAccountActivity,
    getNotificationPreferences,
    updateNotificationPreferences,
    getDesktopLink,
    getSyncVault,
    listDesktopDevices,
    listSessions,
    reauthenticate,
    requestEmailChange,
    requestEmailVerification,
    revokeAllSessions,
    revokeDesktopDevice,
    renameDesktopDevice,
    revokeSession,
    type Account,
    type AccountActivityEvent,
    type NotificationPreferences,
    type AuthState,
    type AccountAccess,
    type AccountSession,
    type DesktopDevice,
    type DesktopLink,
    type SyncVaultStatus,
  } from '../lib/auth'
  import { deletePasskey, listPasskeys, passkeysSupported, registerPasskey, type PasskeyInfo } from '../lib/passkey'
  import { siteOrigin } from '../lib/runtime-config'
  import { capabilities, capabilityEnabled, type CapabilityConfig } from '../lib/capabilities'
  import PasswordSetupForm from '../lib/PasswordSetupForm.svelte'
  import { nextStep } from '../lib/next-step'
  import { describeActivity } from '../lib/activity'
  import { accountTabLabel, type AccountTab } from '../lib/account-routes'
  import PageHeader from '../lib/PageHeader.svelte'
  import Notice from '../lib/Notice.svelte'
  import Icon from '../lib/Icon.svelte'

  export let account: Account | null
  export let authState: AuthState = { state: 'loading' }
  export let onSignedOut: () => void
  export let tab: AccountTab = 'overview'
  export let onSelectTab: (next: AccountTab) => void = (next) => { tab = next }

  let shownTab: AccountTab | null = null
  const siteHost = new URL(siteOrigin).host
  let now = Date.now()
  let clock: number | undefined
  let poll: number | undefined

  let working = false
  let error = ''
  let notice = ''
  let access: AccountAccess | null = null
  let supportUnread = 0
  let capabilityConfig: CapabilityConfig | null = null
  let accessLoaded = false
  let credentialSetupRequired = false
  let connectedDesktops = 0

  let currentPassword = ''
  let newPassword = ''
  let confirmPassword = ''
  let securityPassword = ''
  let newEmail = ''
  let passwordSaving = false
  let securityBusy = false
  let passwordError = ''
  let emailFormOpen = false
  let passwordFormOpen = false
  let passkeys: PasskeyInfo[] = []
  let passkeysLoaded = false
  let passkeyError = ''
  let newPasskeyName = ''
  let passkeyConfirmId = ''
  let passkeyRowError = ''
  let removingPasskeyId = ''
  let passkeyPasswordInput: HTMLInputElement | undefined
  const canUsePasskey = passkeysSupported()

  let sessions: AccountSession[] = []
  let sessionsLoaded = false
  let sessionsLoading = false
  let sessionsPassword = ''
  let sessionError = ''
  let revokingSession = ''
  let sessionConfirmId = ''
  let sessionRowError = ''
  let sessionsPasswordInput: HTMLInputElement | undefined
  let revokeAllConfirm = false
  let revokeAllError = ''
  let revokeAllPasswordInput: HTMLInputElement | undefined

  let link: DesktopLink | null = null
  let linking = false
  let copied = false
  let linkTotal = 0
  let linkTotalCode = ''
  let devicesPassword = ''
  let devices: DesktopDevice[] = []
  let devicesLoaded = false
  let devicesLoading = false
  let deviceError = ''
  let revokingDeviceId = ''
  let renamingDeviceId = ''
  let renameDraftId = ''
  let renameDraftValue = ''
  let deviceConfirmId = ''
  let deviceRowError = ''
  let devicesPasswordInput: HTMLInputElement | undefined

  let syncVault: SyncVaultStatus | null = null
  let syncVaultConfirm = false
  let syncVaultDeleting = false
  let syncVaultError = ''
  let syncVaultPasswordInput: HTMLInputElement | undefined

  let activity: AccountActivityEvent[] = []
  let activityLoaded = false
  let activityLoading = false
  let activityError = ''

  let notificationPreferences: NotificationPreferences = { betaReleases: false, supportReplies: false, productAnnouncements: false }
  let notificationPreferencesLoaded = false
  let notificationPreferencesSaving = false
  let notificationPreferencesError = ''
  const supportReplyNoticeKey = 'sesame-support-reply-email-notice'
  let supportReplyNoticeVisible = false

  let deleteOpen = false
  let deletePassword = ''
  let deleteConfirm = ''
  let deleting = false
  let deleteError = ''

  $: sessionsBusy = Boolean(revokingSession) || sessionsLoading
  $: deviceRowBusy = Boolean(revokingDeviceId || renamingDeviceId) || linking || syncVaultDeleting
  $: linkSeconds = link?.expiresAt ? Math.max(0, Math.ceil((new Date(link.expiresAt).getTime() - now) / 1000)) : 0
  $: linkCountdown = `${Math.floor(linkSeconds / 60)}:${String(linkSeconds % 60).padStart(2, '0')}`
  $: desktopCode = link?.code ? splitDesktopCode(link.code) : { prefix: '', groups: [] as string[] }
  $: trackLinkTotal(link?.code ?? '', linkSeconds)
  $: linkFraction = linkTotal > 0 ? Math.min(1, linkSeconds / linkTotal) : 0
  $: headerFact = pageFact(tab, account, sessionsLoaded, sessions.length, devicesLoaded || accessLoaded, desktopCount, activityLoaded, activity.length)
  $: headerDetail = tab === 'security' && account ? account.emailVerified ? 'email verified' : 'email not verified' : tab === 'overview' && account && accessLoaded ? betaGranted ? 'beta access' : 'no beta access' : tab === 'devices' && link?.code ? 'a code is waiting' : ''
  $: if (tab !== shownTab) showTab(tab)
  $: betaGranted = access?.betaAccess ?? account?.betaAccess ?? false
  $: desktopCount = devicesLoaded ? devices.length : connectedDesktops
  $: step = account && accessLoaded
    ? nextStep({
        email: account.email,
        emailVerified: account.emailVerified,
        betaGranted,
        desktopLinkReady,
        connectedDesktops: desktopCount,
      })
    : null
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
    supportReplyNoticeVisible = supportReplyNoticeUnseen()
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
      credentialSetupRequired = bootstrap.security?.credentialSetupRequired === true
      connectedDesktops = bootstrap.security?.connectedDesktops ?? 0
    } catch { /* Account can still manage security if entitlement data is unavailable. */ }
    accessLoaded = true
  }

  function supportReplyNoticeUnseen() {
    try {
      return localStorage.getItem(supportReplyNoticeKey) !== 'seen'
    } catch {
      return true
    }
  }

  function dismissSupportReplyNotice() {
    supportReplyNoticeVisible = false
    try {
      localStorage.setItem(supportReplyNoticeKey, 'seen')
    } catch {
      return
    }
  }

  function formatDate(value?: string) {
    if (!value) return 'Unknown'
    const date = new Date(value)
    if (Number.isNaN(date.getTime())) return 'Unknown'
    return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(date)
  }

  function splitDesktopCode(value: string) {
    const prefix = value.startsWith('SESAME-') ? 'SESAME-' : ''
    const code = prefix ? value.slice(prefix.length) : value
    return { prefix, groups: code.replace(/\s+/g, '').match(/.{1,4}/g) ?? [code] }
  }

  function trackLinkTotal(code: string, seconds: number) {
    if (code !== linkTotalCode) { linkTotalCode = code; linkTotal = seconds }
    else if (seconds > linkTotal) linkTotal = seconds
  }

  function plural(count: number, word: string) {
    if (count === 0) return `No ${word}s`
    return `${count} ${word}${count === 1 ? '' : 's'}`
  }

  function pageFact(current: AccountTab, who: Account | null, haveSessions: boolean, sessionCount: number, haveDesktops: boolean, desktops: number, haveActivity: boolean, events: number) {
    if (!who) return ''
    if (current === 'overview' || current === 'security') return who.email
    if (current === 'sessions') return haveSessions ? plural(sessionCount, 'session') : ''
    if (current === 'devices') return haveDesktops ? plural(desktops, 'desktop') : ''
    return haveActivity ? plural(events, 'event') : ''
  }

  function platformName(value?: string) {
    const names: Record<string, string> = { linux: 'Linux', windows: 'Windows', macos: 'macOS', darwin: 'macOS' }
    return value ? names[value.toLowerCase()] ?? value : ''
  }

  function showTab(next: AccountTab) {
    const first = shownTab === null
    shownTab = next
    if (!first) { error = ''; notice = '' }
    if (next === 'security' && canUsePasskey && !passkeysLoaded) void loadPasskeys()
    if (next === 'security' && !notificationPreferencesLoaded) void loadNotificationPreferences()
    if (next === 'sessions' && !sessionsLoaded) void loadSessions()
    if (next === 'devices' && !devicesLoaded) void loadDevicesAndLink()
    if (next === 'activity' && !activityLoaded) void loadActivity()
    if (next === 'overview' && !first && !accessLoaded) void loadAccess()
  }

  function askForPassword(input: HTMLInputElement | undefined) {
    input?.focus()
    input?.scrollIntoView?.({ block: 'center' })
  }

  async function confirmSensitive(password: string) {
    if (!password) throw new Error('Enter your website account password to continue.')
    await reauthenticate(password)
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
      emailFormOpen = false
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
      passwordFormOpen = false
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

  function askRemovePasskey(id: string) {
    passkeyConfirmId = id; passkeyRowError = ''; passkeyError = ''
  }

  function keepPasskey() {
    passkeyConfirmId = ''; passkeyRowError = ''
  }

  async function removePasskey(id: string) {
    passkeyRowError = ''
    if (!securityPassword) {
      passkeyRowError = 'Enter your account password, then remove the passkey again.'
      askForPassword(passkeyPasswordInput)
      return
    }
    securityBusy = true; removingPasskeyId = id
    try {
      await confirmSensitive(securityPassword)
      await deletePasskey(id)
      securityPassword = ''
      passkeyConfirmId = ''
      passkeys = passkeys.filter((passkey) => passkey.id !== id)
    } catch (reason) { passkeyRowError = reason instanceof Error ? reason.message : 'That passkey could not be removed.' }
    finally { securityBusy = false; removingPasskeyId = '' }
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
    if (deviceRowBusy) return
    deviceConfirmId = ''; deviceRowError = ''
    renameDraftId = device.deviceId; renameDraftValue = device.deviceName; deviceError = ''
  }

  function cancelRename() {
    if (deviceRowBusy) return
    renameDraftId = ''; renameDraftValue = ''
  }

  async function renameDevice(device: DesktopDevice) {
    if (deviceRowBusy || renameDraftId !== device.deviceId) return
    const name = renameDraftValue.trim()
    if (!name) { deviceError = 'Enter a desktop name.'; return }
    renamingDeviceId = device.deviceId; deviceError = ''
    try {
      await confirmSensitive(devicesPassword)
      await renameDesktopDevice(device.deviceId, name)
      devices = devices.map((item) => item.deviceId === device.deviceId ? { ...item, deviceName: name } : item)
      devicesPassword = ''
      if (renameDraftId === device.deviceId) { renameDraftId = ''; renameDraftValue = '' }
      notice = 'Desktop name updated.'
    } catch (reason) { deviceError = reason instanceof Error ? reason.message : 'That desktop name could not be updated.' }
    finally { renamingDeviceId = '' }
  }

  function askRevokeSession(session: AccountSession) {
    if (sessionsBusy) return
    revokeAllConfirm = false; revokeAllError = ''
    sessionConfirmId = session.id; sessionRowError = ''; sessionError = ''
  }

  function keepSession() {
    if (sessionsBusy) return
    sessionConfirmId = ''; sessionRowError = ''
  }

  async function removeSession(session: AccountSession) {
    if (sessionsBusy || sessionConfirmId !== session.id) return
    sessionRowError = ''
    if (!sessionsPassword) {
      sessionRowError = 'Enter your account password, then revoke the session again.'
      askForPassword(sessionsPasswordInput)
      return
    }
    revokingSession = session.id; sessionError = ''
    try {
      await confirmSensitive(sessionsPassword)
      await revokeSession(session.id)
      sessionsPassword = ''
      if (sessionConfirmId === session.id) sessionConfirmId = ''
      sessions = sessions.filter((item) => item.id !== session.id)
      if (session.current) { onSignedOut(); window.location.assign('/login') }
    } catch (reason) {
      const message = reason instanceof Error ? reason.message : 'That session could not be revoked.'
      if (sessionConfirmId === session.id) sessionRowError = message
      else sessionError = message
    }
    finally { revokingSession = '' }
  }

  function askRevokeAllSessions() {
    if (sessionsBusy) return
    sessionConfirmId = ''; sessionRowError = ''; sessionError = ''
    revokeAllConfirm = true; revokeAllError = ''
  }

  function keepAllSessions() {
    if (sessionsBusy) return
    revokeAllConfirm = false; revokeAllError = ''
  }

  async function removeAllSessions() {
    if (sessionsBusy || !revokeAllConfirm) return
    revokeAllError = ''
    if (!sessionsPassword) {
      revokeAllError = 'Enter your account password, then revoke every session again.'
      askForPassword(revokeAllPasswordInput)
      return
    }
    sessionsLoading = true; sessionError = ''
    try {
      await confirmSensitive(sessionsPassword)
      await revokeAllSessions()
      onSignedOut(); window.location.assign('/login')
    } catch (reason) { revokeAllError = reason instanceof Error ? reason.message : 'Sessions could not be revoked.'; sessionsLoading = false }
  }

  async function refreshDesktopLink(showError = true) {
    try {
      const state = await getDesktopLink()
      if (link?.code && state.state === 'pending') link = { ...state, code: link.code }
      else link = state
      if (state.state === 'connected') { notice = 'Desktop connected. The one-time code can no longer be used.'; await loadDevices() }
    } catch (reason) { if (showError) deviceError = reason instanceof Error ? reason.message : 'Link status is unavailable.' }
  }

  async function loadDevicesAndLink() { await Promise.all([loadDevices(), refreshDesktopLink(false), loadSyncVault()]) }

  async function refreshDevices() { await Promise.all([loadDevices(), loadSyncVault()]) }

  async function loadSyncVault() {
    try { syncVault = await getSyncVault() ?? null }
    catch { syncVault = null }
  }

  function askDeleteSyncVault() {
    if (deviceRowBusy) return
    deviceConfirmId = ''; deviceRowError = ''; renameDraftId = ''; renameDraftValue = ''
    syncVaultConfirm = true; syncVaultError = ''; deviceError = ''
  }

  function keepSyncVault() {
    if (deviceRowBusy) return
    syncVaultConfirm = false; syncVaultError = ''
  }

  async function removeSyncVault() {
    if (deviceRowBusy || !syncVaultConfirm) return
    syncVaultError = ''
    if (!devicesPassword) {
      syncVaultError = 'Enter your account password, then delete the synced vault again.'
      askForPassword(syncVaultPasswordInput)
      return
    }
    syncVaultDeleting = true
    try {
      await confirmSensitive(devicesPassword)
      await deleteSyncVault()
      devicesPassword = ''
      syncVaultConfirm = false
      syncVault = syncVault ? { ...syncVault, hasVault: false, approvedDevices: 0, updatedAt: undefined } : null
      notice = 'Synced vault deleted.'
    } catch (reason) { syncVaultError = reason instanceof Error ? reason.message : 'The synced vault could not be deleted.' }
    finally { syncVaultDeleting = false }
  }

  async function loadDevices() {
    devicesLoading = true; deviceError = ''
    try { devices = await listDesktopDevices(); devicesLoaded = true }
    catch (reason) { deviceError = reason instanceof Error ? reason.message : 'Connected desktops are temporarily unavailable.' }
    finally { devicesLoading = false }
  }

  async function makeDesktopLink() {
    if (deviceRowBusy) return
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
    if (deviceRowBusy) return
    linking = true; deviceError = ''
    try {
      await confirmSensitive(devicesPassword)
      await cancelDesktopLink()
      devicesPassword = ''; link = { state: 'none' }
    } catch (reason) { deviceError = reason instanceof Error ? reason.message : 'The link could not be cancelled.' }
    finally { linking = false }
  }

  function askRemoveDevice(device: DesktopDevice) {
    if (deviceRowBusy) return
    renameDraftId = ''; renameDraftValue = ''
    deviceConfirmId = device.deviceId; deviceRowError = ''; deviceError = ''
  }

  function keepDevice() {
    if (deviceRowBusy) return
    deviceConfirmId = ''; deviceRowError = ''
  }

  async function removeDevice(device: DesktopDevice) {
    if (deviceRowBusy || deviceConfirmId !== device.deviceId) return
    deviceRowError = ''
    if (!devicesPassword) {
      deviceRowError = 'Enter your account password, then remove the desktop again.'
      askForPassword(devicesPasswordInput)
      return
    }
    revokingDeviceId = device.deviceId; deviceError = ''
    try {
      await confirmSensitive(devicesPassword)
      await revokeDesktopDevice(device.deviceId)
      devicesPassword = ''
      if (deviceConfirmId === device.deviceId) deviceConfirmId = ''
      devices = devices.filter((item) => item.deviceId !== device.deviceId)
    } catch (reason) {
      const message = reason instanceof Error ? reason.message : 'This desktop could not be removed.'
      if (deviceConfirmId === device.deviceId) deviceRowError = message
      else deviceError = message
    }
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

<div class="page">
  <PageHeader title={accountTabLabel(tab)} fact={headerFact} detail={headerDetail} />

  {#if account}
    {#if notice}<Notice kind="ok" title={notice} role="status" />{/if}
    {#if error}<Notice kind="danger" title={error} role="alert" />{/if}

    {#if tab === 'overview'}
      {#if credentialSetupRequired}
        <section class="group" aria-labelledby="setup-title">
          <h2 class="section-title" id="setup-title">Set a password</h2>
          <p class="group-note">This account has no password yet, so you cannot sign in again after this session ends. Choose one now, or add a passkey on the Security page.</p>
          <PasswordSetupForm onComplete={() => { credentialSetupRequired = false; notice = 'Password set.' }} />
        </section>
      {/if}
      {#if step}
        <section class="group next-step" aria-labelledby="next-step-title">
          <h2 class="section-title" id="next-step-title">{step.title}</h2>
          <p class="group-note">{step.body}</p>
          {#if step.action || step.secondary}
            <div class="button-row">
              {#if step.action?.kind === 'verify-email'}
                <button class="button" type="button" on:click={resendVerification} disabled={working}>{working ? 'Sending…' : step.action.label}</button>
              {:else if step.action?.kind === 'tab'}
                <button class="button" type="button" on:click={() => step.action?.kind === 'tab' && onSelectTab(step.action.tab)}>{step.action.label}</button>
              {:else if step.action?.kind === 'link'}
                <a class="button" href={step.action.path}>{step.action.label}</a>
              {/if}
              {#if step.secondary}<a class="button button-soft" href={`${siteOrigin}${step.secondary.path}`}>{step.secondary.label}</a>{/if}
            </div>
          {/if}
        </section>
      {/if}
      <section class="group" aria-labelledby="details-title">
        <h2 class="section-title" id="details-title">Account</h2>
        <dl class="spec-rows">
          <div><dt>Signed in as</dt><dd><span class="spec-value">{account.email}</span><span class={account.emailVerified ? 'tag tag-ok' : 'tag tag-warn'}>{account.emailVerified ? 'Email verified' : 'Email not verified'}</span></dd></div>
          <div><dt>Beta access</dt><dd>{betaGranted ? 'Granted' : 'Not granted'}</dd></div>
          <div><dt>Desktops</dt><dd>{desktopCount === 0 ? 'None connected' : `${desktopCount} connected`}<button class="button button-text button-sm spec-action" type="button" on:click={() => onSelectTab('devices')}>Manage</button></dd></div>
          <div><dt>Support</dt><dd>{supportUnread > 0 ? `${supportUnread} unread` : 'Up to date'}<a class="button button-text button-sm spec-action" href="/support">Your requests and replies</a></dd></div>
          <div><dt>Licences</dt><dd>{access?.licences.length ? access.licences.length : 'None yet'}{#if !access?.licences.length}<span class="spec-note">Purchases will show here when sales open.</span>{/if}</dd></div>
        </dl>
      </section>
      <section class="group danger-zone" aria-labelledby="delete-title">
        <h2 class="section-title" id="delete-title">Delete website account</h2>
        <p class="group-note">Deletes sessions, eligibility, licences, and desktop links. Local vaults are untouched.</p>
        {#if !deleteOpen}
          <button class="button button-danger-text button-flush" type="button" on:click={() => { deleteOpen = true; deleteError = '' }}>Delete account</button>
        {:else}
          <form class="account-form" on:submit|preventDefault={confirmDelete}>
            <label>Account password<input type="password" autocomplete="current-password" bind:value={deletePassword} required /></label>
            <label>Type DELETE<input type="text" autocomplete="off" bind:value={deleteConfirm} /></label>
            {#if deleteError}<p class="inline-notice inline-danger" role="alert"><Icon name="error" size={15} />{deleteError}</p>{/if}
            <div class="button-row"><button class="button button-danger" type="submit" disabled={deleting || deleteConfirm !== 'DELETE' || !deletePassword}>{deleting ? 'Deleting…' : 'Delete permanently'}</button><button class="button button-secondary" type="button" on:click={() => { deleteOpen = false; deletePassword = ''; deleteConfirm = '' }}>Cancel</button></div>
          </form>
        {/if}
      </section>

    {:else if tab === 'security'}
      <section class="group" aria-labelledby="signin-title">
        <h2 class="section-title" id="signin-title">Sign-in</h2>
        {#if passwordError}<p class="inline-notice inline-danger group-alert" role="alert"><Icon name="error" size={15} />{passwordError}</p>{/if}
        <div class="rows">
          <div class="settings-row">
            <div class="row-text"><h3 class="row-title" id="email-title">Email address</h3><p class="row-meta">{account.email}</p></div>
            <button class="button button-secondary button-sm" type="button" aria-expanded={emailFormOpen} aria-controls="email-form" on:click={() => (emailFormOpen = !emailFormOpen)}>Change email</button>
            {#if emailFormOpen}
              <form id="email-form" class="account-form row-form" aria-labelledby="email-title" on:submit|preventDefault={startEmailChange}>
                <label>New email<input type="email" autocomplete="email" bind:value={newEmail} required maxlength="254" /></label>
                <label>Account password<input type="password" autocomplete="current-password" bind:value={securityPassword} required /></label>
                <div class="button-row"><button class="button button-secondary" type="submit" disabled={securityBusy}>Send confirmation to new email</button></div>
              </form>
            {/if}
          </div>
          <div class="settings-row">
            <div class="row-text"><h3 class="row-title" id="password-title">Password</h3><p class="row-meta">Changing it signs out every other session.</p></div>
            <button class="button button-secondary button-sm" type="button" aria-expanded={passwordFormOpen} aria-controls="password-form" on:click={() => (passwordFormOpen = !passwordFormOpen)}>Change password</button>
            {#if passwordFormOpen}
              <form id="password-form" class="account-form row-form" aria-labelledby="password-title" on:submit|preventDefault={submitPassword}>
                <label>Current password<input type="password" autocomplete="current-password" bind:value={currentPassword} required /></label>
                <label>New password<input type="password" autocomplete="new-password" bind:value={newPassword} minlength="12" required /></label>
                <label>Confirm new password<input type="password" autocomplete="new-password" bind:value={confirmPassword} required /></label>
                <div class="button-row"><button class="button" type="submit" disabled={passwordSaving}>{passwordSaving ? 'Saving…' : 'Update password and revoke other sessions'}</button></div>
              </form>
            {/if}
          </div>
        </div>
      </section>
      {#if canUsePasskey}
        <section class="group" aria-labelledby="passkeys-title">
          <h2 class="section-title" id="passkeys-title">Passkeys</h2>
          <p class="group-note">Passkeys sign in to this website account. They do not unlock the desktop vault.</p>
          {#if passkeyError}<p class="inline-notice inline-danger group-alert" role="alert"><Icon name="error" size={15} />{passkeyError}</p>{/if}
          {#if passkeys.length > 0}
            <ul class="rows">
              {#each passkeys as passkey (passkey.id)}
                <li class="list-row" class:confirming={passkeyConfirmId === passkey.id}>
                  {#if passkeyConfirmId === passkey.id}
                    <div class="row-text"><p class="row-title">Remove the passkey "{passkey.name}"?</p> <p class="row-meta">It can no longer sign in to this account.</p></div>
                    <div class="row-actions"><label class="row-password">Account password<input type="password" autocomplete="current-password" bind:value={securityPassword} bind:this={passkeyPasswordInput} disabled={securityBusy} /></label><button class="button button-danger" type="button" on:click={() => removePasskey(passkey.id)} disabled={securityBusy}>{removingPasskeyId === passkey.id ? 'Removing…' : 'Remove passkey'}</button><button class="button button-secondary" type="button" on:click={keepPasskey} disabled={securityBusy}>Cancel</button></div>
                    {#if passkeyRowError}<p class="inline-notice inline-danger row-error" role="alert"><Icon name="error" size={15} />{passkeyRowError}</p>{/if}
                  {:else}
                    <div class="row-text"><p class="row-name"><strong>{passkey.name}</strong></p><p class="row-meta row-facts">added {formatDate(passkey.createdAt)}</p></div>
                    <div class="row-actions"><button class="button button-danger-text" type="button" on:click={() => askRemovePasskey(passkey.id)} disabled={securityBusy}>Remove</button></div>
                  {/if}
                </li>
              {/each}
            </ul>
          {:else if passkeysLoaded}<p class="rows-empty">No passkeys.</p>{/if}
          <form class="account-form inline-form" on:submit|preventDefault={addPasskey}>
            <label>Name<input type="text" bind:value={newPasskeyName} maxlength="64" placeholder="This laptop" /></label>
            <label>Account password<input type="password" autocomplete="current-password" bind:value={securityPassword} required /></label>
            <button class="button button-secondary" type="submit" disabled={securityBusy}>Add passkey</button>
          </form>
        </section>
      {/if}
      <section class="group" aria-labelledby="notifications-title">
        <h2 class="section-title" id="notifications-title">Email notifications</h2>
        <p class="group-note">Security notices such as sign-ins and account changes are always sent. Choose optional updates below.</p>
        {#if notificationPreferencesError}<p class="inline-notice inline-danger group-alert" role="alert"><Icon name="error" size={15} />{notificationPreferencesError}</p>{/if}
        <div class="check-rows" role="group" aria-label="Optional email notifications">
          <label class="check-row"><input class="check-input" type="checkbox" bind:checked={notificationPreferences.betaReleases} disabled={!notificationPreferencesLoaded} /><span class="check-box" aria-hidden="true"><Icon name="check" size={12} /></span><span>New beta releases</span></label>
          <label class="check-row"><input class="check-input" type="checkbox" bind:checked={notificationPreferences.supportReplies} disabled={!notificationPreferencesLoaded} /><span class="check-box" aria-hidden="true"><Icon name="check" size={12} /></span><span>Support replies</span></label>
          <label class="check-row"><input class="check-input" type="checkbox" bind:checked={notificationPreferences.productAnnouncements} disabled={!notificationPreferencesLoaded} /><span class="check-box" aria-hidden="true"><Icon name="check" size={12} /></span><span>Product announcements</span></label>
        </div>
        {#if supportReplyNoticeVisible}
          <Notice title="Reply email is now on for this account." body="You get an email when support answers a request." role="status">
            <button slot="action" class="button button-secondary button-sm" type="button" on:click={dismissSupportReplyNotice}>Got it</button>
          </Notice>
        {/if}
        <div class="button-row"><button class="button button-secondary button-sm" type="button" on:click={saveNotificationPreferences} disabled={notificationPreferencesSaving || !notificationPreferencesLoaded}>{notificationPreferencesSaving ? 'Saving…' : 'Save preferences'}</button></div>
      </section>

    {:else if tab === 'sessions'}
      <section class="group" aria-labelledby="sessions-title">
        <div class="group-head"><div><h2 class="section-title" id="sessions-title">Website sessions</h2><p class="group-note">Browsers signed in to {siteHost}. Desktop links are listed separately.</p></div><button class="button button-secondary button-sm" type="button" on:click={loadSessions} disabled={sessionsBusy}>{sessionsLoading ? 'Checking…' : 'Refresh'}</button></div>
        {#if sessionError}<p class="inline-notice inline-danger group-alert" role="alert"><Icon name="error" size={15} />{sessionError}</p>{/if}
        {#if sessions.length > 0}
          <ul class="rows">
            {#each sessions as session (session.id)}
              <li class="list-row" class:confirming={sessionConfirmId === session.id}>
                {#if sessionConfirmId === session.id}
                  <div class="row-text"><p class="row-title">{session.current ? 'Revoke this session?' : `Revoke ${session.label || 'this browser session'}?`}</p> <p class="row-meta">{session.current ? 'This browser is signed out and goes back to the sign-in page.' : 'That browser is signed out of this account.'}</p></div>
                  <div class="row-actions"><label class="row-password">Account password<input type="password" autocomplete="current-password" bind:value={sessionsPassword} bind:this={sessionsPasswordInput} disabled={sessionsBusy} /></label><button class="button button-danger" type="button" on:click={() => removeSession(session)} disabled={sessionsBusy}>{revokingSession === session.id ? 'Revoking…' : 'Revoke session'}</button><button class="button button-secondary" type="button" on:click={keepSession} disabled={sessionsBusy}>Cancel</button></div>
                  {#if sessionRowError}<p class="inline-notice inline-danger row-error" role="alert"><Icon name="error" size={15} />{sessionRowError}</p>{/if}
                {:else}
                  <div class="row-text"><p class="row-name"><strong>{session.label || 'Browser session'}</strong>{#if session.current}<span class="tag">This browser</span>{/if}</p><p class="row-meta row-facts"><span>last used {formatDate(session.lastSeenAt)}</span><span>expires {formatDate(session.expiresAt)}</span></p></div>
                  <div class="row-actions"><button class="button button-danger-text" type="button" on:click={() => askRevokeSession(session)} disabled={sessionsBusy}>Revoke</button></div>
                {/if}
              </li>
            {/each}
          </ul>
        {:else if sessionsLoaded}<p class="rows-empty">No active website sessions.</p>{/if}
        {#if sessions.length > 1}
          {#if revokeAllConfirm}
            <div class="list-row confirming" role="group" aria-labelledby="revoke-all-title">
              <div class="row-text"><p class="row-title" id="revoke-all-title">Revoke every session?</p> <p class="row-meta">Every browser is signed out of this account, this one included, and this browser goes back to the sign-in page.</p></div>
              <div class="row-actions"><label class="row-password">Account password<input type="password" autocomplete="current-password" bind:value={sessionsPassword} bind:this={revokeAllPasswordInput} disabled={sessionsBusy} /></label><button class="button button-danger" type="button" on:click={removeAllSessions} disabled={sessionsBusy}>{sessionsLoading ? 'Revoking…' : 'Revoke every session'}</button><button class="button button-secondary" type="button" on:click={keepAllSessions} disabled={sessionsBusy}>Cancel</button></div>
              {#if revokeAllError}<p class="inline-notice inline-danger row-error" role="alert"><Icon name="error" size={15} />{revokeAllError}</p>{/if}
            </div>
          {:else}
            <button class="button button-danger-text button-flush" type="button" on:click={askRevokeAllSessions} disabled={sessionsBusy}>Revoke every session</button>
            <p class="field-hint">Revoking every session signs out each browser, this one included. It needs your account password.</p>
          {/if}
        {/if}
      </section>

    {:else if tab === 'devices'}
      {#if deviceError && !renameDraftId}<p class="inline-notice inline-danger" role="alert"><Icon name="error" size={15} />{deviceError}</p>{/if}
      <section class="group" aria-labelledby="connect-title">
        <h2 class="section-title" id="connect-title">Connect a desktop</h2>
        {#if !desktopLinkReady}
          <Notice title={desktopLinkBlocker} role="status">
            <svelte:fragment slot="action">{#if !account.emailVerified}<button class="button button-secondary button-sm" type="button" on:click={resendVerification} disabled={working}>{working ? 'Sending…' : 'Send verification email'}</button>{/if}</svelte:fragment>
          </Notice>
        {:else if link?.state === 'pending' && link.code && linkSeconds > 0}
          <div class="code-card">
            <div class="code-card-head">
              <div><p class="code-card-title">Desktop code</p><p class="code-card-hint">Enter it in Sesame Settings before it expires.</p></div>
              <p class="code-time" aria-label={`${linkSeconds} seconds remaining`}><span class="code-time-value">{linkCountdown}</span><span class="code-time-label">left</span></p>
            </div>
            <svg class="code-time-bar" viewBox="0 0 100 3" preserveAspectRatio="none" aria-hidden="true" focusable="false"><rect class="code-time-track" width="100" height="3" /><rect class="code-time-fill" width={linkFraction * 100} height="3" /></svg>
            <code class="code-groups"><span class="visually-hidden">{link.code}</span><span class="code-group-row" aria-hidden="true">{#if desktopCode.prefix}<span class="code-prefix">{desktopCode.prefix}</span>{/if}{#each desktopCode.groups as group, index (index)}<span class="code-group">{group}</span>{/each}</span></code>
            <div class="code-card-foot">
              <p class="inline-notice inline-info"><Icon name="info" size={15} />Works once, for one desktop.</p>
              <button class="button button-secondary button-sm" type="button" on:click={copyDesktopCode}><Icon name="copy" size={14} />{copied ? 'Copied' : 'Copy code'}</button>
            </div>
            <div class="code-card-manage">
              <label class="row-password">Account password<input type="password" autocomplete="current-password" bind:value={devicesPassword} disabled={deviceRowBusy} /></label>
              <div class="code-card-actions"><button class="button button-text button-sm" type="button" on:click={makeDesktopLink} disabled={deviceRowBusy || !devicesPassword}>Regenerate</button><button class="button button-danger-text button-sm" type="button" on:click={cancelLink} disabled={deviceRowBusy || !devicesPassword}>Cancel</button></div>
            </div>
          </div>
        {:else if link?.state === 'connected'}
          <Notice kind="ok" title="Desktop connected" body="The one-time code is closed. You can manage the desktop below." role="status" />
          <div class="password-action">
            <label class="row-password">Account password<input type="password" autocomplete="current-password" bind:value={devicesPassword} disabled={deviceRowBusy} /></label>
            <button class="button button-secondary" type="button" on:click={makeDesktopLink} disabled={deviceRowBusy || !devicesPassword}>Connect another desktop</button>
          </div>
        {:else}
          <div class="password-action">
            <label class="row-password">Account password<input type="password" autocomplete="current-password" bind:value={devicesPassword} disabled={deviceRowBusy} /></label>
            <button class="button" type="button" on:click={makeDesktopLink} disabled={deviceRowBusy || !devicesPassword}>{linking ? 'Creating…' : link?.state === 'expired' ? 'Create a new code' : 'Create desktop code'}</button>
          </div>
          <p class="field-hint">You enter the code in Sesame Settings, and a new code cancels the old one.</p>
        {/if}
      </section>
      <section class="group" aria-labelledby="desktops-title">
        <div class="group-head"><div><h2 class="section-title" id="desktops-title">Connected desktops</h2><p class="group-note">Removing a link does not remove or delete the local vault.</p></div><button class="button button-secondary button-sm" type="button" on:click={refreshDevices} disabled={devicesLoading}>{devicesLoading ? 'Checking…' : 'Refresh'}</button></div>
        {#if devices.length > 0}
          <ul class="rows">
            {#each devices as device (device.deviceId)}
              <li class="list-row" class:confirming={deviceConfirmId === device.deviceId}>
                {#if deviceConfirmId === device.deviceId}
                  <div class="row-text"><p class="row-title">Remove {device.deviceName || 'this Sesame desktop'}?</p> <p class="row-meta">It is disconnected from this account until you connect it again with a new code. The vault on that computer is not touched.</p></div>
                  <div class="row-actions"><label class="row-password">Account password<input type="password" autocomplete="current-password" bind:value={devicesPassword} bind:this={devicesPasswordInput} disabled={deviceRowBusy} /></label><button class="button button-danger" type="button" on:click={() => removeDevice(device)} disabled={deviceRowBusy}>{revokingDeviceId === device.deviceId ? 'Removing…' : 'Remove desktop'}</button><button class="button button-secondary" type="button" on:click={keepDevice} disabled={deviceRowBusy}>Cancel</button></div>
                  {#if deviceRowError}<p class="inline-notice inline-danger row-error" role="alert"><Icon name="error" size={15} />{deviceRowError}</p>{/if}
                {:else}
                  <div class="row-text">
                    {#if renameDraftId === device.deviceId}<label class="row-rename-label" for="device-rename-{device.deviceId}">Desktop name</label><input id="device-rename-{device.deviceId}" class="row-rename-input" type="text" bind:value={renameDraftValue} maxlength="64" disabled={deviceRowBusy} /><label class="row-password">Account password<input type="password" autocomplete="current-password" bind:value={devicesPassword} disabled={deviceRowBusy} /></label>{#if deviceError}<p class="inline-notice inline-danger" role="alert"><Icon name="error" size={15} />{deviceError}</p>{/if}{:else}<p class="row-name"><strong>{device.deviceName || 'Sesame desktop'}</strong></p>{/if}
                    <p class="row-meta row-facts">{#if device.platform}<span>{platformName(device.platform)}</span>{/if}{#if device.appVersion}<span class="version">{device.appVersion}</span>{/if}<span>connected {formatDate(device.connectedAt)}</span><span>authorization expires {formatDate(device.expiresAt)}</span></p>
                  </div>
                  <div class="row-actions">{#if renameDraftId === device.deviceId}<button class="button button-text" type="button" on:click={() => renameDevice(device)} disabled={deviceRowBusy}>{renamingDeviceId === device.deviceId ? 'Saving…' : 'Save name'}</button><button class="button button-text" type="button" on:click={cancelRename} disabled={deviceRowBusy}>Cancel</button>{:else}<button class="button button-text" type="button" on:click={() => startRename(device)} disabled={deviceRowBusy}>Rename</button><button class="button button-danger-text" type="button" on:click={() => askRemoveDevice(device)} disabled={deviceRowBusy}>Remove</button>{/if}</div>
                {/if}
              </li>
            {/each}
          </ul>
        {:else if devicesLoaded}
          <div class="empty-state"><h3>No desktops yet</h3><p>Connect the desktop app to see it here. Your vault stays on that computer either way.</p></div>
        {/if}
      </section>
      {#if syncVault?.available && syncVault.hasVault}
        <section class="group" aria-labelledby="sync-vault-title">
          <h2 class="section-title" id="sync-vault-title">Synced vault</h2>
          <div class="rows">
            <div class="list-row" class:confirming={syncVaultConfirm} role="group" aria-labelledby="sync-vault-row-title">
              {#if syncVaultConfirm}
                <div class="row-text"><p class="row-title" id="sync-vault-row-title">Delete the synced vault?</p> <p class="row-meta">This removes the encrypted copy kept by Sesame. The vault on each of your devices stays, and a device can turn Sync on again afterwards.</p></div>
                <div class="row-actions"><label class="row-password">Account password<input type="password" autocomplete="current-password" bind:value={devicesPassword} bind:this={syncVaultPasswordInput} disabled={deviceRowBusy} /></label><button class="button button-danger" type="button" on:click={removeSyncVault} disabled={deviceRowBusy}>{syncVaultDeleting ? 'Deleting…' : 'Delete synced vault'}</button><button class="button button-secondary" type="button" on:click={keepSyncVault} disabled={deviceRowBusy}>Cancel</button></div>
                {#if syncVaultError}<p class="inline-notice inline-danger row-error" role="alert"><Icon name="error" size={15} />{syncVaultError}</p>{/if}
              {:else}
                <div class="row-text"><p class="row-name" id="sync-vault-row-title"><strong>Encrypted copy kept by Sesame</strong></p><p class="row-meta row-facts"><span>{plural(syncVault.approvedDevices, 'approved device')}</span><span>changed {formatDate(syncVault.updatedAt)}</span></p></div>
                <div class="row-actions"><button class="button button-danger-text" type="button" on:click={askDeleteSyncVault} disabled={deviceRowBusy}>Delete synced vault</button></div>
              {/if}
            </div>
          </div>
        </section>
      {/if}

    {:else if tab === 'activity'}
      <section class="group" aria-labelledby="activity-title">
        <div class="group-head"><div><h2 class="section-title" id="activity-title">Security activity</h2><p class="group-note">A retained history of account-security changes. It never includes passwords, tokens, vault data, or IP addresses.</p></div><button class="button button-secondary button-sm" type="button" on:click={loadActivity} disabled={activityLoading}>{activityLoading ? 'Checking…' : 'Refresh'}</button></div>
        {#if activityError}<Notice kind="danger" title={activityError} role="alert" />{/if}
        {#if activity.length > 0}
          <ul class="rows">
            {#each activity as event (event.id)}
              {@const described = describeActivity(event)}
              <li class="list-row activity-row"><div class="row-text"><p class="row-name"><strong>{described.title}</strong></p>{#if described.detail}<p class="row-meta">{described.detail}</p>{/if}</div><time class="row-time" datetime={event.createdAt}>{formatDate(event.createdAt)}</time></li>
            {/each}
          </ul>
        {:else if activityLoaded}<p class="rows-empty">No retained security activity.</p>{/if}
      </section>
    {/if}
  {:else if authState.state === 'loading'}
    <div class="page-loading" role="status"><span class="session-spinner" aria-hidden="true"></span><p>Loading your account</p></div>
  {:else if authState.state === 'offline' || authState.state === 'error'}
    <Notice kind="danger" title="Account service unavailable" body={authState.state === 'offline' ? 'Reconnect and try again. Your session has not been signed out.' : authState.error.message} role="status">
      <button slot="action" class="button button-secondary button-sm" type="button" on:click={() => window.location.reload()}>Try again</button>
    </Notice>
  {:else}
    <section class="group"><h2 class="section-title">You are signed out</h2><p class="group-note">Sign in to manage website services.</p><div class="button-row"><a class="button" href="/login">Sign in</a></div></section>
  {/if}
</div>
