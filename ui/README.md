# Brokoli UI

Svelte 5 frontend for the Brokoli data orchestration platform.

## Development

```bash
npm install
npm run dev
```

Opens at `http://localhost:5173` with API proxy to `http://localhost:8080`.

## Build

```bash
npm run build
```

Output goes to `../web/dist/` and is embedded into the Go binary via `go:embed`.

## Structure

```
src/
├── pages/           Route pages (Dashboard, Pipelines, Editor, Runs, etc.)
├── components/      Reusable components (Canvas, NodeCard, StatusBadge, etc.)
├── lib/             Stores, auth, types, WebSocket, utilities
└── styles/          Global CSS with dark/light theme variables
```

## Enterprise

The enterprise edition overlays additional pages and components on top of this UI. See `brokoli-enterprise/ee/ui-overlay/` for details. The enterprise build merges both source trees before compiling.
