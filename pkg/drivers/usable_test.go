package drivers

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// Orkidea serves Oracle connections and nothing else, and Oracle becomes a
// type that can be pinned to a native driver.
func TestOrkideaServesOracle(t *testing.T) {
	if !Serves("orkidea", models.ConnTypeOracle) {
		t.Fatal("orkidea does not serve oracle connections")
	}
	if got := UsableBy("orkidea"); len(got) != 1 || got[0] != models.ConnTypeOracle {
		t.Fatalf("UsableBy(orkidea) = %v, want [oracle]", got)
	}
	if Serves("postgresql", models.ConnTypeOracle) {
		t.Fatal("the postgresql driver claims oracle connections")
	}
	found := false
	for _, k := range NativeConnectionTypes() {
		found = found || k == models.ConnTypeOracle
	}
	if !found {
		t.Fatalf("NativeConnectionTypes() = %v, want it to include oracle", NativeConnectionTypes())
	}
}
