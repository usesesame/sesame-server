<script lang="ts">
  import Icon from './Icon.svelte'

  export let kind: 'info' | 'warn' | 'danger' | 'ok' = 'info'
  export let title: string
  export let body = ''
  export let role: 'status' | 'alert' | undefined = undefined
  export let label: string | undefined = undefined

  const icons = { info: 'info', warn: 'warn', danger: 'error', ok: 'ok' } as const
</script>

<div class={`notice notice-${kind}`} {role} aria-label={label}>
  <span class="notice-icon"><Icon name={icons[kind]} size={18} /></span>
  <div class="notice-text">
    <p class="notice-title">{title}</p>
    {#if body}<p class="notice-body">{body}</p>{/if}
    <slot />
  </div>
  {#if $$slots.action}<div class="notice-action"><slot name="action" /></div>{/if}
</div>
