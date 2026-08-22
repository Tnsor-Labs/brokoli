package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNotificationSettingsTest_BlocksLoopbackWebhook proves that testing
// a stored notification webhook cannot reach a service on the Brokoli host.
func TestNotificationSettingsTest_BlocksLoopbackWebhook(t *testing.T) {
	s := newOrgCheckStore(t)

	if err := s.SetSetting(
		"slack_webhook",
		"http://127.0.0.1:1/should-be-blocked",
	); err != nil {
		t.Fatal(err)
	}

	handler := NewNotificationSettingsHandler(s, nil)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/settings/notifications/test",
		nil,
	)
	rec := httptest.NewRecorder()

	handler.Test(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf(
			"status = %d, want %d",
			rec.Code,
			http.StatusBadGateway,
		)
	}
	if !strings.Contains(rec.Body.String(), "blocked") {
		t.Fatalf(
			"body = %q, want it to mention the target was blocked",
			rec.Body.String(),
		)
	}
}
