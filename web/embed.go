package web

import "embed"

// Dist contains the built frontend assets.
// When building without the UI (dev mode), dist/ may be empty.
//
//go:embed all:dist
var Dist embed.FS
