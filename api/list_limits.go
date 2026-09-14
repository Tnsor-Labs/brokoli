package api

import "strconv"

// A ceiling on the ad-hoc list limits.
//
// The paginated endpoints go through store.NewPageParams, which caps
// page_size at 100. The endpoints that take a bare `limit` did not: the
// value was parsed and handed to SQL, so `?limit=100000000` asked the
// database for everything and then serialised it. One request is enough
// to hold a connection open and grow the process by the size of the
// table.
//
// Deliberately generous rather than matching the 100 above: these lists
// are read by an inbox and a triage view that legitimately want more
// than a page, and the point is a ceiling, not a page size.
const (
	defaultListLimit = 100
	maxListLimit     = 1000
)

// boundedListLimit reads a `limit` parameter that has no page cursor
// behind it.
//
// A value that is absent, unparseable or out of range becomes the
// default rather than an error, because these are read paths where a
// bad limit should still return something useful. That is the opposite
// of the choice made for `state` and `days` filters, where a value the
// server cannot honour is refused -- and the difference is that a
// substituted limit returns fewer rows, while a substituted filter
// returns rows the caller did not ask for and will misread.
func boundedListLimit(raw string) int {
	if raw == "" {
		return defaultListLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return defaultListLimit
	}
	if n > maxListLimit {
		return maxListLimit
	}
	return n
}
