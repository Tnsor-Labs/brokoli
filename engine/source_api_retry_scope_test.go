package engine

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// runSourceAPIPipeline runs a minimal single-node source_api pipeline
// against server and returns the run's logs, for asserting on warnings
// runSourceAPI emits about execution() policy fields.
func runSourceAPIPipeline(t *testing.T, id string, config map[string]interface{}, serverURL string) []models.LogEntry {
	t.Helper()
	eng, s := newResumeTestEngine(t)

	config["url"] = serverURL
	pipeline := &models.Pipeline{
		ID: id, Name: id, Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceAPI, Name: "Source", Config: config},
		},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success (error: %s)", run.Status, run.Error)
	}

	logs, err := s.GetLogs(run.ID)
	if err != nil {
		t.Fatalf("get logs: %v", err)
	}
	return logs
}

func hasLogContaining(logs []models.LogEntry, substr string) bool {
	for _, l := range logs {
		if strings.Contains(l.Message, substr) {
			return true
		}
	}
	return false
}

func simpleJSONServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
}

func TestRunSourceAPI_RetryScopePage_NoWarning(t *testing.T) {
	server := simpleJSONServer(t)
	defer server.Close()

	logs := runSourceAPIPipeline(t, "p-retry-scope-page", map[string]interface{}{
		"execution": map[string]interface{}{"retry_scope": "page"},
	}, server.URL)

	if hasLogContaining(logs, "retry_scope") {
		t.Errorf("retry_scope=\"page\" should not produce a warning, logs: %+v", logs)
	}
}

func TestRunSourceAPI_RetryScopeUnrecognized_Warns(t *testing.T) {
	server := simpleJSONServer(t)
	defer server.Close()

	logs := runSourceAPIPipeline(t, "p-retry-scope-bogus", map[string]interface{}{
		"execution": map[string]interface{}{"retry_scope": "partition"},
	}, server.URL)

	if !hasLogContaining(logs, `retry_scope="partition" is not a recognized value`) {
		t.Errorf("expected a warning about unrecognized retry_scope, logs: %+v", logs)
	}
}

func TestRunSourceAPI_RetryScopeUnset_NoWarning(t *testing.T) {
	server := simpleJSONServer(t)
	defer server.Close()

	logs := runSourceAPIPipeline(t, "p-retry-scope-unset", map[string]interface{}{}, server.URL)

	if hasLogContaining(logs, "retry_scope") {
		t.Errorf("no retry_scope set should not produce a warning, logs: %+v", logs)
	}
}
