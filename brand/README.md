# Brokoli Visual Assets

This directory is the source of truth for Brokoli's reusable visual identity
assets. Product UI code should reference these assets instead of copying SVG
markup into individual pages.

## Identity v1.4

- `tokens.css` contains the brand, editor taxonomy, and runtime status channels
  from `brokoli-visual-identity-v1.4.html`.
- `icons/` contains one SVG per `bk-*` symbol plus `sprite.svg` for consumers
  that need a single symbol sheet.
- `icons/manifest.json` records the source document and extracted icon names.

The icon set uses stroke-only SVGs with consistent view boxes. Brand green is
reserved for product identity and primary actions. Node families and runtime
states use their own semantic channels so the interface does not turn every
state into the same green.
