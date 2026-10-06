package api

import (
	"os"
	"testing"
)

// TestMain keeps every test in this package away from the developer's real
// native driver directory and catalog. Driver routes act on
// drivers.DefaultDir(), and a test that drives DELETE /api/drivers/{name}
// through the router as an admin once removed a real installed driver
// build from ~/.brokoli/drivers. A test that wants a driver directory sets
// its own with t.Setenv, which overrides this one.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "brokoli-api-test-drivers-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("BROKOLI_DRIVER_DIR", dir)
	_ = os.Setenv("BROKOLI_DRIVER_INDEX", "")
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
