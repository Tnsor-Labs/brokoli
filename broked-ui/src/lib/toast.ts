import { writable } from "svelte/store";

export interface Toast {
  id: number;
  type: "success" | "error" | "info" | "warning";
  message: string;
  duration: number;
}

let nextId = 0;

export const toasts = writable<Toast[]>([]);

export function toast(type: Toast["type"], message: string, duration = 4000) {
  const id = nextId++;
  toasts.update((t) => [...t, { id, type, message, duration }]);
  if (duration > 0) {
    setTimeout(() => dismiss(id), duration);
  }
}

export function dismiss(id: number) {
  toasts.update((t) => t.filter((x) => x.id !== id));
}

export const notify = {
  success: (msg: string) => toast("success", msg),
  error: (msg: string) => toast("error", msg, 6000),
  info: (msg: string) => toast("info", msg),
  warning: (msg: string) => toast("warning", msg, 5000),
};
