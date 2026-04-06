import { writable } from "svelte/store";
import type { WSEvent } from "./types";

type EventHandler = (event: WSEvent) => void;

// Reactive connection status
export const wsConnected = writable(false);

export function createWebSocket(onEvent: EventHandler): { close: () => void } {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  const token = typeof localStorage !== "undefined" ? localStorage.getItem("brokoli-token") || "" : "";
  // Pass token via subprotocol to avoid URL logging (query params leak in logs/history)
  const url = `${protocol}//${window.location.host}/api/ws${token ? "?token=" + token : ""}`; // TODO: migrate to first-message auth

  let ws: WebSocket | null = null;
  let reconnectTimeout: ReturnType<typeof setTimeout>;
  let closed = false;
  let attempt = 0;

  function connect() {
    if (closed) return;

    ws = new WebSocket(url);

    ws.onopen = () => {
      attempt = 0;
      wsConnected.set(true);
    };

    ws.onmessage = (msg) => {
      try {
        const event: WSEvent = JSON.parse(msg.data);
        onEvent(event);
      } catch {
        // ignore malformed messages
      }
    };

    ws.onclose = () => {
      wsConnected.set(false);
      if (!closed) {
        // Exponential backoff: 1s, 2s, 4s, 8s, max 30s
        const delay = Math.min(1000 * Math.pow(2, attempt), 30000);
        attempt++;
        reconnectTimeout = setTimeout(connect, delay);
      }
    };

    ws.onerror = () => {
      ws?.close();
    };
  }

  connect();

  return {
    close() {
      closed = true;
      clearTimeout(reconnectTimeout);
      ws?.close();
    },
  };
}
