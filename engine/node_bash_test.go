package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func TestRunBashPassesInputAndUsesConfiguredEnvironment(t *testing.T) {
	workDir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", workDir)
	input := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": 1}}}
	node := models.Node{ID: "bash", Config: map[string]interface{}{
		"command":     `test "$TEST_VALUE" = "ok"; printf 'stdout\n'; printf 'stderr\n' >&2`,
		"working_dir": workDir,
		"env":         map[string]interface{}{"TEST_VALUE": "ok"},
	}}

	output, err := (&Runner{}).runBash(context.Background(), node, input)
	if err != nil {
		t.Fatalf("runBash: %v", err)
	}
	if output != input {
		t.Fatal("bash operator did not pass the input dataset through unchanged")
	}
}

func TestRunBashFailsCommandAndHonorsCancellation(t *testing.T) {
	workDir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", workDir)
	node := models.Node{ID: "bash", Config: map[string]interface{}{"command": "exit 7", "working_dir": workDir}}
	if _, err := (&Runner{}).runBash(context.Background(), node, nil); err == nil || !strings.Contains(err.Error(), "bash command failed") {
		t.Fatalf("runBash error = %v, want command failure", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	node.Config["command"] = "sleep 10"
	if _, err := (&Runner{}).runBash(ctx, node, nil); err == nil {
		t.Fatal("runBash succeeded after context cancellation")
	}
}

func TestBashConfigErrors(t *testing.T) {
	if got := bashConfigErrors(map[string]interface{}{}); len(got) != 1 || got[0] != "'command' is required" {
		t.Fatalf("bashConfigErrors(empty) = %v", got)
	}
	if got := bashConfigErrors(map[string]interface{}{"command": "true", "env": "bad"}); len(got) != 1 || got[0] != "'env' must be an object of string values" {
		t.Fatalf("bashConfigErrors(env) = %v", got)
	}
}
