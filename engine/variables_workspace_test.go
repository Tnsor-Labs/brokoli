package engine

import (
	"testing"
	"time"
)

// ${var.X} resolved by key alone, so a pipeline in one workspace read
// whichever workspace had written that name last. For a secret variable
// that is one tenant's pipeline resolving another tenant's secret.

type wsVarStore map[string]string // "workspace\x00key" -> value

func (m wsVarStore) GetVariableValue(workspaceID, key string) (string, bool, error) {
	v, ok := m[workspaceID+"\x00"+key]
	if !ok {
		return "", false, errNoVar
	}
	return v, false, nil
}

var errNoVar = &noVarError{}

type noVarError struct{}

func (*noVarError) Error() string { return "no such variable" }

func TestVarResolutionIsScopedToThePipelinesWorkspace(t *testing.T) {
	vars := wsVarStore{
		"tenant-a\x00api_token": "a-secret",
		"tenant-b\x00api_token": "b-secret",
	}

	vc := NewVariableContext(nil, "run-1", time.Now())
	vc.Vars = vars
	vc.WorkspaceID = "tenant-b"

	if got := vc.Resolve("${var.api_token}"); got != "b-secret" {
		t.Errorf("got %q, want tenant-b's own value", got)
	}
}

func TestVarResolutionCannotReachAnotherWorkspace(t *testing.T) {
	vars := wsVarStore{"tenant-a\x00only_in_a": "a-secret"}

	vc := NewVariableContext(nil, "run-1", time.Now())
	vc.Vars = vars
	vc.WorkspaceID = "tenant-b"

	got := vc.Resolve("${var.only_in_a}")
	if got == "a-secret" {
		t.Fatal("a pipeline resolved another workspace's variable")
	}
	if got != "" {
		t.Errorf("got %q, want empty for a variable this workspace does not have", got)
	}
}
