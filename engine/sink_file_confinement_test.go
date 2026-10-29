package engine

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// A symlink planted in a data directory must not redirect a sink's write
// out of it. The path check alone read the path as written and passed it.
func TestSinkFileDoesNotWriteThroughAnEscapingSymlink(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	if err := os.Symlink(outside, filepath.Join(dir, "exports")); err != nil {
		t.Fatal(err)
	}
	node := models.Node{ID: "sink", Type: models.NodeTypeSinkFile}
	write := func(w io.Writer) error {
		_, err := io.WriteString(w, "id\n1\n")
		return err
	}
	r := &Runner{}

	for _, path := range []string{
		filepath.Join(dir, "exports", "out.csv"),
		filepath.Join(dir, "exports", "nested", "out.csv"),
	} {
		if _, err := r.writeFileOutput(context.Background(), node, path, write); err == nil {
			t.Errorf("writing %s succeeded through a link out of the data directory", path)
		}
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the sink created %d entries outside the data directory", len(entries))
	}

	// The ordinary case still writes.
	path := filepath.Join(dir, "daily", "out.csv")
	if _, err := r.writeFileOutput(context.Background(), node, path, write); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "id\n1\n" { //nolint:gosec // test temp file
		t.Errorf("read back %q, %v", got, err)
	}
}
