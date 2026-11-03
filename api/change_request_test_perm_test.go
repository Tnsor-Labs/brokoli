package api

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/golang-jwt/jwt/v5"
)

// Testing a change request runs its proposed code on the server, so it
// needs edit permission, not only run. An operator may propose a change
// and run pipelines, but not edit them: with run alone they could propose
// any code and then run it by testing it.
func TestOperatorCannotTestAChangeRequest(t *testing.T) {
	b := newAgentBed(t)
	// The operator role as a distribution resolves it: propose and run,
	// not edit. The open-source fallback has no such role.
	found := false
	for _, role := range models.DefaultRoles() {
		if role.ID == "operator" {
			if slices.Contains(role.Permissions, models.PermPipelinesEdit) || !slices.Contains(role.Permissions, models.PermPipelinesPropose) {
				t.Fatalf("operator role = %v; this test assumes propose without edit", role.Permissions)
			}
			found = true
			previous := RequestIdentityFunc
			RequestIdentityFunc = func(_ *http.Request, claims *jwt.MapClaims) IdentityRefinement {
				if name, _ := (*claims)["username"].(string); name == "ops" {
					return IdentityRefinement{Permissions: role.Permissions}
				}
				return IdentityRefinement{}
			}
			t.Cleanup(func() { RequestIdentityFunc = previous })
		}
	}
	if !found {
		t.Fatal("no built-in operator role")
	}
	u, err := b.users.CreateUser("ops", "Str0ng!Passw0rd", RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := GenerateToken(u)
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(t.TempDir(), "in.csv")
	if err := os.WriteFile(in, []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.seedPipeline(&models.Pipeline{
		ID: "p-op", Name: "op", Enabled: true, WorkspaceID: models.DefaultWorkspaceID,
		Nodes: []models.Node{
			{ID: "src", Type: "source_file", Name: "In", Config: map[string]interface{}{"path": in, "format": "csv"}},
			{ID: "tf", Type: "transform", Name: "T", Config: map[string]interface{}{"rules": []interface{}{}}},
		},
		Edges: []models.Edge{{From: "src", To: "tf"}},
	})
	id := b.pipelineID("op")
	def := b.edited(id, func(p *models.Pipeline) { p.Description = "changed" })
	code, cr, raw := b.propose(ops, id, def, "Change")
	if code != http.StatusCreated {
		t.Fatalf("an operator proposes: %d %s", code, raw)
	}
	path := "/api/pipelines/" + id + "/changes/" + cr["id"].(string) + "/test"
	if code, _, raw := b.do(http.MethodPost, path, ops, ""); code != http.StatusForbidden {
		t.Fatalf("an operator tested a change: %d %s", code, raw)
	}
	if code, _, raw := b.do(http.MethodPost, path, b.tokens["eve"], ""); code != http.StatusOK {
		t.Fatalf("an editor tests a change: %d %s", code, raw)
	}
}
