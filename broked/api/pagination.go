package api

import (
	"net/http"
	"strconv"

	"github.com/hc12r/broked/store"
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
