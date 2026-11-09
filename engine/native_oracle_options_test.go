package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// The native Oracle driver reads an unconstrained NUMBER as float64 unless
// told otherwise, rounding past about 15 digits. Brokoli asks for the exact
// text instead, and a connection that sets the option keeps its choice.
func TestNativeOracleReadsUnconstrainedNumbersExactly(t *testing.T) {
	conn := &models.Connection{Type: models.ConnTypeOracle}
	if got := nativeADBCOptions(conn, nil)["oracle.unconstrained_number"]; got != "string" {
		t.Fatalf("default oracle.unconstrained_number = %q, want string", got)
	}
	extra := map[string]interface{}{"adbc_options": map[string]interface{}{"oracle.unconstrained_number": "float64"}}
	if got := nativeADBCOptions(conn, extra)["oracle.unconstrained_number"]; got != "float64" {
		t.Fatalf("connection's own choice replaced: got %q, want float64", got)
	}
	other := &models.Connection{Type: models.ConnTypePostgres}
	if _, set := nativeADBCOptions(other, nil)["oracle.unconstrained_number"]; set {
		t.Fatal("an Oracle option was given to a PostgreSQL driver")
	}
}
