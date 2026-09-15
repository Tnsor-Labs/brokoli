/*
 * Pure logic behind the search palette and the keyboard shortcuts, kept
 * free of React so it can be tested directly.
 */

/** A page an application offers in its navigation. `key` is the second key of its "g" shortcut. */
export interface ShellPage {
  to: string
  label: string
  group: string
  key?: string
}

/** An extra palette action an application offers, such as "Invite teammates". It opens a page. */
export interface ShellAction {
  id: string
  label: string
  to: string
  keywords?: string[]
}

/**
 * How well `query` matches `text`: 0 for no match, higher is better.
 * Whole match, then prefix, then the start of a word, then anywhere.
 */
export function score(query: string, text: string | undefined | null): number {
  const q = query.trim().toLowerCase()
  const t = (text ?? '').toLowerCase()
  if (!q || !t) return 0
  if (t === q) return 100
  if (t.startsWith(q)) return 80
  const at = t.indexOf(q)
  if (at < 0) return 0
  return /[\s._\-/:]/.test(t[at - 1]) ? 60 : 40
}

export interface Rankable {
  label: string
  /** Secondary text searched with a lower weight (description, tags, type). */
  keywords?: (string | undefined | null)[]
}

/** Items matching `query`, best first; ties keep their original order. An empty query keeps everything. */
export function rank<T extends Rankable>(query: string, items: T[]): T[] {
  if (!query.trim()) return items
  return items
    .map((item, index) => {
      const primary = score(query, item.label)
      const secondary = Math.max(0, ...(item.keywords ?? []).map((k) => score(query, k)))
      return { item, index, value: Math.max(primary, secondary / 2) }
    })
    .filter((r) => r.value > 0)
    .sort((a, b) => b.value - a.value || a.index - b.index)
    .map((r) => r.item)
}

/** Splits `label` around the first case-insensitive occurrence of `query`, for highlighting. */
export function splitMatch(label: string, query: string): [string, string, string] | null {
  const q = query.trim()
  if (!q) return null
  const at = label.toLowerCase().indexOf(q.toLowerCase())
  if (at < 0) return null
  return [label.slice(0, at), label.slice(at, at + q.length), label.slice(at + q.length)]
}

/** Where the "g" shortcut followed by `key` goes, if anywhere. */
export function goTarget(key: string, pages: ShellPage[]): ShellPage | undefined {
  const k = key.toLowerCase()
  return pages.find((p) => p.key === k)
}

/** Keys typed into a field, an editor or while a dialog is open belong to that element, not to the shortcuts. */
export function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false
  if (target.closest('input, textarea, select, [contenteditable=""], [contenteditable="true"]')) return true
  return false
}

/** Moves a highlighted index through `length` options, wrapping at both ends. */
export function cycle(index: number, delta: number, length: number): number {
  if (length <= 0) return -1
  return (((index + delta) % length) + length) % length
}
