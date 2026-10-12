<script lang="ts">
  type Props = { label: string; hint: string; checked: boolean; disabled?: boolean; onchange: (checked: boolean) => void }
  const { label, hint, checked, disabled = false, onchange }: Props = $props()
</script>

<label class="check-row">
  <input type="checkbox" {checked} {disabled} onchange={(event) => onchange(event.currentTarget.checked)} />
  <span><strong>{label}</strong><small>{hint}</small></span>
</label>
