package cmd

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/pkg/drivers"
)

// A worker refuses to start on a managed set it cannot read, and does
// nothing when no set is declared.
func TestEnsureManagedDriversAtWorkerStartup(t *testing.T) {
	t.Setenv("BROKOLI_DRIVER_DIR", t.TempDir())
	t.Setenv(drivers.ManagedEnvVar, "")
	if err := ensureManagedDrivers(context.Background()); err != nil {
		t.Fatalf("no set declared: %v", err)
	}
	t.Setenv(drivers.ManagedEnvVar, "flightsql@1.12.0")
	if err := ensureManagedDrivers(context.Background()); err == nil || !strings.Contains(err.Error(), drivers.ManagedEnvVar) {
		t.Fatalf("unparseable set: err = %v, want the worker stopped naming %s", err, drivers.ManagedEnvVar)
	}
}

// A build that cannot load native drivers installs none: it would fetch
// libraries it can never run.
func TestEnsureManagedDriversSkipsABuildThatCannotLoadThem(t *testing.T) {
	if engine.NativeADBCWorkerEnabled() {
		t.Skip("this build can load native drivers")
	}
	dir := t.TempDir()
	t.Setenv("BROKOLI_DRIVER_DIR", dir)
	t.Setenv(drivers.IndexEnvVar, "http://127.0.0.1:1/index.json") // would fail if fetched
	t.Setenv(drivers.ManagedEnvVar, `[{"name":"flightsql","version":"1.12.0","library_sha256":"2c9ead4e46ce85223d152bd1b9aae33823c1de5bd20a5b2a888b4b75024d2d4e"}]`)
	if err := ensureManagedDrivers(context.Background()); err != nil {
		t.Fatalf("ensureManagedDrivers: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("installed into %s on a build that cannot load drivers: %v", dir, entries)
	}
}
