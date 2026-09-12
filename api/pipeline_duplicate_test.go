package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Creating a pipeline whose name slugifies to an existing pipeline_id
// used to answer 500 with the driver's own text in it. That reads as an
// outage to any client, and it is what an ordinary re-run of a deploy
// script produces, since the id is derived from the name.
func TestCreatingADuplicatePipelineIDIsAConflict(t *testing.T) {
	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)

	body := `{"name":"battle test polyglot","nodes":[` +
		`{"id":"n1","type":"source_file","name":"S","config":{"path":"/tmp/x.csv"}}],"edges":[]}`

	rec := servePipelineHandler(t, http.MethodPost, "/pipelines", "/pipelines", []byte(body), h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create = %d, want 201: %s", rec.Code, rec.Body.String())
	}

	rec = servePipelineHandler(t, http.MethodPost, "/pipelines", "/pipelines", []byte(body), h.Create)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second create = %d, want 409: %s", rec.Code, rec.Body.String())
	}

	// The message has to name what collided, and must not leak the
	// driver's constraint text.
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !strings.Contains(out["error"], "pipeline_id") {
		t.Errorf("error %q does not name pipeline_id", out["error"])
	}
	for _, leak := range []string{"UNIQUE constraint", "idx_pipeline_pid", "SQLSTATE"} {
		if strings.Contains(out["error"], leak) {
			t.Errorf("error leaks driver detail %q: %s", leak, out["error"])
		}
	}
}
