<script lang="ts">
  import { updateNotice } from '../lib/updates.svelte'
</script>

{#if updateNotice.available && !updateNotice.dismissed}
  <div class="banner" role="status">
    <p>An update is available. <a href="#/updates">View update</a></p>
    <button type="button" class="quiet" onclick={() => { updateNotice.dismissed = true }}>Dismiss</button>
  </div>
{/if}
