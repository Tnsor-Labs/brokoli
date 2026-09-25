package api

import (
	"context"
	"net/http"
)

func withOrgForTest(r *http.Request, org string) context.Context {
	return context.WithValue(r.Context(), OrgIDContextKey{}, org)
}
