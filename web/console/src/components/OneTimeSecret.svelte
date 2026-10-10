<script lang="ts">
  import { onDestroy, onMount } from 'svelte'
  import { formatCountdown } from '../lib/format'
  import CopyCode from './CopyCode.svelte'

  type Field = { label: string; value: string }
  type Props = {
    title: string
    note: string
    fields: Field[]
    expiresAt: string
    onExpire: () => void
    onDismiss: () => void
    dismissLabel?: string
    onCancel?: () => void
    cancelLabel?: string
  }
  const { title, note, fields, expiresAt, onExpire, onDismiss, dismissLabel = 'Done', onCancel, cancelLabel = 'Cancel' }: Props = $props()

  let now = $state(Date.now())
  let timer: number | undefined
  const remaining = $derived(new Date(expiresAt).getTime() - now)

  onMount(() => {
    timer = window.setInterval(() => {
      now = Date.now()
      if (new Date(expiresAt).getTime() - now <= 0) onExpire()
    }, 1000)
  })

  onDestroy(() => window.clearInterval(timer))
</script>

<section class="secret" aria-labelledby="secret-title">
  <h2 id="secret-title">{title}</h2>
  <p>{note}</p>
  {#each fields as field (field.label)}
    <CopyCode label={field.label} value={field.value} />
  {/each}
  <div class="secret-foot">
    <span>Expires in {formatCountdown(remaining)}</span>
    <div>
      {#if onCancel}<button type="button" class="danger" onclick={onCancel}>{cancelLabel}</button>{/if}
      <button type="button" onclick={onDismiss}>{dismissLabel}</button>
    </div>
  </div>
</section>
