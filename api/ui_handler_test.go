package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestUIHandlerServesFilesAndTheAppButNotOtherBuildsAssets(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html>app")},
		"assets/index-new.js":  {Data: []byte("console.log(1)")},
		"assets/HomePage-n.js": {Data: []byte("export {}")},
	}
	h := uiHandler(ui)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	if rec := get("/assets/HomePage-n.js"); rec.Code != http.StatusOK || rec.Body.String() != "export {}" ||
		rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("an asset: %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
	}
	for _, path := range []string{"/", "/pipelines", "/anything/else"} {
		if rec := get(path); rec.Code != http.StatusOK || rec.Body.String() != "<!doctype html>app" {
			t.Fatalf("%s: %d %q", path, rec.Code, rec.Body.String())
		}
	}
	// An asset of an earlier build is missing, not the app.
	rec := get("/assets/HomePage-old.js")
	if rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("an old asset: %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
	}
}
