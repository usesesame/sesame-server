<script lang="ts">
  import { onDestroy } from 'svelte'

  type Props = { label: string; value: string }
  const { label, value }: Props = $props()

  let copied = $state(false)
  let copyFailed = $state(false)
  let timer: number | undefined

  onDestroy(() => window.clearTimeout(timer))

  async function copy() {
    copyFailed = false
    try {
      await navigator.clipboard.writeText(value)
      copied = true
      window.clearTimeout(timer)
      timer = window.setTimeout(() => { copied = false }, 2000)
    } catch {
      copyFailed = true
    }
  }
</script>

<div class="secret-field" role="group" aria-label={label}>
  <span>{label}</span>
  <div class="secret-value">
    <code>{value}</code>
    <button type="button" onclick={copy}>{copied ? 'Copied' : 'Copy'}</button>
  </div>
  {#if copyFailed}<p class="message error" role="alert">Copying was blocked. Select the text and copy it by hand.</p>{/if}
</div>
