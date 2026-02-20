import { writable } from "svelte/store";

export type Theme = "dark" | "light";

function getInitialTheme(): Theme {
  if (typeof localStorage !== "undefined") {
    const stored = localStorage.getItem("broked-theme");
    if (stored === "light" || stored === "dark") return stored;
  }
  return "dark";
}

export const theme = writable<Theme>(getInitialTheme());

export function toggleTheme() {
  theme.update((t) => {
    const next = t === "dark" ? "light" : "dark";
    localStorage.setItem("broked-theme", next);
    document.documentElement.setAttribute("data-theme", next);
    return next;
  });
}

// Apply on load
export function initTheme() {
  const t = getInitialTheme();
  document.documentElement.setAttribute("data-theme", t);
}
