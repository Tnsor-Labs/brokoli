package api

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

func TestUnsupportedDatabaseTestNamesMissingDriver(t *testing.T) {
	for _, kind := range []models.ConnectionType{
		models.ConnTypeMSSQL,
		models.ConnTypeSnowflake,
		models.ConnTypeOracle,
		models.ConnTypeBigQuery,
		models.ConnTypeDatabricks,
	} {
		result := unsupportedDatabaseTest(kind)
		if result["success"] != false {
			t.Errorf("%s: success = %v, want false", kind, result["success"])
		}
		if result["error"] != string(kind)+" has no driver in this build" {
			t.Errorf("%s: error = %v", kind, result["error"])
		}
	}
}
