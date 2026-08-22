package api

import (
	"net/http"
	"strconv"

	"github.com/Tnsor-Labs/brokoli/store"
)

// ParsePageParams extracts page and page_size from query string.
// Defaults: page=1, page_size=25. Max page_size=100.
func ParsePageParams(r *http.Request) store.PageParams {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	return store.NewPageParams(page, pageSize)
}

// PaginateSlice applies offset/limit to a total count and returns a PageResult.
// The caller is responsible for slicing the actual items before passing them in.
func PaginateSlice(items interface{}, total int, params store.PageParams) store.PageResult {
	return store.NewPageResult(items, total, params)
}

// CursorParam reads the keyset position from a request.
//
// Accepts "cursor" as well as "after" because the responses call the
// field "cursor": a client that feeds the response's own cursor back —
// the obvious thing to do — was silently ignored, got page one again
// with has_next still true, and looped forever hammering the server.
// "after" stays supported for existing callers.
func CursorParam(r *http.Request) string {
	q := r.URL.Query()
	if v := q.Get("after"); v != "" {
		return v
	}
	return q.Get("cursor")
}

// HasCursorPagination reports whether the caller asked for the keyset
// form, by either parameter name or by giving an explicit limit.
func HasCursorPagination(r *http.Request) bool {
	q := r.URL.Query()
	return q.Get("after") != "" || q.Get("cursor") != "" || q.Get("limit") != ""
}
