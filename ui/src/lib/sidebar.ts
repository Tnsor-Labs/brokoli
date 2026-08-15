import { writable } from "svelte/store";

// Sidebar layout preferences, persisted like theme.ts persists the theme.
// One JSON blob under a single key: { collapsed: boolean, groups: { [id]: boolean } }.
// `groups` maps a section id to its EXPANDED state; missing ids default to
// expanded, so new sections added later start open without a migration.
const STORAGE_KEY = "brokoli-sidebar";

interface SidebarPrefs {
  collapsed: boolean;
  groups: Record<string, boolean>;
}

function load(): SidebarPrefs {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw) {
      const parsed = JSON.parse(raw);
      return {
        collapsed: parsed.collapsed === true,
        groups: typeof parsed.groups === "object" && parsed.groups !== null ? parsed.groups : {},
      };
    }
  } catch {
    // Corrupt or unavailable storage falls back to defaults.
  }
  return { collapsed: false, groups: {} };
}

function persist(prefs: SidebarPrefs) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(prefs));
  } catch {
    // Storage unavailable (private mode, quota) — preference lives for the session only.
  }
}

const initial = load();

export const sidebarCollapsed = writable<boolean>(initial.collapsed);
export const sidebarGroups = writable<Record<string, boolean>>(initial.groups);

function applyAttribute(collapsed: boolean) {
  document.documentElement.setAttribute("data-sidebar", collapsed ? "collapsed" : "expanded");
}

/** Apply the persisted state on boot. Call next to initTheme() in App.svelte. */
export function initSidebar() {
  applyAttribute(initial.collapsed);
}

export function toggleSidebar() {
  sidebarCollapsed.update((collapsed) => {
    const next = !collapsed;
    applyAttribute(next);
    persistCurrent({ collapsed: next });
    return next;
  });
}

export function toggleGroup(id: string) {
  sidebarGroups.update((groups) => {
    const next = { ...groups, [id]: groups[id] === false };
    persistCurrent({ groups: next });
    return next;
  });
}

// Merge-and-write helper so toggling one preference never clobbers the other.
let current: SidebarPrefs = initial;
function persistCurrent(patch: Partial<SidebarPrefs>) {
  current = { ...current, ...patch };
  persist(current);
}
