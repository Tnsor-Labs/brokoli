<script lang="ts">
  import { onMount } from "svelte";
  import { login, createFirstUser, needsSetup, authUser, setToken } from "../lib/auth";
  import { loadPermissions } from "../lib/auth";
  import { push } from "svelte-spa-router";

  let username = "";
  let password = "";
  let error = "";
  let loading = false;

  // Handle OAuth redirect: if the URL has ?token=xxx, store it and go to dashboard
  onMount(() => {
    const params = new URLSearchParams(window.location.search);
    const token = params.get("token");
    if (token) {
      setToken(token);
      // Clean URL and redirect
      window.history.replaceState({}, "", window.location.pathname + "#/");
      window.location.reload();
    }
    const oauthError = params.get("error");
    if (oauthError) {
      error = oauthError;
      window.history.replaceState({}, "", window.location.pathname + "#/login");
    }
  });

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
      await loadPermissions();
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
        <path d="M16 19v9" stroke="#4ade80" stroke-width="2.5" stroke-linecap="round"/>
        <path d="M14 22l2-3 2 3" stroke="#4ade80" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" opacity="0.5"/>
        <circle cx="16" cy="11" r="4.5" fill="#0d9488"/>
        <circle cx="9" cy="7" r="3.5" fill="#16a34a"/>
        <circle cx="23" cy="7" r="3.5" fill="#16a34a"/>
        <circle cx="6" cy="2" r="2.5" fill="#22c55e"/>
        <circle cx="16" cy="2" r="3" fill="#22c55e"/>
        <circle cx="26" cy="2" r="2.5" fill="#22c55e"/>
        <line x1="9" y1="7" x2="16" y2="11" stroke="#0d9488" stroke-width="1" opacity="0.4"/>
        <line x1="23" y1="7" x2="16" y2="11" stroke="#0d9488" stroke-width="1" opacity="0.4"/>
        <line x1="6" y1="2" x2="9" y2="7" stroke="#16a34a" stroke-width="1" opacity="0.3"/>
        <line x1="16" y1="2" x2="9" y2="7" stroke="#16a34a" stroke-width="1" opacity="0.3"/>
        <line x1="16" y1="2" x2="23" y2="7" stroke="#16a34a" stroke-width="1" opacity="0.3"/>
        <line x1="26" y1="2" x2="23" y2="7" stroke="#16a34a" stroke-width="1" opacity="0.3"/>
      </svg>
      <h1>Brokoli</h1>
    </div>

    {#if $needsSetup}
      <p class="setup-msg">Create your admin account to get started.</p>
    {:else}
      <div class="oauth-buttons">
        <a href="/api/auth/oauth/github?redirect_uri={encodeURIComponent(window.location.origin)}" class="oauth-btn">
          <svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor"><path d="M12 0C5.37 0 0 5.37 0 12c0 5.31 3.435 9.795 8.205 11.385.6.105.825-.255.825-.57 0-.285-.015-1.23-.015-2.235-3.015.555-3.795-.735-4.035-1.41-.135-.345-.72-1.41-1.23-1.695-.42-.225-1.02-.78-.015-.795.945-.015 1.62.87 1.845 1.23 1.08 1.815 2.805 1.305 3.495.99.105-.78.42-1.305.765-1.605-2.67-.3-5.46-1.335-5.46-5.925 0-1.305.465-2.385 1.23-3.225-.12-.3-.54-1.53.12-3.18 0 0 1.005-.315 3.3 1.23.96-.27 1.98-.405 3-.405s2.04.135 3 .405c2.295-1.56 3.3-1.23 3.3-1.23.66 1.65.24 2.88.12 3.18.765.84 1.23 1.905 1.23 3.225 0 4.605-2.805 5.625-5.475 5.925.435.375.81 1.095.81 2.22 0 1.605-.015 2.895-.015 3.3 0 .315.225.69.825.57A12.02 12.02 0 0024 12c0-6.63-5.37-12-12-12z"/></svg>
          Continue with GitHub
        </a>
        <a href="/api/auth/oauth/google?redirect_uri={encodeURIComponent(window.location.origin)}" class="oauth-btn">
          <svg width="18" height="18" viewBox="0 0 24 24"><path d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92a5.06 5.06 0 01-2.2 3.32v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.1z" fill="#4285F4"/><path d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z" fill="#34A853"/><path d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18A11.96 11.96 0 001 12c0 1.94.46 3.77 1.18 5.41l3.66-2.84.01-.48z" fill="#FBBC05"/><path d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z" fill="#EA4335"/></svg>
          Continue with Google
        </a>
        <a href="/api/auth/oauth/keycloak?redirect_uri={encodeURIComponent(window.location.origin)}" class="oauth-btn">
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none"><path d="M3 3h18v18H3V3z" fill="#4D4D4D"/><path d="M7 7h4l2 3-2 3H7l2-3-2-3z" fill="#fff"/><path d="M13 7h4l-2 3 2 3h-4l-2-3 2-3z" fill="#00B8E3"/></svg>
          Continue with SSO
        </a>
      </div>
      <div class="oauth-divider">
        <span>or</span>
      </div>
    {/if}

    <!-- svelte-ignore a11y_no_static_element_interactions -->
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

      {#if !$needsSetup}
        <p class="signup-link">Don't have an account? <a href="#/signup">Sign up</a></p>
      {/if}
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

  .signup-link {
    font-size: 13px;
    color: var(--text-muted);
    text-align: center;
    margin-top: 4px;
  }
  .signup-link a {
    color: var(--accent);
    font-weight: 500;
  }
  .signup-link a:hover {
    color: var(--accent-hover);
  }

  /* OAuth buttons */
  .oauth-buttons {
    display: flex;
    flex-direction: column;
    gap: 10px;
    margin-bottom: 0;
  }
  .oauth-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 10px;
    padding: 10px 16px;
    border-radius: var(--radius-md);
    font-size: 14px;
    font-weight: 500;
    color: var(--text-primary);
    background: transparent;
    border: 1px solid var(--border);
    text-decoration: none;
    transition: background 150ms ease, border-color 150ms ease;
    cursor: pointer;
  }
  .oauth-btn:hover {
    background: var(--bg-tertiary);
    border-color: var(--border-hover);
    color: var(--text-primary);
  }
  .oauth-btn svg {
    flex-shrink: 0;
  }

  .oauth-divider {
    display: flex;
    align-items: center;
    gap: 12px;
    margin: 8px 0;
  }
  .oauth-divider::before,
  .oauth-divider::after {
    content: "";
    flex: 1;
    height: 1px;
    background: var(--border);
  }
  .oauth-divider span {
    font-size: 12px;
    color: var(--text-dim);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
</style>
