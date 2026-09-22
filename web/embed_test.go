package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// The embedded UI must be internally consistent: whatever index.html the
// binary serves, the assets that page references have to be embedded
// beside it. They were not. web/dist/index.html was a real built page
// committed to the repo while .gitignore excluded web/dist/assets/, so
// the published module carried a page asking for two files it did not
// contain, and every source build served it under a log line announcing
// success.
func TestEmbeddedIndexReferencesOnlyEmbeddedAssets(t *testing.T) {
	index, err := fs.ReadFile(Dist, "dist/index.html")
	if err != nil {
		// No index.html is a legitimate state: a checkout that has not
		// built the UI. Built() must notice, which the tests below cover.
		t.Skipf("no dist/index.html embedded: %v", err)
	}

	refs := regexp.MustCompile(`(?:src|href)="(/[^"]+)"`).FindAllStringSubmatch(string(index), -1)
	if len(refs) == 0 {
		t.Fatal("dist/index.html references no assets at all; a built bundle always does")
	}
	for _, ref := range refs {
		path := strings.TrimPrefix(ref[1], "/")
		if _, err := fs.Stat(Dist, "dist/"+path); err != nil {
			t.Errorf("index.html references %q, which is not in the embed: %v", ref[1], err)
		}
	}
}

// A binary compiled without a bundle must still serve something honest.
// The placeholder is read directly rather than through Built(), so this
// runs in every checkout: reaching it only when no bundle exists would
// mean it never ran in CI, where build-ui.sh runs first.
func TestUnbuiltPlaceholderIsSelfContained(t *testing.T) {
	page, err := fs.ReadFile(unbuilt, "unbuilt/index.html")
	if err != nil {
		t.Fatalf("the placeholder page is not embedded: %v", err)
	}
	if refs := regexp.MustCompile(`(?:src|href)="/`).FindAllString(string(page), -1); len(refs) > 0 {
		t.Errorf("the placeholder references %d external asset(s); it must be self-contained, because nothing is embedded beside it", len(refs))
	}
	if !strings.Contains(string(page), "build-ui.sh") {
		t.Error("the placeholder does not tell the reader how to build the UI")
	}
}

// Whichever state Built() reports, it hands back a filesystem with an
// index.html in it, because cmd/serve.go mounts it directly.
func TestBuiltAlwaysServesAnIndex(t *testing.T) {
	uiFS, _ := Built()
	if uiFS == nil {
		t.Fatal("Built() returned no filesystem at all")
	}
	if _, err := fs.ReadFile(uiFS, "index.html"); err != nil {
		t.Fatalf("the served filesystem has no index.html: %v", err)
	}
}

// Built() must not report the placeholder as a real bundle: that is the
// difference between "Serving embedded UI" and a warning, and reporting
// it wrongly is how the original defect stayed invisible in the logs.
func TestBuiltReportsPlaceholderHonestly(t *testing.T) {
	_, built := Built()
	_, err := fs.Stat(Dist, "dist/index.html")
	if err != nil && built {
		t.Error("Built() claimed a real bundle while dist/index.html does not exist")
	}
	if err == nil && !built {
		t.Error("Built() claimed a placeholder while dist/index.html exists")
	}
}
