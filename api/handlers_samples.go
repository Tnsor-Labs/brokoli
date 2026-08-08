package api

import (
	"embed"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// samplesDataFS holds the sample data the built-in pipeline templates
// (ui/src/pages/Pipelines.svelte's "Hello World", "API Fetch", "Join +
// Aggregate", and "Data Quality" starters) fetch from
// GET /api/samples/data/{file}. This endpoint is exempt from auth (see
// AuthMiddleware and WorkspaceMiddleware's "/api/samples/" skip) since
// it's static, non-sensitive demo data referenced before a user has
// created an account.
//
// JSON, not CSV: every template consumes this through a source_api node,
// and source_api's fetcher (pkg/fetchers/rest_fetcher.go) only ever
// parses response bodies as JSON (pkg/common.ParseJSONData) — there is no
// CSV-response support anywhere in that path. Serving CSV here would 404
// on the URL and then fail to parse even once the URL resolved.
//
//go:embed samples_data/*.json
var samplesDataFS embed.FS

// samplesDataHandler serves one of the embedded sample files by name.
// Only exact matches against the embedded file set are served — the
// embed.FS is closed over a fixed set of files at build time, so an
// unrecognized name can't reach anything outside samples_data/ (no
// path traversal is possible via embed.FS regardless).
func samplesDataHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		file := chi.URLParam(r, "file")
		data, err := samplesDataFS.ReadFile("samples_data/" + file)
		if err != nil {
			http.Error(w, "sample not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(data) // #nosec G705 -- data is never attacker-influenced: it's one of a fixed, embedded JSON set selected by exact-match lookup against embed.FS above. The "file" param only picks which embedded file to serve (or fails closed to 404, see the ReadFile error check above); it never reaches the response body or influences its content.
	}
}
