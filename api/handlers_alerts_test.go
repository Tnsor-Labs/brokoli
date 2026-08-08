package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func newAlertHandlerTestStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "alerts.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// newAlertTestRouter mirrors routes.go's real wiring.
func newAlertTestRouter(s store.Store) *chi.Mux {
	h := NewAlertHandler(s)
	r := chi.NewRouter()
	r.Get("/alerts", h.List)
	r.Post("/alerts/{id}/read", h.MarkRead)
	r.Post("/alerts/read-all", h.MarkAllRead)
	r.Delete("/alerts/{id}", h.Dismiss)
	r.Get("/dlq", h.ListDLQ)
	return r
}

// withOrg attaches the org the enterprise middleware would normally set.
func withOrg(req *http.Request, orgID string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), OrgIDContextKey{}, orgID))
}

func seedAlert(t *testing.T, s store.Store, id, orgID, title string) {
	t.Helper()
	if err := s.CreateAlert(&models.Alert{
		ID: id, OrgID: orgID, Kind: models.AlertKindRunFailure,
		Severity: models.AlertSeverityCritical, Title: title,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed alert: %v", err)
	}
}

func TestAlertHandler_List_ReturnsAlertsAndUnreadCount(t *testing.T) {
	s := newAlertHandlerTestStore(t)
	seedAlert(t, s, "a1", "org-1", "pipeline failed")
	router := newAlertTestRouter(s)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withOrg(httptest.NewRequest(http.MethodGet, "/alerts", nil), "org-1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Alerts      []models.Alert `json:"alerts"`
		UnreadCount int            `json:"unread_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Alerts) != 1 || resp.UnreadCount != 1 {
		t.Fatalf("got %d alerts / unread %d, want 1 / 1", len(resp.Alerts), resp.UnreadCount)
	}
}

func TestAlertHandler_List_EmptyIsArrayNotNull(t *testing.T) {
	router := newAlertTestRouter(newAlertHandlerTestStore(t))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withOrg(httptest.NewRequest(http.MethodGet, "/alerts", nil), "org-1"))

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["alerts"] == nil {
		t.Error("expected an empty array, not null — clients .map() this")
	}
}

// The isolation test that matters: one tenant must not be able to read or
// mutate another tenant's alerts through the HTTP surface.
func TestAlertHandler_CrossOrgAccessIsRejected(t *testing.T) {
	s := newAlertHandlerTestStore(t)
	seedAlert(t, s, "a-other", "org-b", "org b failure")
	router := newAlertTestRouter(s)

	// org-a lists: must not see org-b's alert.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withOrg(httptest.NewRequest(http.MethodGet, "/alerts", nil), "org-a"))
	var resp struct {
		Alerts []models.Alert `json:"alerts"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Alerts) != 0 {
		t.Errorf("org-a must not see org-b's alerts, got %+v", resp.Alerts)
	}

	// org-a marks read / dismisses org-b's alert: must 404.
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/alerts/a-other/read"},
		{http.MethodDelete, "/alerts/a-other"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, withOrg(httptest.NewRequest(tc.method, tc.path, nil), "org-a"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}

	// org-b's alert must still be untouched.
	list, _ := s.ListAlerts("org-b", false, 0)
	if len(list) != 1 || list[0].IsRead() || list[0].IsDismissed() {
		t.Errorf("org-b's alert was modified across the tenant boundary: %+v", list)
	}
}

func TestAlertHandler_MarkRead(t *testing.T) {
	s := newAlertHandlerTestStore(t)
	seedAlert(t, s, "a1", "org-1", "failed")
	router := newAlertTestRouter(s)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withOrg(httptest.NewRequest(http.MethodPost, "/alerts/a1/read", nil), "org-1"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n, _ := s.CountUnreadAlerts("org-1"); n != 0 {
		t.Errorf("unread = %d, want 0", n)
	}
}

func TestAlertHandler_MarkAllRead(t *testing.T) {
	s := newAlertHandlerTestStore(t)
	seedAlert(t, s, "a1", "org-1", "one")
	seedAlert(t, s, "a2", "org-1", "two")
	router := newAlertTestRouter(s)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withOrg(httptest.NewRequest(http.MethodPost, "/alerts/read-all", nil), "org-1"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n, _ := s.CountUnreadAlerts("org-1"); n != 0 {
		t.Errorf("unread = %d, want 0", n)
	}
}

func TestAlertHandler_Dismiss(t *testing.T) {
	s := newAlertHandlerTestStore(t)
	seedAlert(t, s, "a1", "org-1", "noisy")
	router := newAlertTestRouter(s)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withOrg(httptest.NewRequest(http.MethodDelete, "/alerts/a1", nil), "org-1"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	list, _ := s.ListAlerts("org-1", false, 0)
	if len(list) != 0 {
		t.Errorf("dismissed alert still listed: %+v", list)
	}
}

func TestAlertHandler_UnknownIDReturns404(t *testing.T) {
	router := newAlertTestRouter(newAlertHandlerTestStore(t))
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/alerts/nope/read"},
		{http.MethodDelete, "/alerts/nope"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, withOrg(httptest.NewRequest(tc.method, tc.path, nil), "org-1"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
}

func TestAlertHandler_ListDLQ_EmptyIsArrayNotNull(t *testing.T) {
	router := newAlertTestRouter(newAlertHandlerTestStore(t))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withOrg(httptest.NewRequest(http.MethodGet, "/dlq", nil), "org-1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body == "null\n" || body == "null" {
		t.Error("expected an empty array, not null")
	}
}
