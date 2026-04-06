<script lang="ts">
  import type { RunStatus } from "../lib/types";

  export let status: RunStatus;
  export let size: "sm" | "md" = "md";

  const labels: Record<RunStatus, string> = {
    pending: "Pending",
    running: "Running",
    success: "Success",
    failed: "Failed",
    cancelled: "Cancelled",
  };
</script>

<span class="badge {status} {size}">
  {#if status === "running"}
    <span class="dot pulse"></span>
  {:else}
    <span class="dot"></span>
  {/if}
  {labels[status]}
</span>

<style>
  .badge {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-family: var(--font-mono);
    font-size: 0.75rem;
    font-weight: 500;
    padding: 2px 8px;
    border-radius: var(--radius-sm);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .badge.md { font-size: 0.75rem; padding: 3px 10px; }
  .badge.sm { font-size: 0.625rem; padding: 1px 6px; }

  .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    flex-shrink: 0;
  }

  .pending { color: var(--pending); background: var(--pending-bg); }
  .pending .dot { background: var(--pending); }

  .running { color: var(--running); background: var(--running-bg); }
  .running .dot { background: var(--running); }
  .running .dot.pulse { animation: pulse-dot 1.5s ease-in-out infinite; }

  .success { color: var(--success); background: var(--success-bg); }
  .success .dot { background: var(--success); animation: success-pop 0.4s ease-out; }

  .failed { color: var(--failed); background: var(--failed-bg); }
  .failed .dot { background: var(--failed); }

  .cancelled { color: var(--text-muted); background: var(--pending-bg); }
  .cancelled .dot { background: var(--text-muted); }

  @keyframes pulse-dot {
    0%, 100% { opacity: 1; transform: scale(1); }
    50% { opacity: 0.4; transform: scale(0.8); }
  }
  @keyframes success-pop {
    0% { transform: scale(0.4); }
    60% { transform: scale(1.3); }
    100% { transform: scale(1); }
  }
</style>
