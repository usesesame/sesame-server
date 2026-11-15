<script lang="ts">
  import type { Snippet } from 'svelte'

  const props: {
    message: string
    action: string
    onConfirm: () => void
    onCancel: () => void
    busy?: boolean
    ready?: boolean
    tone?: 'danger' | 'change'
    children?: Snippet
  } = $props()
</script>

<div class="confirm-strip" role="group" aria-label="Confirm the change">
  <p>{props.message}</p>
  {@render props.children?.()}
  <div class="confirm-actions">
    <button type="button" class={props.tone === 'change' ? 'primary' : 'destructive'} onclick={props.onConfirm} disabled={props.busy || props.ready === false}>{props.action}</button>
    <button type="button" class="quiet-button" onclick={props.onCancel} disabled={props.busy}>Cancel</button>
  </div>
</div>
