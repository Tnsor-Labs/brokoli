<script lang="ts">
  import { login, createFirstUser, needsSetup, authUser } from "../lib/auth";
  import { push } from "svelte-spa-router";

  let username = "";
  let password = "";
  let error = "";
  let loading = false;

  async function handleSubmit() {
    if (!username.trim() || !password.trim()) {
      error = "Username and password required";
      return;
    }
    loading = true;
    error = "";

    let err: string | null;
    if ($needsSetup) {
      err = await createFirstUser(username, password);
    } else {
      err = await login(username, password);
    }

    if (err) {
      error = err;
      loading = false;
    } else {
      push("/");
    }
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === "Enter") handleSubmit();
  }
</script>

<div class="login-page">
  <div class="login-card">
    <div class="login-logo">
      <svg width="36" height="36" viewBox="0 0 32 32" fill="none">
        <rect x="14.5" y="20" width="3" height="10" rx="1.5" fill="var(--accent)" opacity="0.6"/>
        <circle cx="16" cy="13" r="5" fill="var(--accent)"/>
        <circle cx="10" cy="10" r="4" fill="var(--accent)"/>
        <circle cx="22" cy="10" r="4" fill="var(--accent)"/>
        <circle cx="8" cy="5" r="3" fill="var(--accent)" opacity="0.85"/>
        <circle cx="16" cy="4" r="3.5" fill="var(--accent)" opacity="0.9"/>
        <circle cx="24" cy="5" r="3" fill="var(--accent)" opacity="0.85"/>
        <circle cx="13" cy="7" r="2.5" fill="var(--accent)" opacity="0.7"/>
        <circle cx="19" cy="7" r="2.5" fill="var(--accent)" opacity="0.7"/>
      </svg>
      <h1>Brokoli</h1>
    </div>

    {#if $needsSetup}
      <p class="setup-msg">Create your admin account to get started.</p>
    {/if}

    <div class="form" on:keydown={handleKeydown}>
      <div class="field">
        <label for="username">Username</label>
        <input id="username" type="text" bind:value={username} placeholder="admin" autocomplete="username" />
      </div>
      <div class="field">
        <label for="password">Password</label>
        <input id="password" type="password" bind:value={password} placeholder="Password" autocomplete="current-password" />
      </div>

      {#if error}
        <div class="error">{error}</div>
      {/if}

      <button class="btn-login" on:click={handleSubmit} disabled={loading}>
        {#if loading}
          Signing in...
        {:else if $needsSetup}
          Create Account
        {:else}
          Sign In
        {/if}
      </button>
    </div>
  </div>
</div>

<style>
  .login-page {
    min-height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--bg-primary);
    padding: var(--space-xl);
  }

  .login-card {
    width: 100%;
    max-width: 380px;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 40px 32px;
  }

  .login-logo {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 32px;
    justify-content: center;
  }
  .login-logo h1 {
    font-size: 24px;
    font-weight: 700;
    letter-spacing: -0.03em;
  }

  .setup-msg {
    font-size: 13px;
    color: var(--text-muted);
    text-align: center;
    margin-bottom: 24px;
  }

  .form {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .field label {
    font-size: 12px;
    font-weight: 500;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .field input {
    padding: 10px 12px;
    font-size: 14px;
  }

  .error {
    font-size: 13px;
    color: var(--failed);
    background: var(--failed-bg);
    border: 1px solid rgba(239, 68, 68, 0.2);
    padding: 8px 12px;
    border-radius: var(--radius-md);
  }

  .btn-login {
    padding: 10px;
    border-radius: var(--radius-md);
    font-size: 14px;
    font-weight: 600;
    background: var(--accent);
    color: white;
    transition: background 150ms ease;
    margin-top: 8px;
  }
  .btn-login:hover { opacity: 0.9; }
  .btn-login:disabled { opacity: 0.6; cursor: wait; }
</style>
