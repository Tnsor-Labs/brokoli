// Package modulepaths holds a repository check: every tracked file must
// have a path Go accepts in a module zip.
package modulepaths

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/mod/module"
)

// Go refuses to build a module zip that holds a file whose path it does
// not accept, and then nothing can depend on that commit: `go get` of
// main failed on "store/?_pragma=..." after a test of a broken guard
// created the file and it was committed with the change. This fails the
// commit that adds such a path instead of the next consumer.
func TestEveryTrackedFileHasAValidModulePath(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("not a git checkout, nothing to check: %v", err)
	}
	checked := 0
	for _, path := range bytes.Split(out, []byte{0}) {
		if len(path) == 0 {
			continue
		}
		checked++
		if err := module.CheckFilePath(string(path)); err != nil {
			t.Errorf("tracked file %q cannot be in a Go module zip: %v", path, err)
		}
	}
	// An empty listing would pass vacuously.
	if checked < 100 {
		t.Fatalf("git ls-files listed %d files under %s; expected the whole repository", checked, root)
	}
}
