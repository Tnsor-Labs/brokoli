package engine

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// withDeployment sets the two facts that decide whether file nodes are
// safe, and restores them afterwards.
func withDeployment(t *testing.T, distributed, shared bool) {
	t.Helper()
	prev := distributedWorkers.Load()
	distributedWorkers.Store(distributed)
	t.Cleanup(func() { distributedWorkers.Store(prev) })
	if shared {
		t.Setenv("BROKOLI_DATA_DIRS_SHARED", "1")
	} else {
		t.Setenv("BROKOLI_DATA_DIRS_SHARED", "")
	}
}

// The hazard is exactly one combination. A single-worker deployment is
// safe however its storage is arranged, and shared storage is safe however
// many workers there are.
func TestUnsharedFileStorageOnlyFlagsTheRealHazard(t *testing.T) {
	cases := []struct {
		distributed, shared, hazard bool
	}{
		{false, false, false}, // one process: unambiguous
		{false, true, false},
		{true, true, false}, // several workers, one filesystem
		{true, false, true}, // several workers, several filesystems
	}
	for _, c := range cases {
		withDeployment(t, c.distributed, c.shared)
		if got := unsharedFileStorage(); got != c.hazard {
			t.Errorf("distributed=%v shared=%v: hazard=%v, want %v", c.distributed, c.shared, got, c.hazard)
		}
		warned := FileStorageWarning() != ""
		if warned != c.hazard {
			t.Errorf("distributed=%v shared=%v: warned=%v, want %v", c.distributed, c.shared, warned, c.hazard)
		}
	}
}

// The declaration is a boolean, and anything that is not clearly "yes"
// means no — an unparseable value must not be read as an assurance.
func TestSharedStorageMustBeDeclaredClearly(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "t"} {
		t.Setenv("BROKOLI_DATA_DIRS_SHARED", v)
		if !fileStorageShared() {
			t.Errorf("%q should declare shared storage", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "maybe", "yes"} {
		t.Setenv("BROKOLI_DATA_DIRS_SHARED", v)
		if fileStorageShared() {
			t.Errorf("%q must not be read as an assurance of shared storage", v)
		}
	}
}

// A missing file gains the explanation only when the deployment makes it
// the likely cause. Everywhere else the plain error is the whole story.
func TestMissingFileDiagnosis(t *testing.T) {
	notExist := fmt.Errorf("load /data/x.csv: %w", &os.PathError{Op: "open", Path: "/data/x.csv", Err: os.ErrNotExist})

	withDeployment(t, true, false)
	got := describeMissingFile("/data/x.csv", notExist)
	for _, want := range []string{"own filesystem", "BROKOLI_DATA_DIRS_SHARED", "/data/x.csv"} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("diagnosis should mention %q: %v", want, got)
		}
	}
	if !errors.Is(got, os.ErrNotExist) {
		t.Error("the original error must still be unwrappable")
	}

	withDeployment(t, true, true)
	if describeMissingFile("/data/x.csv", notExist).Error() != notExist.Error() {
		t.Error("shared storage: nothing should be added")
	}
	withDeployment(t, false, false)
	if describeMissingFile("/data/x.csv", notExist).Error() != notExist.Error() {
		t.Error("single worker: nothing should be added")
	}

	// A different failure is not this problem.
	withDeployment(t, true, false)
	other := errors.New("csv: field count mismatch on line 3")
	if describeMissingFile("/data/x.csv", other).Error() != other.Error() {
		t.Error("a parse error must not be explained as a storage problem")
	}
}

func sourceFileNode(id, path string) models.Node {
	return models.Node{ID: id, Name: id, Type: models.NodeTypeSourceFile,
		Config: map[string]interface{}{"path": path}}
}

func sinkFileNode(id, path string) models.Node {
	return models.Node{ID: id, Name: id, Type: models.NodeTypeSinkFile,
		Config: map[string]interface{}{"path": path}}
}

func fileStorageWarningsFor(nodes []models.Node) map[string]string {
	out := map[string]string{}
	for _, r := range ValidateNodes(nodes) {
		for _, w := range r.Warnings {
			if strings.Contains(w, "own filesystem") {
				out[r.NodeID] = w
			}
		}
	}
	return out
}

// A pipeline that writes a file and reads it back within the same run is
// safe — every node of a run executes on one worker. Only a dependency on
// a file from outside the run is a coin flip.
func TestValidationWarnsOnlyOnCrossRunFileDependencies(t *testing.T) {
	withDeployment(t, true, false)

	selfContained := []models.Node{
		sinkFileNode("write", "/data/mid.csv"),
		sourceFileNode("read", "/data/mid.csv"),
	}
	if w := fileStorageWarningsFor(selfContained); len(w) != 0 {
		t.Errorf("a file produced within the run should not warn: %v", w)
	}

	crossRun := []models.Node{sourceFileNode("read", "/data/from-yesterday.csv")}
	w := fileStorageWarningsFor(crossRun)
	if len(w) != 1 {
		t.Fatalf("a file from outside the run should warn, got %v", w)
	}
	if !strings.Contains(w["read"], "stale") {
		t.Errorf("the warning should name the silent failure, not just the loud one: %q", w["read"])
	}

	// Sinks are not warned about here: writing is only a problem because
	// of a later read, and that read is where it can be acted on.
	if _, ok := fileStorageWarningsFor([]models.Node{sinkFileNode("write", "/data/out.csv")})["write"]; ok {
		t.Error("a sink alone should not warn")
	}
}

// A correctly configured deployment must be completely silent, or the
// warning becomes noise people learn to ignore.
func TestNoWarningsWhenDeploymentIsSound(t *testing.T) {
	nodes := []models.Node{sourceFileNode("read", "/data/from-yesterday.csv")}

	for _, c := range []struct{ distributed, shared bool }{
		{false, false}, {false, true}, {true, true},
	} {
		withDeployment(t, c.distributed, c.shared)
		if w := fileStorageWarningsFor(nodes); len(w) != 0 {
			t.Errorf("distributed=%v shared=%v should be silent, got %v", c.distributed, c.shared, w)
		}
	}
}
