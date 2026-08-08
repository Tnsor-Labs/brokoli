package api

import (
	"net/http"

	"github.com/Tnsor-Labs/brokoli/pkg/templates"
)

// templatesHandler serves the built-in pipeline templates
// (pkg/templates.Builtin) offered when creating a new pipeline. See that
// package's doc comment for why these moved out of the frontend.
func templatesHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, templates.Builtin)
}
