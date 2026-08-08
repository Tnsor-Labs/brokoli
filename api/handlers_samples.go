package api

import (
	"embed"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// samplesDataFS holds the sample CSVs the built-in pipeline templates
// (ui/src/pages/Pipelines.svelte's "Hello World", "API Fetch", "Join +
// Aggregate", and "Data Quality" starters) fetch from
// GET /api/samples/data/{file}. This endpoint is exempt from auth (see
// AuthMiddleware and WorkspaceMiddleware's "/api/samples/" skip) since
// it's static, non-sensitive demo data referenced before a user has
// created an account.
//
//go:embed samples_data/*.csv
var samplesDataFS embed.FS

// samplesDataHandler serves one of the embedded sample CSVs by name.
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
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(data)
	}
}
