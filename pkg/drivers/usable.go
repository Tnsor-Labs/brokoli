package drivers

import (
	"sort"

	"github.com/Tnsor-Labs/brokoli/models"
)

// driverConnectionTypes says which connection types can read through a
// driver, by the driver's catalog name. It is the one place this is
// decided: the catalog and installed-driver APIs report it, saving a
// connection refuses a pin to a driver that does not serve its type, and
// the runner checks it again before loading anything.
//
// A driver appears here only once a connection type's settings have been
// mapped to the URI and options that driver expects and a run through it
// has been proven against a real server. A catalog driver that is not
// listed installs and loads, but no connection can use it yet.
var driverConnectionTypes = map[string][]models.ConnectionType{
	"flightsql":  {models.ConnTypeFlightSQL},
	"postgresql": {models.ConnTypePostgres},
	"sqlite":     {models.ConnTypeSQLite},
	"mysql":      {models.ConnTypeMySQL},
	"clickhouse": {models.ConnTypeClickHouse},
}

// UsableBy returns the connection types that can read through the driver
// named name, sorted; empty when none can yet.
func UsableBy(name string) []models.ConnectionType {
	kinds := append([]models.ConnectionType(nil), driverConnectionTypes[name]...)
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

// Serves reports whether a connection of type kind can read through the
// driver named name.
func Serves(name string, kind models.ConnectionType) bool {
	for _, k := range driverConnectionTypes[name] {
		if k == kind {
			return true
		}
	}
	return false
}

// NativeConnectionTypes returns every connection type some driver serves,
// sorted: the types that can be pinned to a native driver at all.
func NativeConnectionTypes() []models.ConnectionType {
	seen := map[models.ConnectionType]bool{}
	var kinds []models.ConnectionType
	for _, list := range driverConnectionTypes {
		for _, k := range list {
			if !seen[k] {
				seen[k] = true
				kinds = append(kinds, k)
			}
		}
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}
