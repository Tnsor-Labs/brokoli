import { writable } from "svelte/store";

/**
 * Actions the current page contributes to the command palette.
 *
 * There is one palette, the global search on Cmd+K, and it already knew
 * how to find pipelines, connections and pages. It did not know how to
 * *do* anything, so every editor action needed a button, and the
 * toolbar grew until it broke (#555).
 *
 * A page registers its actions on mount and drops them on destroy, so
 * the palette only ever offers what is actually available where you are
 * standing. A new feature registers a command; a button is optional.
 */
export interface Command {
  /** Stable within a page. Used to de-duplicate re-registration. */
  id: string;
  label: string;
  /** Shown right-aligned, e.g. "Ctrl+S". Purely informational. */
  hint?: string;
  run: () => void;
}

export const commands = writable<Command[]>([]);

/**
 * Replace the current page's commands, and return a function that clears
 * them again.
 *
 * Replace rather than append: a page that re-registers on every reactive
 * update would otherwise accumulate duplicates, and the palette would
 * fill with stale actions pointing at state that has moved on.
 */
export function registerCommands(list: Command[]): () => void {
  commands.set(list);
  return () => commands.set([]);
}
