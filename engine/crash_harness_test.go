package engine

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// TestCrashRecoveryBaseline records the persisted state after the scheduler
// process dies at important execution boundaries. These are baseline assertions:
// recovery is deliberately left to the follow-up durability work.
func TestCrashRecoveryBaseline(t *testing.T) {
	cases := []struct {
		name           string
		point          string
		runStatus      models.RunStatus
		nodeStatuses   map[string]models.RunStatus
		outputExpected bool
	}{
		{name: "before run creation", point: crashPointBeforeRunCreate},
		{name: "after run creation", point: crashPointAfterRunCreate, runStatus: models.RunStatusRunning},
		{name: "before node attempt creation", point: crashPointBeforeNodeAttemptCreate + ":source", runStatus: models.RunStatusRunning},
		{name: "after node attempt creation", point: crashPointAfterNodeAttemptCreate + ":source", runStatus: models.RunStatusRunning, nodeStatuses: map[string]models.RunStatus{"source": models.RunStatusRunning}},
		{name: "before node execution", point: crashPointBeforeNodeExecution + ":source", runStatus: models.RunStatusRunning, nodeStatuses: map[string]models.RunStatus{"source": models.RunStatusRunning}},
		{name: "after sink side effect before persistence", point: crashPointAfterNodeExecutionBeforePersist + ":sink", runStatus: models.RunStatusRunning, nodeStatuses: map[string]models.RunStatus{"source": models.RunStatusSuccess, "condition": models.RunStatusSuccess, "sink": models.RunStatusRunning}, outputExpected: true},
		{name: "after sink completion persistence", point: crashPointAfterNodeCompletionPersist + ":sink", runStatus: models.RunStatusRunning, nodeStatuses: map[string]models.RunStatus{"source": models.RunStatusSuccess, "condition": models.RunStatusSuccess, "sink": models.RunStatusSuccess}, outputExpected: true},
		{name: "before terminal persistence", point: crashPointBeforeRunTerminalPersist, runStatus: models.RunStatusRunning, nodeStatuses: map[string]models.RunStatus{"source": models.RunStatusSuccess, "condition": models.RunStatusSuccess, "sink": models.RunStatusSuccess}, outputExpected: true},
		{name: "after terminal persistence", point: crashPointAfterRunTerminalPersist, runStatus: models.RunStatusSuccess, nodeStatuses: map[string]models.RunStatus{"source": models.RunStatusSuccess, "condition": models.RunStatusSuccess, "sink": models.RunStatusSuccess}, outputExpected: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "crash-harness.db")
			inputPath := filepath.Join(dir, "input.csv")
			outputPath := filepath.Join(dir, "output.csv")
			if err := os.WriteFile(inputPath, []byte("id,name\n1,brokoli\n"), 0o600); err != nil {
				t.Fatalf("write input: %v", err)
			}

			cmd := exec.Command(os.Args[0], "-test.run=^TestCrashRecoveryBaselineProcess$")
			cmd.Env = append(os.Environ(),
				"BROKOLI_ENABLE_TEST_FAILPOINTS=1",
				"BROKOLI_TEST_CRASH_POINT="+tc.point,
				"BROKOLI_CRASH_HARNESS_DB="+dbPath,
				"BROKOLI_CRASH_HARNESS_INPUT="+inputPath,
				"BROKOLI_CRASH_HARNESS_OUTPUT="+outputPath,
			)
			err := cmd.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != testCrashExitCode {
				t.Fatalf("crash subprocess error = %v, want exit code %d", err, testCrashExitCode)
			}

			assertCrashBaseline(t, dbPath, tc.runStatus, tc.nodeStatuses, outputPath, tc.outputExpected)
		})
	}
}

// TestCrashRecoveryBaselineProcess runs only in the subprocess spawned above.
func TestCrashRecoveryBaselineProcess(t *testing.T) {
	if os.Getenv("BROKOLI_ENABLE_TEST_FAILPOINTS") != "1" {
		return
	}

	dbPath := os.Getenv("BROKOLI_CRASH_HARNESS_DB")
	inputPath := os.Getenv("BROKOLI_CRASH_HARNESS_INPUT")
	outputPath := os.Getenv("BROKOLI_CRASH_HARNESS_OUTPUT")
	if dbPath == "" || inputPath == "" || outputPath == "" {
		t.Fatal("crash harness paths must be configured")
	}

	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	pipeline := &models.Pipeline{
		ID: "crash-harness", Name: "Crash Harness", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": inputPath}},
			{ID: "condition", Type: models.NodeTypeCondition, Name: "Condition", Config: map[string]interface{}{}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": outputPath, "format": "csv"}},
		},
		Edges: []models.Edge{{From: "source", To: "condition"}, {From: "condition", To: "sink"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	if _, err := NewEngine(s).RunPipeline(pipeline.ID); err != nil {
		t.Fatalf("run pipeline before injected crash: %v", err)
	}
	t.Fatal("crash point was not reached")
}

func assertCrashBaseline(t *testing.T, dbPath string, expectedRun models.RunStatus, expectedNodes map[string]models.RunStatus, outputPath string, outputExpected bool) {
	t.Helper()

	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store after crash: %v", err)
	}
	defer s.Close()

	runs, err := s.ListRunsByPipeline("crash-harness", 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if expectedRun == "" {
		if len(runs) != 0 {
			t.Fatalf("runs after pre-create crash = %d, want 0", len(runs))
		}
	} else {
		if len(runs) != 1 {
			t.Fatalf("runs after crash = %d, want 1", len(runs))
		}
		if runs[0].Status != expectedRun {
			t.Errorf("run status = %q, want %q", runs[0].Status, expectedRun)
		}

		nodeRuns, err := s.ListNodeRunsByRun(runs[0].ID)
		if err != nil {
			t.Fatalf("list node runs: %v", err)
		}
		var got map[string]models.RunStatus
		if len(nodeRuns) > 0 {
			got = make(map[string]models.RunStatus, len(nodeRuns))
			for _, nodeRun := range nodeRuns {
				got[nodeRun.NodeID] = nodeRun.Status
			}
		}
		if !reflect.DeepEqual(got, expectedNodes) {
			t.Errorf("node states = %v, want %v", got, expectedNodes)
		}
	}

	_, err = os.Stat(outputPath)
	if outputExpected && err != nil {
		t.Errorf("output missing after sink side effect: %v", err)
	}
	if !outputExpected && !errors.Is(err, os.ErrNotExist) {
		t.Errorf("output exists before sink side effect")
	}
}
