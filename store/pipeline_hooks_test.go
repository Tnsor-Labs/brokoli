package store

// Hooks were decoded into models.Pipeline and silently dropped by the
// store -- absent from every column list -- so a client that sent them got
// them echoed back on the create response and lost on every read after
// (issue #109 M1). These tests pin the full round-trip.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

func hookPipeline(id string) *models.Pipeline {
	now := time.Now().UTC()
	return &models.Pipeline{
		ID:   id,
		Name: "hooked",
		Nodes: []models.Node{{
			ID: "src", Type: models.NodeTypeSourceFile, Name: "Source",
			Config: map[string]interface{}{"path": "/tmp/in.csv"},
		}},
		Edges: []models.Edge{},
		Hooks: map[string]models.Hook{
			"on_failure": {
				Type: "webhook", URL: "https://hooks.example/fail", Enabled: true,
				Extra: map[string]string{"channel": "alerts"},
			},
			"on_success": {Type: "slack", URL: "https://hooks.slack.example/ok", Enabled: false},
		},
		CreatedAt: now, UpdatedAt: now,
	}
}

func TestPipelineHooksSurviveCreateAndGet(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "hooks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.CreatePipeline(hookPipeline("hooked-1")); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPipeline("hooked-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hooks) != 2 {
		t.Fatalf("hooks lost on read: %#v", got.Hooks)
	}
	fail := got.Hooks["on_failure"]
	if fail.URL != "https://hooks.example/fail" || !fail.Enabled || fail.Extra["channel"] != "alerts" {
		t.Fatalf("on_failure hook mangled: %#v", fail)
	}

	// List paths share the scanner -- hooks must survive there too.
	all, err := s.ListPipelines()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || len(all[0].Hooks) != 2 {
		t.Fatalf("hooks lost in list scan: %#v", all[0].Hooks)
	}
}

func TestPipelineHooksUpdateAndClear(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "hooks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	p := hookPipeline("hooked-2")
	if err := s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}

	p.Hooks = map[string]models.Hook{
		"on_start": {Type: "webhook", URL: "https://hooks.example/start", Enabled: true},
	}
	p.UpdatedAt = time.Now().UTC()
	if err := s.UpdatePipeline(p); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetPipeline("hooked-2")
	if len(got.Hooks) != 1 || got.Hooks["on_start"].URL != "https://hooks.example/start" {
		t.Fatalf("hooks not replaced on update: %#v", got.Hooks)
	}

	// Clearing hooks must persist as cleared, not resurrect old values --
	// and a hook-less pipeline reads back with nil Hooks, matching the
	// omitempty JSON contract.
	p.Hooks = nil
	if err := s.UpdatePipeline(p); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetPipeline("hooked-2")
	if got.Hooks != nil {
		t.Fatalf("cleared hooks resurrected: %#v", got.Hooks)
	}
}

func TestPipelineWithoutHooksUnchanged(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "hooks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	p := hookPipeline("plain")
	p.Hooks = nil
	if err := s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPipeline("plain")
	if err != nil {
		t.Fatal(err)
	}
	if got.Hooks != nil {
		t.Fatalf("hook-less pipeline grew hooks: %#v", got.Hooks)
	}
}
