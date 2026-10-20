/*
 * Pages that share one sidebar item are tabs of it: each keeps its own
 * address, and a strip above the page moves between them. A tab can need a
 * permission; a strip with one tab left is not shown.
 */

export interface SectionTab {
  to: string
  label: string
  /** Shown when the person holds any of these. */
  anyOf?: string[]
}

export interface Section {
  label: string
  tabs: SectionTab[]
}

/**
 * The section the page at pathname is a tab of, with the tabs the person
 * may open; null when the page is not a tab, or only one tab is left.
 */
export function sectionFor(
  sections: Section[],
  pathname: string,
  can: (permission: string) => boolean,
): Section | null {
  const section = sections.find((s) => s.tabs.some((t) => t.to === pathname))
  if (!section) return null
  const tabs = section.tabs.filter((t) => !t.anyOf || t.anyOf.some(can) || t.to === pathname)
  return tabs.length > 1 ? { label: section.label, tabs } : null
}

/** Whether a sidebar item is the current place: its own pages, and what is under them. */
export function itemActive(match: string[], pathname: string): boolean {
  return match.some((m) => pathname === m || pathname.startsWith(m + '/'))
}
