<script lang="ts">
  export let id: string
  export let label: string
  export let value = ''
  export let autocomplete: 'new-password' | 'current-password'
  export let describedby: string | undefined = undefined
  export let minlength: number | undefined = undefined
  export let disabled = false
  export let revealed = false
  export let toggle = true
</script>

<div class="field">
  <div class="label-row">
    <label for={id}>{label}</label>
    <slot name="aside" />
  </div>
  <div class="password-field">
    <input {id} type={revealed ? 'text' : 'password'} bind:value {autocomplete} aria-describedby={describedby} {minlength} maxlength="1024" required {disabled} />
    {#if toggle}
      <button type="button" class="password-toggle" on:click={() => (revealed = !revealed)} {disabled} aria-label={revealed ? 'Hide password' : 'Show password'}>{revealed ? 'Hide' : 'Show'}</button>
    {/if}
  </div>
  <slot name="note" />
</div>
