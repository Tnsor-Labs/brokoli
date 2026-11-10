package engine

// Server-wide limits on mapping (ADR-050, #967): the defaults a `map` or
// `expansion` block gets when it does not declare a limit, and the ceilings
// an operator allows it to declare. Item limits fail the node, naming the
// limit and the count; a collection is never truncated to fit.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Environment variables an operator sets to change the mapping limits.
const (
	envMapDefaultMaxItems    = "BROKOLI_MAP_DEFAULT_MAX_ITEMS"
	envMapMaxItems           = "BROKOLI_MAP_MAX_ITEMS"
	envMapDefaultMaxParallel = "BROKOLI_MAP_DEFAULT_MAX_PARALLEL"
	envMapMaxParallel        = "BROKOLI_MAP_MAX_PARALLEL"
	envMapMaxRemoteParallel  = "BROKOLI_MAP_MAX_REMOTE_PARALLEL"
)

// defaultMapMaxItemsCeiling is the most items a node may declare when the
// operator sets no ceiling. Every item is an instance record in the store
// and, combined, one dataset in memory; a node declaring more is more
// likely a mistake than a plan.
const defaultMapMaxItemsCeiling = 100000

// defaultMapMaxParallelCeiling is the most items a node may declare to run
// at once on one server or worker when the operator sets no ceiling.
const defaultMapMaxParallelCeiling = 64

// mapLimits are the limits in force on this server.
type mapLimits struct {
	// DefaultItems is the item limit of a block that declares none.
	DefaultItems int
	// MaxItems is the highest item limit a block may declare.
	MaxItems int
	// DefaultParallel is how many items of a `map` block run at once
	// locally when the block declares none.
	DefaultParallel int
	// MaxParallel is the highest max_parallel a block may declare.
	MaxParallel int
	// MaxRemoteParallel bounds how many of one node's items are dispatched
	// to remote workers at once. It protects the job queue and the store,
	// not the author's plan, so a higher max_parallel is lowered to it
	// rather than refused.
	MaxRemoteParallel int
	// Warnings names each limit variable that was set but ignored.
	Warnings []string
}

// currentMapLimits reads the limits from the environment. A variable that
// is not a whole number of at least 1 is ignored with a warning, and a
// default above its ceiling is lowered to the ceiling, also with a warning.
func currentMapLimits() mapLimits {
	l := mapLimits{
		DefaultItems:      defaultMaxExpansionInstances,
		MaxItems:          defaultMapMaxItemsCeiling,
		DefaultParallel:   defaultMapMaxParallel,
		MaxParallel:       defaultMapMaxParallelCeiling,
		MaxRemoteParallel: maxConcurrentRemoteExpansionDispatch,
	}
	read := func(name string, into *int) {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			return
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			l.Warnings = append(l.Warnings, fmt.Sprintf("ignoring %s=%q: expected a whole number of at least 1", name, raw))
			return
		}
		*into = n
	}
	read(envMapMaxItems, &l.MaxItems)
	read(envMapDefaultMaxItems, &l.DefaultItems)
	read(envMapMaxParallel, &l.MaxParallel)
	read(envMapDefaultMaxParallel, &l.DefaultParallel)
	read(envMapMaxRemoteParallel, &l.MaxRemoteParallel)
	if l.DefaultItems > l.MaxItems {
		l.Warnings = append(l.Warnings, fmt.Sprintf("%s=%d is above %s=%d; using %d", envMapDefaultMaxItems, l.DefaultItems, envMapMaxItems, l.MaxItems, l.MaxItems))
		l.DefaultItems = l.MaxItems
	}
	if l.DefaultParallel > l.MaxParallel {
		l.Warnings = append(l.Warnings, fmt.Sprintf("%s=%d is above %s=%d; using %d", envMapDefaultMaxParallel, l.DefaultParallel, envMapMaxParallel, l.MaxParallel, l.MaxParallel))
		l.DefaultParallel = l.MaxParallel
	}
	return l
}

// itemCeilingError refuses a declared item limit above the server's
// ceiling, or returns nil. setting is the block's own name for it.
func (l mapLimits) itemCeilingError(where, setting string, declared int) error {
	if declared <= l.MaxItems {
		return nil
	}
	return fmt.Errorf("%s: '%s' is %d, above this server's ceiling of %d items (%s)", where, setting, declared, l.MaxItems, envMapMaxItems)
}

// parallelCeilingError refuses a declared max_parallel above the server's
// ceiling, or returns nil.
func (l mapLimits) parallelCeilingError(where string, declared int) error {
	if declared <= l.MaxParallel {
		return nil
	}
	return fmt.Errorf("%s: 'map.max_parallel' is %d, above this server's ceiling of %d (%s)", where, declared, l.MaxParallel, envMapMaxParallel)
}
