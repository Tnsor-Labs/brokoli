package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

// Incident ownership on alerts (brokoli-ee#242).

func incidentStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "incidents.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func incidentRouter(s store.Store) *chi.Mux {
	h := NewAlertHandler(s)
	r := chi.NewRouter()
	r.Get("/alerts", h.List)
	r.Post("/alerts/{id}/read", h.MarkRead)
	r.Post("/alerts/read-all", h.MarkAllRead)
	r.Post("/alerts/{id}/assign", h.Assign)
	r.Post("/alerts/{id}/acknowledge", h.Acknowledge)
	r.Post("/alerts/{id}/resolve", h.Resolve)
	return r
}

func incidentReq(method, path, orgID, userID, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	ctx := r.Context()
	if orgID != "" {
		ctx = context.WithValue(ctx, OrgIDContextKey{}, orgID)
	}
	if userID != "" {
		claims := jwt.MapClaims{"sub": userID}
		ctx = context.WithValue(ctx, "claims", &claims)
	}
	return r.WithContext(ctx)
}

func incidentDo(t *testing.T, s store.Store, method, path, orgID, userID, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	incidentRouter(s).ServeHTTP(rec, incidentReq(method, path, orgID, userID, body))
	return rec
}

func seedIncidentAlert(t *testing.T, s store.Store, id, orgID string) {
	t.Helper()
	if err := s.CreateAlert(&models.Alert{
		ID: id, OrgID: orgID, Kind: "run_failure", Severity: "error",
		Title: "a pipeline failed", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed alert %s: %v", id, err)
	}
}

func listAlerts(t *testing.T, s store.Store, query, orgID, userID string) ([]models.Alert, int) {
	t.Helper()
	rec := incidentDo(t, s, http.MethodGet, "/alerts"+query, orgID, userID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /alerts%s: status = %d; body=%s", query, rec.Code, rec.Body.String())
	}
	var out struct {
		Alerts      []models.Alert `json:"alerts"`
		UnreadCount int            `json:"unread_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	return out.Alerts, out.UnreadCount
}

// The headline of #242: read state was a column on the alert, so one
// person marking an alert read marked it read for the whole organization.
func TestReadStateIsPerPerson(t *testing.T) {
	s := incidentStore(t)
	seedIncidentAlert(t, s, "a1", "org-1")
	seedIncidentAlert(t, s, "a2", "org-1")

	if rec := incidentDo(t, s, http.MethodPost, "/alerts/a1/read", "org-1", "u1", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("mark read: status = %d; body=%s", rec.Code, rec.Body.String())
	}

	// u1 sees one read, one unread.
	alerts, unread := listAlerts(t, s, "", "org-1", "u1")
	if len(alerts) != 2 {
		t.Fatalf("u1 sees %d alerts, want 2", len(alerts))
	}
	if unread != 1 {
		t.Errorf("u1 unread_count = %d, want 1", unread)
	}

	// u2 has read nothing, and must not inherit u1's mark.
	alerts, unread = listAlerts(t, s, "", "org-1", "u2")
	if unread != 2 {
		t.Errorf("u2 unread_count = %d, want 2; u1's read must not be u2's", unread)
	}
	for _, a := range alerts {
		if a.ReadAt != nil {
			t.Errorf("u2 sees alert %s as read, which only u1 read", a.ID)
		}
	}

	// unread=true is per person too.
	alerts, _ = listAlerts(t, s, "?unread=true", "org-1", "u1")
	if len(alerts) != 1 || alerts[0].ID != "a2" {
		t.Errorf("u1 unread list = %+v, want only a2", alerts)
	}
	alerts, _ = listAlerts(t, s, "?unread=true", "org-1", "u2")
	if len(alerts) != 2 {
		t.Errorf("u2 unread list has %d, want 2", len(alerts))
	}
}

// Mark-all is per person as well, or one person clearing their inbox
// clears everybody's.
func TestMarkAllReadIsPerPerson(t *testing.T) {
	s := incidentStore(t)
	seedIncidentAlert(t, s, "a1", "org-1")
	seedIncidentAlert(t, s, "a2", "org-1")

	if rec := incidentDo(t, s, http.MethodPost, "/alerts/read-all", "org-1", "u1", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("read-all: status = %d", rec.Code)
	}
	if _, unread := listAlerts(t, s, "", "org-1", "u1"); unread != 0 {
		t.Errorf("u1 unread = %d, want 0", unread)
	}
	if _, unread := listAlerts(t, s, "", "org-1", "u2"); unread != 2 {
		t.Errorf("u2 unread = %d, want 2; u1 clearing their own inbox must not clear u2's", unread)
	}
}

// Assign, acknowledge, resolve, and the state each produces.
func TestIncidentLifecycle(t *testing.T) {
	s := incidentStore(t)
	seedIncidentAlert(t, s, "a1", "org-1")

	alerts, _ := listAlerts(t, s, "", "org-1", "u1")
	if got := alerts[0].AlertState(); got != "open" {
		t.Fatalf("state = %q, want open", got)
	}

	if rec := incidentDo(t, s, http.MethodPost, "/alerts/a1/assign", "org-1", "u1", `{"user_id":"u2"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("assign: status = %d; body=%s", rec.Code, rec.Body.String())
	}
	alerts, _ = listAlerts(t, s, "", "org-1", "u1")
	if alerts[0].AssigneeUserID != "u2" {
		t.Fatalf("assignee = %q, want u2", alerts[0].AssigneeUserID)
	}
	if got := alerts[0].AlertState(); got != "open" {
		t.Errorf("state after assigning = %q, want open; nobody has said they are on it", got)
	}

	if rec := incidentDo(t, s, http.MethodPost, "/alerts/a1/acknowledge", "org-1", "u3", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("acknowledge: status = %d; body=%s", rec.Code, rec.Body.String())
	}
	alerts, _ = listAlerts(t, s, "", "org-1", "u1")
	if got := alerts[0].AlertState(); got != "acknowledged" {
		t.Errorf("state = %q, want acknowledged", got)
	}
	if alerts[0].AcknowledgedBy != "u3" {
		t.Errorf("acknowledged_by = %q, want u3", alerts[0].AcknowledgedBy)
	}
	// Acknowledging does not steal an existing assignment.
	if alerts[0].AssigneeUserID != "u2" {
		t.Errorf("assignee = %q after u3 acknowledged, want u2 kept", alerts[0].AssigneeUserID)
	}

	if rec := incidentDo(t, s, http.MethodPost, "/alerts/a1/resolve", "org-1", "u3", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("resolve: status = %d", rec.Code)
	}
	alerts, _ = listAlerts(t, s, "", "org-1", "u1")
	if got := alerts[0].AlertState(); got != "resolved" {
		t.Errorf("state = %q, want resolved", got)
	}
	if alerts[0].ResolvedBy != "u3" {
		t.Errorf("resolved_by = %q, want u3", alerts[0].ResolvedBy)
	}
}

// Acknowledging an unowned incident takes it. "I am on this" and "nobody
// owns this" cannot both be true, and making somebody press two buttons
// to say one thing is how an incident ends up acknowledged and unowned.
func TestAcknowledgingAnUnownedIncidentTakesIt(t *testing.T) {
	s := incidentStore(t)
	seedIncidentAlert(t, s, "a1", "org-1")

	if rec := incidentDo(t, s, http.MethodPost, "/alerts/a1/acknowledge", "org-1", "u1", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("acknowledge: status = %d", rec.Code)
	}
	alerts, _ := listAlerts(t, s, "", "org-1", "u1")
	if alerts[0].AssigneeUserID != "u1" {
		t.Errorf("assignee = %q, want u1; acknowledging an unowned incident assigns it", alerts[0].AssigneeUserID)
	}
}

func TestUnassignIsExpressible(t *testing.T) {
	s := incidentStore(t)
	seedIncidentAlert(t, s, "a1", "org-1")

	incidentDo(t, s, http.MethodPost, "/alerts/a1/assign", "org-1", "u1", `{"user_id":"u2"}`)
	if rec := incidentDo(t, s, http.MethodPost, "/alerts/a1/assign", "org-1", "u1", `{"user_id":null}`); rec.Code != http.StatusNoContent {
		t.Fatalf("unassign: status = %d; body=%s", rec.Code, rec.Body.String())
	}
	alerts, _ := listAlerts(t, s, "", "org-1", "u1")
	if alerts[0].AssigneeUserID != "" {
		t.Errorf("assignee = %q, want empty after unassigning", alerts[0].AssigneeUserID)
	}
}

// The filters, and the refusal of one that cannot be honoured. An
// unrecognised state silently ignored returns every alert, which the
// caller renders as "these are the open ones".
func TestStateAndAssigneeFilters(t *testing.T) {
	s := incidentStore(t)
	for _, id := range []string{"open-1", "ack-1", "res-1"} {
		seedIncidentAlert(t, s, id, "org-1")
	}
	incidentDo(t, s, http.MethodPost, "/alerts/ack-1/acknowledge", "org-1", "u1", "")
	incidentDo(t, s, http.MethodPost, "/alerts/res-1/resolve", "org-1", "u1", "")

	for _, tc := range []struct {
		state string
		want  string
	}{{"open", "open-1"}, {"acknowledged", "ack-1"}, {"resolved", "res-1"}} {
		alerts, _ := listAlerts(t, s, "?state="+tc.state, "org-1", "u1")
		if len(alerts) != 1 || alerts[0].ID != tc.want {
			t.Errorf("state=%s returned %+v, want only %s", tc.state, alerts, tc.want)
		}
	}

	alerts, _ := listAlerts(t, s, "?state=open", "org-1", "u1")
	_ = alerts

	if rec := incidentDo(t, s, http.MethodGet, "/alerts?state=urgent", "org-1", "u1", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("state=urgent: status = %d, want 400; a filter that does nothing returns everything", rec.Code)
	}

	// assignee=me, and nothing else.
	incidentDo(t, s, http.MethodPost, "/alerts/open-1/assign", "org-1", "u1", `{"user_id":"u9"}`)
	alerts, _ = listAlerts(t, s, "?assignee=me", "org-1", "u9")
	if len(alerts) != 1 || alerts[0].ID != "open-1" {
		t.Errorf("assignee=me for u9 returned %+v, want open-1", alerts)
	}
	alerts, _ = listAlerts(t, s, "?assignee=me", "org-1", "u1")
	if len(alerts) != 1 || alerts[0].ID != "ack-1" {
		t.Errorf("assignee=me for u1 returned %+v, want the one they acknowledged", alerts)
	}
	if rec := incidentDo(t, s, http.MethodGet, "/alerts?assignee=u9", "org-1", "u1", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("assignee=u9: status = %d, want 400", rec.Code)
	}
}

// Every incident route is scoped to the caller's organization, and says
// "not found" rather than distinguishing a missing alert from another
// tenant's.
func TestIncidentRoutesAreOrgScoped(t *testing.T) {
	s := incidentStore(t)
	seedIncidentAlert(t, s, "theirs", "org-other")

	for _, tc := range []struct{ path, body string }{
		{"/alerts/theirs/assign", `{"user_id":"u1"}`},
		{"/alerts/theirs/acknowledge", ""},
		{"/alerts/theirs/resolve", ""},
		{"/alerts/theirs/read", ""},
	} {
		rec := incidentDo(t, s, http.MethodPost, tc.path, "org-1", "u1", tc.body)
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST %s from another org: status = %d, want 404; body=%s", tc.path, rec.Code, rec.Body.String())
		}
	}
	// And nothing was written to it.
	alerts, _ := listAlerts(t, s, "", "org-other", "u1")
	if len(alerts) != 1 {
		t.Fatalf("expected the one alert, got %d", len(alerts))
	}
	if alerts[0].AssigneeUserID != "" || alerts[0].AcknowledgedAt != nil || alerts[0].ResolvedAt != nil {
		t.Errorf("another org's alert was modified: %+v", alerts[0])
	}
}

// Saying "I am on it" with nobody to name is not a fact worth storing.
func TestAcknowledgeAndResolveNeedAnIdentity(t *testing.T) {
	s := incidentStore(t)
	seedIncidentAlert(t, s, "a1", "org-1")

	for _, path := range []string{"/alerts/a1/acknowledge", "/alerts/a1/resolve"} {
		rec := incidentDo(t, s, http.MethodPost, path, "org-1", "", "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s unauthenticated: status = %d, want 401", path, rec.Code)
		}
	}
}

// An incident that was acknowledged and then resolved is resolved, not
// still acknowledged -- otherwise the list of who is working on what
// grows for ever.
//
// Its own test rather than a case inside the filter test: the alert has
// to be assigned to somebody to be acknowledged, which changes what
// assignee=me returns there. The mutation that dropped the resolved
// clause passed while this was folded in, because the only resolved
// alert in that fixture had never been acknowledged.
func TestAcknowledgedThenResolvedIsResolved(t *testing.T) {
	s := incidentStore(t)
	seedIncidentAlert(t, s, "both-1", "org-1")
	incidentDo(t, s, http.MethodPost, "/alerts/both-1/acknowledge", "org-1", "u1", "")
	incidentDo(t, s, http.MethodPost, "/alerts/both-1/resolve", "org-1", "u1", "")

	if alerts, _ := listAlerts(t, s, "?state=acknowledged", "org-1", "u1"); len(alerts) != 0 {
		t.Errorf("state=acknowledged returned %+v, want none: it was resolved", alerts)
	}
	alerts, _ := listAlerts(t, s, "?state=resolved", "org-1", "u1")
	if len(alerts) != 1 || alerts[0].ID != "both-1" {
		t.Errorf("state=resolved returned %+v, want both-1", alerts)
	}
}
