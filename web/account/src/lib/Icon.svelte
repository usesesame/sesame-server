<script lang="ts">
  export let name: 'check' | 'chevron' | 'search' | 'copy' | 'close' | 'arrow' | 'download' | 'info' | 'warn' | 'error' | 'ok' | 'spin' | 'external' | 'menu'
  export let size = 16
</script>

<svg class="icon" width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
  {#if name === 'check'}
    <path d="M3 8.5l3.2 3L13 4.5" stroke-width="2" />
  {:else if name === 'chevron'}
    <path d="M4 6l4 4 4-4" stroke-width="1.6" />
  {:else if name === 'search'}
    <path d="M7 12A5 5 0 1 0 7 2a5 5 0 0 0 0 10zm3.6-1.4L14 14" stroke-width="1.6" />
  {:else if name === 'copy'}
    <path d="M5.5 5.5v-3h8v8h-3M2.5 5.5h8v8h-8z" />
  {:else if name === 'close'}
    <path d="M4 4l8 8M12 4l-8 8" stroke-width="1.6" />
  {:else if name === 'arrow'}
    <path d="M3 8h10M9 4l4 4-4 4" stroke-width="1.6" />
  {:else if name === 'download'}
    <path d="M8 2.5v8M4.5 7.5L8 11l3.5-3.5M3 13.5h10" stroke-width="1.6" />
  {:else if name === 'info'}
    <path d="M8 1.75a6.25 6.25 0 1 0 0 12.5a6.25 6.25 0 0 0 0-12.5z" />
    <path d="M8 7.3v3.6M8 5v.2" stroke-width="1.6" />
  {:else if name === 'warn'}
    <path d="M8 2.3l6.2 10.9H1.8z" />
    <path d="M8 6.7v2.8M8 11.4v.2" stroke-width="1.6" />
  {:else if name === 'error'}
    <path d="M8 1.75a6.25 6.25 0 1 0 0 12.5a6.25 6.25 0 0 0 0-12.5z" />
    <path d="M5.9 5.9l4.2 4.2M10.1 5.9l-4.2 4.2" stroke-width="1.6" />
  {:else if name === 'ok'}
    <path d="M8 1.75a6.25 6.25 0 1 0 0 12.5a6.25 6.25 0 0 0 0-12.5z" />
    <path d="M5.2 8.3l2 2 3.7-4.2" stroke-width="1.6" />
  {:else if name === 'spin'}
    <path d="M8 2.2a5.8 5.8 0 1 1-5.8 5.8" stroke-width="1.8" />
  {:else if name === 'external'}
    <path d="M6.5 3.5h-3v9h9v-3M9.5 2.5h4v4M13.2 2.8L7.5 8.5" />
  {:else if name === 'menu'}
    <path d="M2.5 4.5h11M2.5 8h11M2.5 11.5h11" stroke-width="1.6" />
  {/if}
</svg>
