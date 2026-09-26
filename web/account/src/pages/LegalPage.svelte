<script lang="ts">
  import { LEGAL_UPDATED, LEGAL_VERSION } from '../lib/legal'

  export let document: 'terms' | 'privacy' | 'security'
</script>

<section class="compact-page-hero legal-page">
  {#if document === 'terms'}
    <h1>Terms of use</h1>
    <p class="intro">This Sesame account service is operated by the person or organization running this instance, not by the Sesame project.</p>
    <h2>Your account</h2>
    <p>You need an invitation from the operator to create an account. Keep your password, passkeys, and recovery codes private.</p>
    <p>The operator may suspend or remove accounts that abuse the service or break the law.</p>
    <h2>The vault</h2>
    <p>Sesame never sends your vault or master password to this service. The operator cannot read or recover them.</p>
  {:else if document === 'privacy'}
    <h1>Privacy notice</h1>
    <p class="intro">This notice describes the account data this instance stores. The operator running this instance controls that data.</p>
    <h2>What is stored</h2>
    <ul>
      <li>Your email address and a password hash.</li>
      <li>Passkey public keys and session records.</li>
      <li>Email verification state and account notices.</li>
      <li>Support messages you send through the portal.</li>
      <li>Administrator audit records for staff actions.</li>
    </ul>
    <h2>What is never stored</h2>
    <p>Vault contents, your master password, recovery kits, and browser-fill secrets never reach this service.</p>
    <h2>Your choices</h2>
    <p>Use the <a href="/support">support page</a> to ask the operator to show, correct, or delete your account data.</p>
  {:else}
    <h1>Security</h1>
    <p class="intro">This account service stores only account metadata. It cannot decrypt a vault.</p>
    <h2>Report a problem with this service</h2>
    <p>Contact the operator through the <a href="/support">support page</a>.</p>
    <h2>Report a vulnerability in Sesame</h2>
    <p>Report software vulnerabilities through the project's security policy at <a href="https://github.com/usesesame/sesame-server/security/policy" rel="noreferrer">github.com/usesesame/sesame-server</a>.</p>
  {/if}
  <p class="legal-version">Version {LEGAL_VERSION}, updated {LEGAL_UPDATED}.</p>
</section>

<style>
  .legal-page h2 { margin-top: 28px; }
  .legal-page ul { max-width: 68ch; }
  .legal-version { margin-top: 32px; color: var(--text-muted); font-size: 13px; }
</style>
