<script lang="ts">
  import { onMount } from 'svelte'
  import Brand from './components/Brand.svelte'
  import StepUpDialog from './components/StepUpDialog.svelte'
  import UpdateBanner from './components/UpdateBanner.svelte'
  import { onSessionEnded, onStepUpRequired, setAPIBase } from './lib/api'
  import { loadConfig } from './lib/owner'
  import { endSession, establishSession, session, signOutOwner } from './lib/session.svelte'
  import { requestStepUp } from './lib/stepup.svelte'
  import { refreshUpdateNotice, resetUpdateNotice } from './lib/updates.svelte'
  import type { Config } from './lib/types'
  import Audit from './views/Audit.svelte'
  import Devices from './views/Devices.svelte'
  import Members from './views/Members.svelte'
  import Owners from './views/Owners.svelte'
  import PairLanding from './views/PairLanding.svelte'
  import Pairing from './views/Pairing.svelte'
  import Settings from './views/Settings.svelte'
  import Setup from './views/Setup.svelte'
  import SignIn from './views/SignIn.svelte'
  import System from './views/System.svelte'
  import Updates from './views/Updates.svelte'

  type Phase = 'loading' | 'unavailable' | 'setup' | 'pair' | 'signin' | 'app'
  type ViewId = 'devices' | 'members' | 'pairing' | 'audit' | 'owners' | 'settings' | 'updates' | 'system'

  const nav: { id: ViewId; label: string }[] = [
    { id: 'devices', label: 'Devices' },
    { id: 'members', label: 'Members' },
    { id: 'pairing', label: 'Pairing' },
    { id: 'audit', label: 'Audit log' },
    { id: 'owners', label: 'Owners' },
    { id: 'settings', label: 'Settings' },
    { id: 'updates', label: 'Updates' },
    { id: 'system', label: 'System' },
  ]

  let phase = $state<Phase>('loading')
  let config = $state<Config | null>(null)
  let setupToken = $state('')
  let view = $state<ViewId>('devices')
  let pairingTarget = $state('self')
  let heading = $state<HTMLElement>()
  let unavailableMessage = $state('')

  function readView(): ViewId {
    const id = window.location.hash.replace(/^#\/?/, '')
    return nav.some((item) => item.id === id) ? (id as ViewId) : 'devices'
  }

  function takeSetupToken(): string {
    const params = new URLSearchParams(window.location.hash.replace(/^#/, ''))
    const token = params.get('token') ?? ''
    window.history.replaceState(null, '', window.location.pathname + window.location.search)
    return token
  }

  function pathName(): string {
    return window.location.pathname.replace(/\/+$/, '')
  }

  async function start() {
    const path = pathName()
    if (path === '/pair') {
      window.history.replaceState(null, '', window.location.pathname)
      phase = 'pair'
      return
    }
    try {
      config = await loadConfig()
      setAPIBase(config.apiBase)
    } catch {
      phase = 'unavailable'
      return
    }
    if (path === '/setup') {
      setupToken = takeSetupToken()
      phase = 'setup'
      return
    }
    const result = await establishSession()
    if (result.status === 'signed-in') {
      enterApp()
    } else if (result.status === 'signed-out') {
      phase = 'signin'
    } else {
      unavailableMessage = result.message
      phase = 'unavailable'
    }
  }

  function enterApp() {
    if (pathName() !== '') window.history.replaceState(null, '', '/')
    view = readView()
    phase = 'app'
    resetUpdateNotice()
    void refreshUpdateNotice()
  }

  function afterSetup() {
    setupToken = ''
    void establishSession().then((result) => {
      if (result.status === 'signed-in') {
        window.location.hash = ''
        enterApp()
      } else {
        window.history.replaceState(null, '', '/')
        phase = 'signin'
      }
    })
  }

  async function afterSignIn() {
    session.notice = ''
    const result = await establishSession()
    if (result.status === 'signed-in') enterApp()
  }

  function pairFor(memberId: string) {
    pairingTarget = memberId
    window.location.hash = '/pairing'
  }

  async function leave() {
    await signOutOwner()
    window.location.hash = ''
    phase = 'signin'
  }

  $effect(() => {
    if (phase === 'app' && session.owner === null) phase = 'signin'
  })

  onMount(() => {
    onStepUpRequired(requestStepUp)
    onSessionEnded(() => {
      if (phase !== 'app') return
      endSession('Your session ended.')
      phase = 'signin'
    })
    const onHash = () => {
      if (phase === 'setup') {
        const token = takeSetupToken()
        if (token) setupToken = token
        return
      }
      if (phase !== 'app') return
      view = readView()
      heading?.focus()
    }
    window.addEventListener('hashchange', onHash)
    void start()
    return () => {
      window.removeEventListener('hashchange', onHash)
      onStepUpRequired(null)
      onSessionEnded(null)
    }
  })
</script>

{#if phase === 'loading'}
  <div class="center"><div class="spinner" role="status" aria-label="Loading"></div></div>
{:else if phase === 'unavailable'}
  <main class="auth-shell">
    <div class="auth-panel">
      <Brand />
      <h1>The console cannot reach the server</h1>
      <p>{unavailableMessage || 'Check that the server is running, then reload.'}</p>
    </div>
  </main>
{:else if phase === 'pair'}
  <PairLanding />
{:else if phase === 'setup'}
  {#key setupToken}<Setup token={setupToken} onDone={afterSetup} />{/key}
{:else if phase === 'signin'}
  <SignIn notice={session.notice} setupRequired={config?.setupRequired ?? false} onDone={afterSignIn} />
{:else}
  <div class="app-shell">
    <aside class="sidebar">
      <Brand />
      <nav aria-label="Console">
        {#each nav as item (item.id)}
          <a href="#/{item.id}" aria-current={view === item.id ? 'page' : undefined} onclick={() => { pairingTarget = 'self' }}>{item.label}</a>
        {/each}
      </nav>
      <div class="signed-in">
        <strong>{session.owner?.name}</strong>
        <span>{session.instanceName}{config ? ` · ${config.version}` : ''}</span>
        <button type="button" onclick={leave}>Sign out</button>
      </div>
    </aside>
    <div class="workspace">
      {#if view !== 'updates'}<UpdateBanner />{/if}
      <main class="content" tabindex="-1" bind:this={heading}>
        {#key view}
          {#if view === 'devices'}<Devices />
          {:else if view === 'members'}<Members onPair={pairFor} />
          {:else if view === 'pairing'}<Pairing initialTarget={pairingTarget} />
          {:else if view === 'audit'}<Audit />
          {:else if view === 'owners'}<Owners />
          {:else if view === 'settings'}<Settings />
          {:else if view === 'updates'}<Updates />
          {:else}<System />
          {/if}
        {/key}
      </main>
    </div>
  </div>
  <StepUpDialog />
{/if}
