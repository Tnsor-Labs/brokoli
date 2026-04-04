package models

import (
	"testing"
)

// ─── Organization.Validate XSS prevention ───────────────────────────────────

func TestOrganization_Validate_XSS(t *testing.T) {
	xssPayloads := []struct {
		name    string
		payload string
	}{
		{"script tag", "<script>alert('xss')</script>"},
		{"img onerror", "Acme<img src=x onerror=alert(1)>"},
		{"double quotes", `Org "name" with quotes`},
		{"single quotes", "Name with 'single quotes'"},
		{"ampersand", "Name & ampersand"},
		{"angle brackets", "Name with <brackets>"},
		{"nested script", "<<script>>alert(1)<</script>>"},
		{"event handler", `<div onmouseover="alert(1)">`},
		{"href javascript", `<a href="javascript:alert(1)">`},
	}
	for _, tc := range xssPayloads {
		t.Run(tc.name, func(t *testing.T) {
			org := &Organization{Name: tc.payload, Slug: "test-slug"}
			if err := org.Validate(); err == nil {
				t.Errorf("XSS payload %q should be rejected", tc.payload)
			}
		})
	}
}

func TestOrganization_Validate_SafeNames(t *testing.T) {
	safeNames := []string{
		"Acme Corp",
		"DataFlow Labs",
		"My Company 123",
		"Nordic Health Systems",
		"test-org",
		"Org with (parentheses)",
		"Org 2.0",
		"Multi Word Name Here",
	}
	for _, name := range safeNames {
		t.Run(name, func(t *testing.T) {
			org := &Organization{Name: name, Slug: "test"}
			if err := org.Validate(); err != nil {
				t.Errorf("safe name %q should be accepted but got: %v", name, err)
			}
		})
	}
}

func TestOrganization_Validate_EmptyName(t *testing.T) {
	org := &Organization{Name: "", Slug: "test"}
	if err := org.Validate(); err == nil {
		t.Error("empty name should be rejected")
	}
}

func TestOrganization_Validate_SlugFormat(t *testing.T) {
	tests := []struct {
		slug string
		ok   bool
	}{
		{"acme", true},
		{"my-org", true},
		{"org123", true},
		{"a", false},                                                     // too short
		{"UPPERCASE", false},                                             // no uppercase
		{"has_underscore", false},                                        // no underscores
		{"has space", false},                                             // no spaces
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false}, // > 50 chars (52)
	}
	for _, tc := range tests {
		t.Run(tc.slug, func(t *testing.T) {
			org := &Organization{Name: "Valid Name", Slug: tc.slug}
			err := org.Validate()
			if tc.ok && err != nil {
				t.Errorf("slug %q should be valid but got: %v", tc.slug, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("slug %q should be invalid", tc.slug)
			}
		})
	}
}

func TestOrganization_Validate_AccountStatus(t *testing.T) {
	validStatuses := []string{"trial", "active", "suspended", "churned", ""}
	for _, status := range validStatuses {
		org := &Organization{Name: "Test Org", Slug: "test", AccountStatus: status}
		if err := org.Validate(); err != nil {
			t.Errorf("account status %q should be valid but got: %v", status, err)
		}
	}

	org := &Organization{Name: "Test Org", Slug: "test", AccountStatus: "invalid"}
	if err := org.Validate(); err == nil {
		t.Error("invalid account status should be rejected")
	}
}

// ─── Organization.Sanitize ──────────────────────────────────────────────────

func TestOrganization_Sanitize(t *testing.T) {
	org := Organization{
		Name:            "Test Org",
		Notes:           "Internal ops note: customer called about billing",
		SuspendedReason: "Non-payment",
	}
	sanitized := org.Sanitize()
	if sanitized.Notes != "" {
		t.Error("Notes should be cleared after sanitize")
	}
	if sanitized.SuspendedReason != "" {
		t.Error("SuspendedReason should be cleared after sanitize")
	}
	if sanitized.Name != "Test Org" {
		t.Error("Name should be preserved after sanitize")
	}
}

// ─── Pipeline.Validate XSS prevention ───────────────────────────────────────

func TestPipeline_Validate_XSS(t *testing.T) {
	xssPayloads := []struct {
		name    string
		payload string
	}{
		{"script tag", "<script>alert(1)</script>"},
		{"img onerror", "Pipeline <img onerror=alert(1)>"},
		{"double quotes", `Name "with" quotes`},
		{"single quotes", "Name 'with' quotes"},
		{"ampersand", "Pipeline & Stuff"},
		{"mixed html", "<b>bold</b> pipeline"},
	}
	for _, tc := range xssPayloads {
		t.Run(tc.name, func(t *testing.T) {
			p := &Pipeline{Name: tc.payload}
			if err := p.Validate(); err == nil {
				t.Errorf("XSS payload %q should be rejected in pipeline name", tc.payload)
			}
		})
	}
}

func TestPipeline_Validate_SafeNames(t *testing.T) {
	safeNames := []string{
		"My ETL Pipeline",
		"orders-daily-sync",
		"Pipeline v2.1",
		"Customer Data (prod)",
		"Load: users table",
	}
	for _, name := range safeNames {
		t.Run(name, func(t *testing.T) {
			p := &Pipeline{Name: name}
			if err := p.Validate(); err != nil {
				t.Errorf("safe pipeline name %q should be accepted but got: %v", name, err)
			}
		})
	}
}

func TestPipeline_Validate_EmptyName(t *testing.T) {
	p := &Pipeline{Name: ""}
	if err := p.Validate(); err == nil {
		t.Error("empty pipeline name should be rejected")
	}
}

func TestPipeline_Validate_NameTooLong(t *testing.T) {
	longName := ""
	for i := 0; i < 256; i++ {
		longName += "a"
	}
	p := &Pipeline{Name: longName}
	if err := p.Validate(); err == nil {
		t.Error("pipeline name over 255 chars should be rejected")
	}
}

func TestPipeline_Validate_DescriptionTooLong(t *testing.T) {
	longDesc := ""
	for i := 0; i < 2001; i++ {
		longDesc += "a"
	}
	p := &Pipeline{Name: "Valid Name", Description: longDesc}
	if err := p.Validate(); err == nil {
		t.Error("description over 2000 chars should be rejected")
	}
}

func TestPipeline_Validate_TooManyNodes(t *testing.T) {
	nodes := make([]Node, 501)
	for i := range nodes {
		nodes[i] = Node{ID: "node-" + string(rune('a'+i%26)) + string(rune('0'+i/26))}
	}
	p := &Pipeline{Name: "Valid Name", Nodes: nodes}
	if err := p.Validate(); err == nil {
		t.Error("pipeline with >500 nodes should be rejected")
	}
}

func TestPipeline_Validate_DuplicateNodeIDs(t *testing.T) {
	p := &Pipeline{
		Name: "Test",
		Nodes: []Node{
			{ID: "node-1", Type: NodeTypeTransform},
			{ID: "node-1", Type: NodeTypeTransform},
		},
	}
	if err := p.Validate(); err == nil {
		t.Error("duplicate node IDs should be rejected")
	}
}

func TestPipeline_Validate_InvalidEdgeRefs(t *testing.T) {
	p := &Pipeline{
		Name: "Test",
		Nodes: []Node{
			{ID: "node-1", Type: NodeTypeTransform},
		},
		Edges: []Edge{
			{From: "node-1", To: "nonexistent"},
		},
	}
	if err := p.Validate(); err == nil {
		t.Error("edge referencing nonexistent node should be rejected")
	}
}

func TestPipeline_Validate_InvalidSLATimezone(t *testing.T) {
	p := &Pipeline{
		Name:        "Test",
		SLATimezone: "Invalid/Timezone",
	}
	if err := p.Validate(); err == nil {
		t.Error("invalid timezone should be rejected")
	}
}

func TestPipeline_Validate_ValidSLATimezone(t *testing.T) {
	p := &Pipeline{
		Name:        "Test",
		SLATimezone: "America/New_York",
	}
	if err := p.Validate(); err != nil {
		t.Errorf("valid timezone should be accepted: %v", err)
	}
}

func TestPipeline_Validate_InvalidSLADeadline(t *testing.T) {
	p := &Pipeline{
		Name:        "Test",
		SLADeadline: "invalid",
	}
	if err := p.Validate(); err == nil {
		t.Error("invalid SLA deadline format should be rejected")
	}
}

// ─── Role permissions ───────────────────────────────────────────────────────

func TestRole_HasPermission(t *testing.T) {
	roles := DefaultRoles()

	// Admin should have all permissions
	var adminRole *Role
	for i := range roles {
		if roles[i].ID == "admin" {
			adminRole = &roles[i]
			break
		}
	}
	if adminRole == nil {
		t.Fatal("admin role not found")
	}
	for _, perm := range AllPermissions() {
		if !adminRole.HasPermission(perm) {
			t.Errorf("admin should have permission %q", perm)
		}
	}

	// Viewer should only have read permissions
	var viewerRole *Role
	for i := range roles {
		if roles[i].ID == "viewer" {
			viewerRole = &roles[i]
			break
		}
	}
	if viewerRole == nil {
		t.Fatal("viewer role not found")
	}
	writePerms := []Permission{
		PermPipelinesCreate, PermPipelinesEdit, PermPipelinesDelete, PermPipelinesRun,
		PermConnectionsCreate, PermConnectionsEdit, PermConnectionsDelete,
		PermVariablesCreate, PermVariablesEdit, PermVariablesDelete,
		PermSettingsEdit, PermSettingsManageUsers, PermSettingsManageRoles,
	}
	for _, perm := range writePerms {
		if viewerRole.HasPermission(perm) {
			t.Errorf("viewer should NOT have write permission %q", perm)
		}
	}
}
