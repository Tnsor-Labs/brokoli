package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

func newTemplateSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "templates.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSQLiteStore_PipelineTemplates_SeededOnFirstMigrate(t *testing.T) {
	s := newTemplateSQLiteStore(t)
	list, err := s.ListPipelineTemplates()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 5 {
		t.Fatalf("got %d seeded templates, want 5 (blank + 4 real starters)", len(list))
	}
	ids := make(map[string]bool, len(list))
	for _, tmpl := range list {
		ids[tmpl.ID] = true
	}
	for _, want := range []string{"blank", "hello-world", "api-fetch", "join-aggregate", "data-quality"} {
		if !ids[want] {
			t.Errorf("expected seeded template %q, not found in %v", want, ids)
		}
	}
}

func TestSQLiteStore_PipelineTemplates_SeedingDoesNotOverwriteAdminChanges(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "templates.db")
	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	if err := s.DeletePipelineTemplate("blank"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	edited, err := s.GetPipelineTemplate("hello-world")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	edited.Name = "Renamed By Admin"
	edited.UpdatedAt = time.Now().UTC()
	if err := s.UpdatePipelineTemplate(edited); err != nil {
		t.Fatalf("update: %v", err)
	}
	s.Close()

	// Reopen — migrate() runs again. Seeding must be a no-op this time.
	s2, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer s2.Close()

	if _, err := s2.GetPipelineTemplate("blank"); err == nil {
		t.Error("expected the admin's deletion of 'blank' to survive a restart")
	}
	helloWorld, err := s2.GetPipelineTemplate("hello-world")
	if err != nil {
		t.Fatalf("get after reopen: %v", err)
	}
	if helloWorld.Name != "Renamed By Admin" {
		t.Errorf("Name = %q, want the admin's rename to survive a restart", helloWorld.Name)
	}
	list, err := s2.ListPipelineTemplates()
	if err != nil {
		t.Fatalf("list after reopen: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("got %d templates after reopen, want 4 (5 seeded - 1 deleted, re-seeding must not re-add it)", len(list))
	}
}

func TestSQLiteStore_PipelineTemplate_CreateGetUpdateDelete(t *testing.T) {
	s := newTemplateSQLiteStore(t)
	now := time.Now().UTC()

	tmpl := &models.PipelineTemplate{
		ID:          "custom-1",
		Name:        "Custom",
		Description: "a custom template",
		Icon:        "file",
		Nodes: []models.Node{
			{ID: "s1", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": "/tmp/in.csv"}},
		},
		Edges:     []models.Edge{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreatePipelineTemplate(tmpl); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetPipelineTemplate("custom-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Custom" || len(got.Nodes) != 1 || got.Nodes[0].ID != "s1" {
		t.Fatalf("round-tripped template mismatch: %+v", got)
	}

	got.Description = "updated description"
	got.UpdatedAt = time.Now().UTC()
	if err := s.UpdatePipelineTemplate(got); err != nil {
		t.Fatalf("update: %v", err)
	}
	reGot, err := s.GetPipelineTemplate("custom-1")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if reGot.Description != "updated description" {
		t.Errorf("Description = %q, want the updated value", reGot.Description)
	}

	if err := s.DeletePipelineTemplate("custom-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetPipelineTemplate("custom-1"); err == nil {
		t.Error("expected the template to be gone after delete")
	}
}

func TestSQLiteStore_DeletePipelineTemplate_UnknownIDReturnsError(t *testing.T) {
	s := newTemplateSQLiteStore(t)
	if err := s.DeletePipelineTemplate("does-not-exist"); err == nil {
		t.Error("expected an error deleting a nonexistent template")
	}
}

func TestSQLiteStore_UpdatePipelineTemplate_UnknownIDReturnsError(t *testing.T) {
	s := newTemplateSQLiteStore(t)
	err := s.UpdatePipelineTemplate(&models.PipelineTemplate{ID: "does-not-exist", Name: "x", UpdatedAt: time.Now()})
	if err == nil {
		t.Error("expected an error updating a nonexistent template")
	}
}
