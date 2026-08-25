package engine

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// TestSinkAPI_BlocksLoopbackTarget pins the sink_api SSRF fix: previously
// this node made outbound requests with zero target validation at all.
// A pipeline editor pointing sink_api at a loopback/internal address
// must now fail the run, not silently succeed in reaching it.
func TestSinkAPI_BlocksLoopbackTarget(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	csvPath := writeCSV(t, t.TempDir(), "in.csv", "id,name\n1,brokoli\n")

	pipeline := &models.Pipeline{
		ID: "p-sink-api-ssrf", Name: "SinkAPISSRF", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source",
				Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "sink", Type: models.NodeTypeSinkAPI, Name: "Sink",
				Config: map[string]interface{}{"url": "http://127.0.0.1:1/should-be-blocked"}},
		},
		Edges: []models.Edge{{From: "source", To: "sink"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	// A blocked sink_api target fails the run -- RunPipeline surfaces
	// that as a non-nil error (the node-failure message), not just via
	// run.Status. Both are checked below.
	run, err := eng.RunPipeline(pipeline.ID)
	if err == nil {
		t.Fatal("expected RunPipeline to report the sink node's failure, got nil error")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("RunPipeline error = %q, want it to mention the target was blocked", err.Error())
	}
	if run == nil {
		t.Fatal("expected a run record even though the pipeline failed")
	}
	if run.Status == models.RunStatusSuccess {
		t.Fatal("expected the run to fail -- sink_api reached a loopback target it should have blocked")
	}
	nodeRuns, err := s.ListNodeRunsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, nr := range nodeRuns {
		if nr.NodeID != "sink" {
			continue
		}
		found = true
		if !strings.Contains(nr.Error, "blocked") {
			t.Fatalf("sink node error = %q, want it to mention the target was blocked", nr.Error)
		}
	}
	if !found {
		t.Fatal("no node run recorded for the sink node")
	}
}

// TestFireHook_BlocksLoopbackURL pins the pipeline-hook SSRF fix: a
// pipeline's on_start/on_success/on_failure hook.URL is
// pipeline-editor-supplied config, previously fired with zero target
// validation. This doesn't fail the run (fireHook is fire-and-forget,
// same as before) -- it proves the request itself was actually blocked,
// via the warning fireHook logs on failure.
func TestFireHook_BlocksLoopbackURL(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	csvPath := writeCSV(t, t.TempDir(), "in.csv", "id,name\n1,brokoli\n")

	pipeline := &models.Pipeline{
		ID: "p-hook-ssrf", Name: "HookSSRF", Enabled: true,
		Hooks: map[string]models.Hook{
			"on_start": {Type: "webhook", URL: "http://127.0.0.1:1/should-be-blocked", Enabled: true},
		},
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source",
				Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
		},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success (a hook failure must not fail the run): %s", run.Status, run.Error)
	}

	logs, err := s.GetLogs(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var hookWarning string
	for _, l := range logs {
		if strings.Contains(l.Message, "Hook on_start") {
			hookWarning = l.Message
			break
		}
	}
	if hookWarning == "" {
		t.Fatal("expected a logged warning for the on_start hook, found none")
	}
	if !strings.Contains(hookWarning, "blocked") {
		t.Fatalf("hook warning = %q, want it to mention the target was blocked (i.e. the SSRF guard actually engaged, not some other failure)", hookWarning)
	}
}

// TestNotifiyNode_BlocksLoopbackTargets proves that notify nodes cannot
// send Slack or webhook requests to services on the Brokoli host.
func TestNotifyNode_BlocksLoopbackTargets(t *testing.T) {
	tests := []struct {
		name       string
		notifyType string
	}{
		{name: "webhook", notifyType: "webhook"},
		{name: "slack", notifyType: "slack"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng, s := newResumeTestEngine(t)
			csvPath := writeCSV(
				t,
				t.TempDir(),
				"in.csv",
				"id,name\n1,brokoli\n",
			)

			pipeline := &models.Pipeline{
				ID:      "p-notify-" + tt.name + "-ssrf",
				Name:    "Notify SSRF " + tt.name,
				Enabled: true,
				Nodes: []models.Node{
					{
						ID:   "source",
						Type: models.NodeTypeSourceFile,
						Name: "Source",
						Config: map[string]interface{}{
							"path":   csvPath,
							"format": "csv",
						},
					},
					{
						ID:   "notify",
						Type: models.NodeTypeNotify,
						Name: "Notify",
						Config: map[string]interface{}{
							"notify_type": tt.notifyType,
							"webhook_url": "http://127.0.0.1:1/should-be-blocked",
						},
					},
				},
				Edges: []models.Edge{
					{From: "source", To: "notify"},
				},
			}

			if err := s.CreatePipeline(pipeline); err != nil {
				t.Fatal(err)
			}

			run, err := eng.RunPipeline(pipeline.ID)
			if err == nil {
				t.Fatal("expected the notify node to reject a loopback target")
			}
			if !strings.Contains(err.Error(), "blocked") {
				t.Fatalf(
					"RunPipeline error = %q, want it to mention the target was blocked",
					err.Error(),
				)
			}
			if run == nil {
				t.Fatal("expected a persisted run even though the notify node failed")
			}
			if run.Status == models.RunStatusSuccess {
				t.Fatal("expected the run to fail")
			}
		})
	}
}

// TestHTTPConnector_BlocksLoopbackTargets proves that both directions of
// the generic HTTP connector enforce the outbound network policy.
func TestHTTPConnector_BlocksLoopbackTargets(t *testing.T) {
	connector := NewHTTPConnector("http")
	const target = "http://127.0.0.1:1/should-be-blocked"

	t.Run("read", func(t *testing.T) {
		_, err := connector.Read(map[string]interface{}{
			"url": target,
		})
		if err == nil {
			t.Fatal("expected HTTPConnector.Read to reject a loopback target")
		}
		if !strings.Contains(err.Error(), "blocked") {
			t.Fatalf(
				"Read error = %q, want it to mention the target was blocked",
				err.Error(),
			)
		}
	})

	t.Run("write", func(t *testing.T) {
		err := connector.Write(
			map[string]interface{}{
				"url": target,
			},
			&common.DataSet{},
		)
		if err == nil {
			t.Fatal("expected HTTPConnector.Write to reject a loopback target")
		}
		if !strings.Contains(err.Error(), "blocked") {
			t.Fatalf(
				"Write error = %q, want it to mention the target was blocked",
				err.Error(),
			)
		}
	})
}

// TestWebhookAndAlertPaths_BlockLoopback proves that legacy pipeline
// webhooks and configurable alert channels use the outbound network policy.
func TestWebhookAndAlertPaths_BlockLoopback(t *testing.T) {
	const target = "http://127.0.0.1:1/should-be-blocked"

	payload := AlertPayload{
		Pipeline:   "Test Pipeline",
		PipelineID: "test-pipeline",
		RunID:      "12345678",
		Status:     "failed",
		Timestamp:  "2026-08-22T00:00:00Z",
	}

	tests := []struct {
		name string
		send func()
	}{
		{
			name: "legacy_pipeline_webhook",
			send: func() {
				SendWebhook(target, WebhookPayload{})
			},
		},
		{
			name: "slack_alert",
			send: func() {
				SendAlert(
					AlertChannel{
						Type: "slack",
						Config: map[string]string{
							"webhook_url": target,
						},
					},
					payload,
				)
			},
		},
		{
			name: "webhook_alert",
			send: func() {
				SendAlert(
					AlertChannel{
						Type: "webhook",
						Config: map[string]string{
							"url": target,
						},
					},
					payload,
				)
			},
		},
	}

	var logs bytes.Buffer
	originalOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() {
		log.SetOutput(originalOutput)
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs.Reset()
			tt.send()

			if !strings.Contains(logs.String(), "blocked") {
				t.Fatalf(
					"log = %q, want it to mention the target was blocked",
					logs.String(),
				)
			}
		})
	}
}
