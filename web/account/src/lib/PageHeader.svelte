<script lang="ts">
  export let title: string
  export let fact = ''
  export let detail = ''
</script>

<div class="page-header">
  <h1 id="page-title" tabindex="-1">{title}</h1>
  {#if fact || detail}
    <p class="page-fact">
      {#if fact}<span class="page-fact-main">{fact}</span>{/if}
      {#if detail}<span class="page-fact-detail">{detail}</span>{/if}
    </p>
  {/if}
</div>
