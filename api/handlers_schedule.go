package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
)

// schedulePreviewMaxCount bounds what a caller can ask for. The work is
// cheap but unbounded iteration on a request parameter is not something
// to leave open.
const schedulePreviewMaxCount = 10

// SchedulePreview answers what a schedule input means, without saving
// anything.
//
// It exists because the editor cannot otherwise tell the user what their
// schedule will do: Scheduler.NextRun only answers for a pipeline that is
// already registered. Typing a schedule was previously a blind edit whose
// first feedback was a run happening, or not happening (#552).
//
// The occurrences come from the scheduler's own parser and timezone
// handling, so a preview cannot disagree with what will actually run. A
// preview computed some other way would be worse than none, because it
// would be trusted.
func SchedulePreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Input    string `json:"input"`
		Timezone string `json:"timezone"`
		Count    int    `json:"count"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	count := req.Count
	if count <= 0 {
		count = 3
	}
	if count > schedulePreviewMaxCount {
		count = schedulePreviewMaxCount
	}

	if req.Timezone != "" {
		if _, err := time.LoadLocation(req.Timezone); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"valid": false,
				"error": "unknown timezone " + req.Timezone,
			})
			return
		}
	}

	expr, err := engine.CompilePhrase(req.Input)
	if err != nil {
		body := map[string]interface{}{"valid": false, "error": err.Error()}
		// A refusal carries what to try instead; a bare parse failure does
		// not, and inventing one would be guessing.
		var pe *engine.PhraseError
		if errors.As(err, &pe) && pe.Suggestion != "" {
			body["suggestion"] = pe.Suggestion
		}
		writeJSON(w, http.StatusOK, body)
		return
	}

	next, err := engine.NextOccurrences(expr, req.Timezone, count, time.Now())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"valid": false,
			"error": err.Error(),
		})
		return
	}

	times := make([]string, len(next))
	for i, t := range next {
		times[i] = t.Format(time.RFC3339)
	}

	// A description outside the closed grammar comes back empty rather
	// than guessed. The editor then shows the expression itself, which is
	// honest; a wrong description would defeat the point of an echo.
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"valid":       true,
		"cron":        expr,
		"description": engine.DescribeCron(expr),
		"timezone":    req.Timezone,
		"next":        times,
	})
}
