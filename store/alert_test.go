package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

func newAlertTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "alerts.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mkAlert(id, orgID, title string) *models.Alert {
	return &models.Alert{
		ID:        id,
		OrgID:     orgID,
		Kind:      models.AlertKindRunFailure,
		Severity:  models.AlertSeverityCritical,
		Title:     title,
		Body:      "something broke",
		CreatedAt: time.Now().UTC(),
	}
}

func TestSQLiteStore_Alerts_CreateAndList(t *testing.T) {
	s := newAlertTestStore(t)
	if err := s.CreateAlert(mkAlert("a1", "org-1", "first")); err != nil {
		t.Fatalf("create: %v", err)
	}

	list, err := s.ListAlerts("org-1", false, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Title != "first" {
		t.Fatalf("unexpected list: %+v", list)
	}
	if list[0].IsRead() || list[0].IsDismissed() {
		t.Error("a new alert should be neither read nor dismissed")
	}
}

// Alerts are org-scoped. This is the test that matters most — a tenant must
// never see, read, or dismiss another tenant's alerts.
func TestSQLiteStore_Alerts_ScopedPerOrg(t *testing.T) {
	s := newAlertTestStore(t)
	if err := s.CreateAlert(mkAlert("a1", "org-a", "org a alert")); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAlert(mkAlert("a2", "org-b", "org b alert")); err != nil {
		t.Fatal(err)
	}

	listA, _ := s.ListAlerts("org-a", false, 0)
	if len(listA) != 1 || listA[0].ID != "a1" {
		t.Fatalf("org-a should see exactly its own alert, got %+v", listA)
	}

	// org-a must not be able to mutate org-b's alert.
	if err := s.MarkAlertRead("org-a", "a2"); err == nil {
		t.Error("expected an error marking another org's alert read")
	}
	if err := s.DismissAlert("org-a", "a2"); err == nil {
		t.Error("expected an error dismissing another org's alert")
	}

	listB, _ := s.ListAlerts("org-b", false, 0)
	if len(listB) != 1 || listB[0].IsRead() || listB[0].IsDismissed() {
		t.Errorf("org-b's alert must be untouched, got %+v", listB)
	}
}

func TestSQLiteStore_Alerts_ReadAndUnreadCount(t *testing.T) {
	s := newAlertTestStore(t)
	for _, id := range []string{"a1", "a2", "a3"} {
		if err := s.CreateAlert(mkAlert(id, "org-1", id)); err != nil {
			t.Fatal(err)
		}
	}

	if n, _ := s.CountUnreadAlerts("org-1"); n != 3 {
		t.Fatalf("unread = %d, want 3", n)
	}
	if err := s.MarkAlertRead("org-1", "a1"); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if n, _ := s.CountUnreadAlerts("org-1"); n != 2 {
		t.Fatalf("unread after one read = %d, want 2", n)
	}

	unread, _ := s.ListAlerts("org-1", true, 0)
	if len(unread) != 2 {
		t.Fatalf("unread-only list = %d, want 2", len(unread))
	}

	if err := s.MarkAllAlertsRead("org-1"); err != nil {
		t.Fatalf("mark all read: %v", err)
	}
	if n, _ := s.CountUnreadAlerts("org-1"); n != 0 {
		t.Fatalf("unread after read-all = %d, want 0", n)
	}
	// Marking an already-read alert again is benign, not an error.
	if err := s.MarkAlertRead("org-1", "a1"); err != nil {
		t.Errorf("re-marking a read alert should be a no-op, got: %v", err)
	}
}

// Dismissal hides an alert from listings but retains the row, so the action
// stays auditable.
func TestSQLiteStore_Alerts_DismissHidesButRetains(t *testing.T) {
	s := newAlertTestStore(t)
	if err := s.CreateAlert(mkAlert("a1", "org-1", "noisy")); err != nil {
		t.Fatal(err)
	}
	if err := s.DismissAlert("org-1", "a1"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	list, _ := s.ListAlerts("org-1", false, 0)
	if len(list) != 0 {
		t.Errorf("dismissed alerts must not appear in the listing, got %+v", list)
	}
	if n, _ := s.CountUnreadAlerts("org-1"); n != 0 {
		t.Errorf("a dismissed alert must not count as unread, got %d", n)
	}

	var retained int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE id = 'a1'`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 1 {
		t.Error("dismissal should retain the row, not delete it")
	}
	// Dismissing twice is an error, not a silent success.
	if err := s.DismissAlert("org-1", "a1"); err == nil {
		t.Error("expected an error dismissing an already-dismissed alert")
	}
}

func TestSQLiteStore_Alerts_UnknownIDErrors(t *testing.T) {
	s := newAlertTestStore(t)
	if err := s.MarkAlertRead("org-1", "nope"); err == nil {
		t.Error("expected an error for an unknown alert")
	}
	if err := s.DismissAlert("org-1", "nope"); err == nil {
		t.Error("expected an error for an unknown alert")
	}
}

func TestSQLiteStore_Alerts_NewestFirstAndLimited(t *testing.T) {
	s := newAlertTestStore(t)
	base := time.Now().UTC().Add(-time.Hour)
	for i, id := range []string{"old", "mid", "new"} {
		a := mkAlert(id, "org-1", id)
		a.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		if err := s.CreateAlert(a); err != nil {
			t.Fatal(err)
		}
	}

	list, _ := s.ListAlerts("org-1", false, 0)
	if len(list) != 3 || list[0].ID != "new" || list[2].ID != "old" {
		t.Fatalf("expected newest-first ordering, got %+v", list)
	}
	if limited, _ := s.ListAlerts("org-1", false, 2); len(limited) != 2 {
		t.Errorf("limit not honoured, got %d", len(limited))
	}
}
