<script lang="ts">
  import { tick } from 'svelte'
  import Icon from './Icon.svelte'
  import { ACCOUNT_TABS } from './account-routes'
  import { siteOrigin } from './runtime-config'

  export let current: string
  export let email = ''
  export let supportUnread = 0
  export let signingOut = false
  export let signOutError = ''
  export let onNavigate: (event: MouseEvent, path: string, fromMenu: boolean) => void
  export let onSignOut: () => void

  const siteHost = new URL(siteOrigin).host
  const destinations = [...ACCOUNT_TABS.map((item) => ({ id: item.id as string, label: item.label, path: item.path })), { id: 'support', label: 'Support', path: '/support' }]

  let menuOpen = false
  let menuButton: HTMLButtonElement | undefined

  async function closeMenu() {
    if (!menuOpen) return
    menuOpen = false
    await tick()
    menuButton?.focus()
  }

  function choose(event: MouseEvent, path: string) {
    const fromMenu = menuOpen
    menuOpen = false
    onNavigate(event, path, fromMenu)
    if (fromMenu) void tick().then(() => menuButton?.focus())
  }

  function keydown(event: KeyboardEvent) {
    if (event.key === 'Escape' && menuOpen) {
      event.preventDefault()
      void closeMenu()
    }
  }
</script>

<svelte:window on:keydown={keydown} />

<aside class="sidebar" class:open={menuOpen}>
  <div class="sidebar-inner">
    <div class="sidebar-top">
      <a class="sidebar-brand" href="/account" on:click={(event) => choose(event, '/account')}>
        <img class="brand-mark" src="/favicon.svg" alt="" />
        <span class="brand-words"><strong>Sesame</strong><span class="brand-portal">Account</span></span>
      </a>
      <button class="menu-button" type="button" aria-expanded={menuOpen} aria-controls="portal-menu" bind:this={menuButton} on:click={() => (menuOpen ? closeMenu() : (menuOpen = true))}>
        <Icon name={menuOpen ? 'close' : 'menu'} />Menu
      </button>
    </div>
    <div class="sidebar-menu" id="portal-menu">
      <nav class="sidebar-nav" aria-label="Portal navigation">
        {#each destinations as item (item.id)}
          <a href={item.path} aria-current={current === item.id ? 'page' : undefined} on:click={(event) => choose(event, item.path)}>
            <span>{item.label}</span>
            {#if item.id === 'support' && supportUnread > 0}<span class="nav-count" aria-label={`${supportUnread} unread support ${supportUnread === 1 ? 'reply' : 'replies'}`}>{supportUnread}</span>{/if}
          </a>
        {/each}
      </nav>
      <div class="sidebar-foot">
        <a class="sidebar-origin" href={siteOrigin}>{siteHost}</a>
        {#if email}<p class="sidebar-email">{email}</p>{/if}
        <button class="button button-text button-sm sidebar-signout" type="button" on:click={onSignOut} disabled={signingOut}>{signingOut ? 'Signing out…' : 'Sign out'}</button>
        {#if signOutError}<p class="inline-notice inline-danger" role="alert"><Icon name="error" size={15} />{signOutError}</p>{/if}
      </div>
    </div>
  </div>
</aside>
{#if menuOpen}<button class="menu-scrim" type="button" tabindex="-1" aria-label="Close the menu" on:click={closeMenu}></button>{/if}
