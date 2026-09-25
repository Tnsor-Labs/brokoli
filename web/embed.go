package web

import (
	"embed"
	"io/fs"
)

// Dist holds the UI bundle that build-ui.sh writes. In a checkout that
// has not run it, dist/ contains only .gitkeep, which is there so this
// embed still compiles.
//
// Nothing under dist/ is committed. It used to be: a real built
// index.html was tracked while .gitignore excluded dist/assets/, so the
// published module carried a page referencing two asset files that were
// not in it. Anything built from source without build-ui.sh -- including
// the README's own "From Source" steps, which build the UI but never
// place it here -- served that page, logged "Serving embedded UI", and
// showed a blank screen.
//
//go:embed all:dist
var Dist embed.FS

//go:embed unbuilt/index.html
var unbuilt embed.FS

// Built reports the bundle to serve, and whether it is the real one. A
// binary compiled without a bundle serves the placeholder in unbuilt/,
// which says so, instead of a shell whose script and stylesheet 404.
func Built() (fs.FS, bool) {
	if dist, err := fs.Sub(Dist, "dist"); err == nil {
		if _, err := fs.Stat(dist, "index.html"); err == nil {
			return dist, true
		}
	}
	placeholder, err := fs.Sub(unbuilt, "unbuilt")
	if err != nil {
		return nil, false
	}
	return placeholder, false
}
