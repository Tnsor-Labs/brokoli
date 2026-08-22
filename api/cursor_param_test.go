package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The responses name this field "cursor", so a client feeding it back
// under that name must be understood. It used to be ignored, returning
// page one again with has_next still true — an infinite loop against
// the server, with no way for the caller to tell.
func TestCursorParamAcceptsCursor(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/pipelines/p1/runs?limit=25&cursor=run-100", nil)
	if got := CursorParam(r); got != "run-100" {
		t.Fatalf("CursorParam = %q, want run-100", got)
	}
}

// The original name keeps working for existing callers.
func TestCursorParamAcceptsAfter(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/pipelines/p1/runs?after=run-50", nil)
	if got := CursorParam(r); got != "run-50" {
		t.Fatalf("CursorParam = %q, want run-50", got)
	}
}

// With both, the documented one wins rather than the result depending on
// map ordering.
func TestCursorParamPrefersAfterWhenBothGiven(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/x?after=a&cursor=c", nil)
	if got := CursorParam(r); got != "a" {
		t.Fatalf("CursorParam = %q, want a", got)
	}
}

func TestCursorParamEmptyWhenAbsent(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/x?limit=10", nil)
	if got := CursorParam(r); got != "" {
		t.Fatalf("CursorParam = %q, want empty", got)
	}
}

// Either parameter selects the keyset form, as does an explicit limit.
func TestHasCursorPaginationRecognisesBothNames(t *testing.T) {
	for _, q := range []string{"?after=x", "?cursor=x", "?limit=10"} {
		if !HasCursorPagination(httptest.NewRequest(http.MethodGet, "/api/x"+q, nil)) {
			t.Fatalf("%q should select cursor pagination", q)
		}
	}
	if HasCursorPagination(httptest.NewRequest(http.MethodGet, "/api/x", nil)) {
		t.Fatal("a bare request should keep the plain-array form")
	}
	if HasCursorPagination(httptest.NewRequest(http.MethodGet, "/api/x?page=2", nil)) {
		t.Fatal("the offset form should not be treated as cursor pagination")
	}
}
