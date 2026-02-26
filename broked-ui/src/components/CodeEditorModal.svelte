<script lang="ts">
  import { createEventDispatcher, onMount, afterUpdate } from "svelte";
  import Prism from "prismjs";
  import "prismjs/components/prism-python";

  export let script: string = "";
  export let visible: boolean = false;

  const dispatch = createEventDispatcher();

  let localScript = script;
  let lineCount = 1;
  let textareaEl: HTMLTextAreaElement;
  let highlightEl: HTMLPreElement;
  let editorWrap: HTMLDivElement;
  let highlighted = "";

  $: if (visible) {
    localScript = script;
    updateHighlight();
  }
  $: lineCount = Math.max((localScript || "").split("\n").length, 1);

  function updateHighlight() {
    const code = localScript || "";
    // Prism highlight, then append a trailing newline so the pre matches textarea height
    highlighted = Prism.highlight(code, Prism.languages.python, "python") + "\n";
  }

  $: if (localScript !== undefined) updateHighlight();

  function syncScroll() {
    if (highlightEl && textareaEl) {
      highlightEl.scrollTop = textareaEl.scrollTop;
      highlightEl.scrollLeft = textareaEl.scrollLeft;
    }
    // Sync line numbers too
    const lnEl = editorWrap?.querySelector(".line-numbers pre") as HTMLElement;
    if (lnEl && textareaEl) {
      lnEl.style.transform = `translateY(-${textareaEl.scrollTop}px)`;
    }
  }

  function save() {
    dispatch("save", localScript);
    visible = false;
  }

  function close() {
    visible = false;
  }

  function handleKeydown(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && e.key === "s") {
      e.preventDefault();
      save();
    }
    if (e.key === "Escape") {
      close();
    }
    if (e.key === "Tab") {
      e.preventDefault();
      const target = e.target as HTMLTextAreaElement;
      const start = target.selectionStart;
      const end = target.selectionEnd;
      localScript = localScript.substring(0, start) + "    " + localScript.substring(end);
      requestAnimationFrame(() => {
        target.selectionStart = target.selectionEnd = start + 4;
      });
    }
  }

  function getLineNumbers(): string {
    return Array.from({ length: lineCount }, (_, i) => i + 1).join("\n");
  }

  $: lineNumbers = getLineNumbers();
</script>

{#if visible}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="modal-overlay" on:keydown={handleKeydown}>
    <div class="modal">
      <div class="modal-header">
        <div class="header-left">
          <span class="modal-title">Python Script Editor</span>
          <span class="modal-hint">Tab = indent | Ctrl+S = save | Esc = close</span>
        </div>
        <div class="header-right">
          <span class="line-info">{lineCount} lines</span>
          <button class="btn-cancel" on:click={close}>Cancel</button>
          <button class="btn-save" on:click={save}>Save</button>
        </div>
      </div>
      <div class="editor-container" bind:this={editorWrap}>
        <div class="line-numbers">
          <pre>{lineNumbers}</pre>
        </div>
        <div class="code-area">
          <!-- Highlighted layer (behind) -->
          <pre
            class="highlight-layer"
            bind:this={highlightEl}
            aria-hidden="true"
          ><code class="language-python">{@html highlighted}</code></pre>

          <!-- Textarea layer (on top, transparent text) -->
          <textarea
            class="code-textarea"
            bind:this={textareaEl}
            bind:value={localScript}
            on:scroll={syncScroll}
            on:input={syncScroll}
            spellcheck="false"
            autocomplete="off"
            autocorrect="off"
            autocapitalize="off"
            placeholder="# Your Python script here"
          ></textarea>
        </div>
      </div>
      <div class="modal-footer">
        <div class="footer-ref">
          <span class="ref-title">Available:</span>
          <code>columns</code> <code>rows</code> <code>config</code> <code>params</code>
          <span class="ref-sep">|</span>
          <span class="ref-title">Output:</span>
          <code>output_data = {`{"columns": [...], "rows": [...]}`}</code>
          <span class="ref-sep">|</span>
          <span class="ref-title">Logging:</span>
          <code>print("msg", file=sys.stderr)</code>
        </div>
      </div>
    </div>
  </div>
{/if}

<style>
  .modal-overlay {
    position: fixed; inset: 0;
    background: var(--bg-overlay);
    z-index: 1000;
    display: flex; align-items: center; justify-content: center;
    padding: 24px;
  }

  .modal {
    width: 100%; height: 100%;
    max-width: 1200px; max-height: 90vh;
    background: var(--bg-code);
    border: 1px solid var(--border-subtle);
    border-radius: 12px;
    display: flex; flex-direction: column; overflow: hidden;
  }

  .modal-header {
    display: flex; justify-content: space-between; align-items: center;
    padding: 12px 20px;
    background: var(--bg-sidebar);
    border-bottom: 1px solid var(--border-sidebar);
    flex-shrink: 0;
  }
  .header-left { display: flex; align-items: center; gap: 12px; }
  .modal-title { font-size: 13px; font-weight: 600; color: var(--text-primary); }
  .modal-hint { font-family: var(--font-mono); font-size: 10px; color: var(--text-dim); }
  .header-right { display: flex; align-items: center; gap: 8px; }
  .line-info { font-family: var(--font-mono); font-size: 10px; color: var(--text-dim); margin-right: 8px; }

  .btn-cancel {
    padding: 5px 14px; border-radius: 6px; font-size: 12px; font-weight: 500;
    background: var(--bg-secondary); border: 1px solid var(--border); color: var(--text-secondary); transition: all 150ms ease;
  }
  .btn-cancel:hover { background: var(--bg-tertiary); color: var(--text-primary); }
  .btn-save {
    padding: 5px 14px; border-radius: 6px; font-size: 12px; font-weight: 500;
    background: var(--code-accent); border: 1px solid var(--code-accent); color: var(--bg-code); transition: all 150ms ease;
  }
  .btn-save:hover { opacity: 0.9; }

  .editor-container {
    flex: 1; display: flex; overflow: hidden;
  }

  .line-numbers {
    width: 48px; flex-shrink: 0;
    background: var(--bg-code-line);
    border-right: 1px solid var(--border-sidebar);
    overflow: hidden; padding: 14px 0;
  }
  .line-numbers pre {
    font-family: var(--font-mono); font-size: 13px; line-height: 1.6;
    color: var(--text-ghost); text-align: right; padding-right: 12px;
    margin: 0; user-select: none; will-change: transform;
  }

  .code-area { flex: 1; position: relative; overflow: hidden; }

  .highlight-layer,
  .code-textarea {
    position: absolute; inset: 0;
    font-family: var(--font-mono); font-size: 13px; line-height: 1.6;
    padding: 14px 16px; margin: 0; border: none;
    white-space: pre; tab-size: 4; overflow: auto; word-wrap: normal;
  }

  .highlight-layer {
    color: var(--text-primary);
    background: var(--bg-code);
    pointer-events: none; z-index: 1;
  }
  .highlight-layer code {
    font-family: inherit; font-size: inherit; line-height: inherit;
    background: none; padding: 0;
  }

  .code-textarea {
    color: transparent;
    caret-color: var(--code-caret);
    background: transparent;
    z-index: 2; resize: none; outline: none;
    -webkit-text-fill-color: transparent;
  }
  .code-textarea::placeholder {
    -webkit-text-fill-color: var(--text-ghost);
    color: var(--text-ghost);
  }
  .code-textarea::selection {
    background: var(--code-selection);
    -webkit-text-fill-color: transparent;
  }

  /* Prism tokens are styled in global.css using CSS variables */

  .modal-footer {
    padding: 8px 20px; background: var(--bg-sidebar); border-top: 1px solid var(--border-sidebar); flex-shrink: 0;
  }
  .footer-ref {
    display: flex; align-items: center; gap: 6px; flex-wrap: wrap; font-size: 10px; color: var(--text-dim);
  }
  .ref-title { font-weight: 600; color: var(--text-muted); }
  .ref-sep { color: var(--text-ghost); margin: 0 2px; }
  .footer-ref code {
    font-family: var(--font-mono); font-size: 9.5px;
    background: var(--bg-secondary); color: var(--code-accent); padding: 1px 5px; border-radius: 3px;
  }
</style>
