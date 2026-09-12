package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func preview(t *testing.T, body string) (int, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/schedule/preview", strings.NewReader(body))
	rec := httptest.NewRecorder()
	SchedulePreview(rec, req)

	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON (%d): %s", rec.Code, rec.Body.String())
	}
	return rec.Code, out
}

func TestSchedulePreviewCompilesAPhrase(t *testing.T) {
	code, out := preview(t, `{"input":"every weekday at 9am","timezone":"Africa/Maputo","count":3}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if out["valid"] != true {
		t.Fatalf("valid = %v, error = %v", out["valid"], out["error"])
	}
	if out["cron"] != "0 9 * * 1-5" {
		t.Errorf("cron = %v, want 0 9 * * 1-5", out["cron"])
	}
	if out["description"] != "Weekdays at 09:00" {
		t.Errorf("description = %v", out["description"])
	}

	next, _ := out["next"].([]interface{})
	if len(next) != 3 {
		t.Fatalf("next has %d entries, want 3", len(next))
	}
	for _, raw := range next {
		ts, err := time.Parse(time.RFC3339, raw.(string))
		if err != nil {
			t.Fatalf("occurrence %q is not RFC3339: %v", raw, err)
		}
		if ts.Hour() != 9 {
			t.Errorf("occurrence %s is not at 09:00 in the requested zone", raw)
		}
		if wd := ts.Weekday(); wd == time.Saturday || wd == time.Sunday {
			t.Errorf("occurrence %s falls on a %s, but weekdays were asked for", raw, wd)
		}
	}
}

// Cron in, cron out: the caller does not have to know which form the
// user typed.
func TestSchedulePreviewAcceptsCronDirectly(t *testing.T) {
	_, out := preview(t, `{"input":"*/15 * * * *"}`)
	if out["valid"] != true {
		t.Fatalf("valid = %v, error = %v", out["valid"], out["error"])
	}
	if out["cron"] != "*/15 * * * *" {
		t.Errorf("cron = %v, want it unchanged", out["cron"])
	}
	if out["description"] != "Every 15 minutes" {
		t.Errorf("description = %v", out["description"])
	}
}

// A refusal is a 200 with valid:false, not an HTTP error: the editor
// asks this on every keystroke and a failing request is not a failing
// call.
func TestSchedulePreviewRefusesWithAReason(t *testing.T) {
	code, out := preview(t, `{"input":"every 90 minutes"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even for a refused phrase", code)
	}
	if out["valid"] != false {
		t.Fatalf("valid = %v, want false", out["valid"])
	}
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, "divide an hour evenly") {
		t.Errorf("error = %q, which does not explain why", msg)
	}
	sugg, _ := out["suggestion"].(string)
	if sugg == "" {
		t.Error("no suggestion offered; a refusal without a way forward is a dead end")
	}
	if _, ok := out["next"]; ok {
		t.Error("a refused schedule must not come with occurrences")
	}
}

func TestSchedulePreviewRejectsAnUnknownTimezone(t *testing.T) {
	_, out := preview(t, `{"input":"every day at 9am","timezone":"Mars/Olympus_Mons"}`)
	if out["valid"] != false {
		t.Fatalf("valid = %v, want false", out["valid"])
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "Mars/Olympus_Mons") {
		t.Errorf("error = %q, which does not name the timezone", msg)
	}
}

// An expression outside the closed grammar is still valid cron and still
// gets occurrences; only the description is empty, so the editor shows
// the expression rather than a guess.
func TestSchedulePreviewLeavesUnknownShapesUndescribed(t *testing.T) {
	_, out := preview(t, `{"input":"0 9 1 1 *"}`)
	if out["valid"] != true {
		t.Fatalf("valid = %v, error = %v", out["valid"], out["error"])
	}
	if out["description"] != "" {
		t.Errorf("description = %q, want empty rather than a guess", out["description"])
	}
	if next, _ := out["next"].([]interface{}); len(next) == 0 {
		t.Error("valid cron must still get occurrences")
	}
}

func TestSchedulePreviewClampsCount(t *testing.T) {
	_, out := preview(t, `{"input":"every hour","count":500}`)
	next, _ := out["next"].([]interface{})
	if len(next) > 10 {
		t.Errorf("count was not clamped: got %d occurrences", len(next))
	}
	if len(next) == 0 {
		t.Error("count clamping should not produce an empty result")
	}
}

func TestSchedulePreviewRejectsGarbageBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/schedule/preview", strings.NewReader(`{not json`))
	rec := httptest.NewRecorder()
	SchedulePreview(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
