package engine

// Reading a task's dataset output (ADR-033 phase 5a). These are the
// worker's defenses against a candidate result manifest describing
// something other than what is really on disk -- the one place in this
// rollout where the worker consumes a file the sandbox wrote, so the
// negative cases matter as much as the happy path.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stageNDJSON writes content into a fresh staging dir and returns the
// dir plus the size and checksum a truthful manifest would declare.
func stageNDJSON(t *testing.T, name, content string) (stagingDir string, size int64, checksum string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	return dir, int64(len(content)), "sha256:" + hex.EncodeToString(sum[:])
}

func TestReadTaskDatasetOutput_DecodesRowsAndUnionsColumns(t *testing.T) {
	// The second row carries a key the first omits: NDJSON rows are
	// independent objects, so the column list is their union, sorted.
	content := `{"b":2,"a":1}` + "\n" + `{"a":3,"c":4}` + "\n"
	dir, size, checksum := stageNDJSON(t, "result.ndjson", content)

	ds, err := readTaskDatasetOutput(dir, "result.ndjson", CodecNDJSON, size, checksum)
	if err != nil {
		t.Fatalf("readTaskDatasetOutput: %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Fatalf("rows = %v, want 2", ds.Rows)
	}
	if strings.Join(ds.Columns, ",") != "a,b,c" {
		t.Errorf("columns = %v, want the sorted union [a b c]", ds.Columns)
	}
	if ds.Rows[0]["a"] != float64(1) || ds.Rows[1]["c"] != float64(4) {
		t.Errorf("rows decoded wrong: %v", ds.Rows)
	}
}

func TestReadTaskDatasetOutput_BlankLinesAreSkipped(t *testing.T) {
	content := `{"a":1}` + "\n\n" + `{"a":2}` + "\n"
	dir, size, checksum := stageNDJSON(t, "result.ndjson", content)
	ds, err := readTaskDatasetOutput(dir, "result.ndjson", CodecNDJSON, size, checksum)
	if err != nil {
		t.Fatalf("readTaskDatasetOutput: %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Errorf("rows = %v, want 2 (a blank line is not a row)", ds.Rows)
	}
}

// The manifest is the task's own claim about bytes the task itself
// wrote. A mismatch means the claim is wrong, and accepting it would
// defeat the point of declaring a checksum at all.
func TestReadTaskDatasetOutput_ChecksumMismatchIsRefused(t *testing.T) {
	dir, size, _ := stageNDJSON(t, "result.ndjson", `{"a":1}`+"\n")
	wrong := "sha256:" + strings.Repeat("0", 64)
	_, err := readTaskDatasetOutput(dir, "result.ndjson", CodecNDJSON, size, wrong)
	if err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("err = %v, want an integrity failure", err)
	}
}

func TestReadTaskDatasetOutput_SizeMismatchIsRefused(t *testing.T) {
	dir, size, checksum := stageNDJSON(t, "result.ndjson", `{"a":1}`+"\n")
	_, err := readTaskDatasetOutput(dir, "result.ndjson", CodecNDJSON, size+1, checksum)
	if err == nil || !strings.Contains(err.Error(), "declares") {
		t.Fatalf("err = %v, want a size mismatch failure", err)
	}
}

// ADR-033 section 7 rule 6's beneath semantics: a path climbing out of
// the staging dir must not resolve, even though the file it names is
// perfectly readable by this process.
func TestReadTaskDatasetOutput_PathTraversalIsRefused(t *testing.T) {
	dir, size, checksum := stageNDJSON(t, "result.ndjson", `{"a":1}`+"\n")
	outside := filepath.Join(filepath.Dir(dir), "outside.ndjson")
	if err := os.WriteFile(outside, []byte(`{"a":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readTaskDatasetOutput(dir, "../outside.ndjson", CodecNDJSON, size, checksum); err == nil {
		t.Fatal("a path escaping the staging dir was accepted")
	}
}

// The same rule's no-follow half: a symlink inside the staging dir
// pointing out of it is the classic way to make a worker read a file the
// sandbox could never have written.
func TestReadTaskDatasetOutput_SymlinkEscapeIsRefused(t *testing.T) {
	dir, size, checksum := stageNDJSON(t, "unused.ndjson", `{"a":1}`+"\n")
	secret := filepath.Join(t.TempDir(), "secret.ndjson")
	if err := os.WriteFile(secret, []byte(`{"a":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "result.ndjson")); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	if _, err := readTaskDatasetOutput(dir, "result.ndjson", CodecNDJSON, size, checksum); err == nil {
		t.Fatal("a symlink out of the staging dir was followed")
	}
}

func TestReadTaskDatasetOutput_NonRegularFileIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "result.ndjson"), 0o750); err != nil {
		t.Fatal(err)
	}
	_, err := readTaskDatasetOutput(dir, "result.ndjson", CodecNDJSON, 0, "sha256:"+strings.Repeat("0", 64))
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("err = %v, want a not-a-regular-file failure", err)
	}
}

// Arrow IPC is ADR-033 section 8's preferred optional transport, but
// this server does not read it yet -- an unknown codec must be named,
// never guessed at as if it were the baseline.
func TestReadTaskDatasetOutput_UnknownCodecIsRefusedByName(t *testing.T) {
	dir, size, checksum := stageNDJSON(t, "result.ndjson", `{"a":1}`+"\n")
	_, err := readTaskDatasetOutput(dir, "result.ndjson", "arrow-ipc/v1", size, checksum)
	if err == nil || !strings.Contains(err.Error(), "arrow-ipc/v1") {
		t.Fatalf("err = %v, want the unsupported codec named", err)
	}
}

func TestReadTaskDatasetOutput_NonObjectLineNamesTheLine(t *testing.T) {
	content := `{"a":1}` + "\n" + `"just a string"` + "\n"
	dir, size, checksum := stageNDJSON(t, "result.ndjson", content)
	_, err := readTaskDatasetOutput(dir, "result.ndjson", CodecNDJSON, size, checksum)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v, want the offending line number named", err)
	}
}
