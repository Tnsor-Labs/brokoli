package drivers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// Managed drivers: a deployment whose native drivers the platform manages
// declares them as one set, by exact identity. The set is the whole truth:
// the API lists it (so users pin connections to what the platform
// provides), refuses installs and removals, and every worker installs it
// from the catalog when it starts. Nobody installs native code through the
// API on such a deployment.

// ManagedEnvVar holds the managed set: a JSON array of driver identities,
//
//	[{"name":"flightsql","version":"1.12.0","library_sha256":"2c9e..."}]
//
// Setting it to anything non-empty makes the deployment managed, even a
// value that does not parse: a typo must not quietly reopen the API to
// installs. The value is checked where it is used.
const ManagedEnvVar = "BROKOLI_MANAGED_DRIVERS"

// ErrManaged is the refusal of an install or removal through the API on a
// managed deployment.
var ErrManaged = errors.New("native drivers on this deployment are managed by the platform: " +
	"they are installed and removed by its operator, not through the API")

// ManagedSet is the declared set of managed driver builds.
type ManagedSet []DriverIdentity

// Managed reports whether this deployment declares a managed set.
func Managed() bool { return strings.TrimSpace(os.Getenv(ManagedEnvVar)) != "" }

// ManagedDrivers returns the declared set, or nil when none is declared.
func ManagedDrivers() (ManagedSet, error) {
	raw := strings.TrimSpace(os.Getenv(ManagedEnvVar))
	if raw == "" {
		return nil, nil
	}
	return ParseManagedSet(raw)
}

// ParseManagedSet parses and checks a managed set: every entry a complete
// identity, and no driver version declared twice.
func ParseManagedSet(raw string) (ManagedSet, error) {
	var set ManagedSet
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&set); err != nil {
		return nil, fmt.Errorf("%s: not a JSON array of driver identities: %w", ManagedEnvVar, err)
	}
	seen := make(map[string]bool, len(set))
	for i, identity := range set {
		if !ValidIdentity(identity) {
			return nil, fmt.Errorf("%s: entry %d (%s %s) is not a complete driver identity: name, version and the library's full sha256 are required",
				ManagedEnvVar, i, identity.Name, identity.Version)
		}
		set[i] = identity.Normalized()
		key := set[i].Name + "@" + set[i].Version
		if seen[key] {
			return nil, fmt.Errorf("%s: %s %s is declared twice", ManagedEnvVar, identity.Name, identity.Version)
		}
		seen[key] = true
	}
	return set, nil
}

// EnsureManaged installs every build of set that is not installed yet,
// from the catalog at indexURL, and checks each against its declared
// library digest. A build already installed is left alone, so a restart
// costs nothing. An unreachable catalog is retried until ctx ends; any
// other failure stops it at once: a worker missing a declared driver must
// not start serving runs.
func (m *Manager) EnsureManaged(ctx context.Context, indexURL string, set ManagedSet) error {
	for _, identity := range set {
		if m.GetIdentity(identity) != nil {
			continue
		}
		installed, err := m.installRetrying(ctx, indexURL, identity)
		if err != nil {
			return fmt.Errorf("install managed driver %s %s: %w", identity.Name, identity.Version, err)
		}
		if got := installed.Identity().LibrarySHA256; got != identity.LibrarySHA256 {
			return fmt.Errorf("managed driver %s %s: the catalog's build has library sha256 %s, but the platform declares %s",
				identity.Name, identity.Version, got, identity.LibrarySHA256)
		}
	}
	return nil
}

// Retrying an unreachable catalog: the first wait, and the longest.
var (
	catalogRetryFirst = 2 * time.Second
	catalogRetryMax   = 30 * time.Second
)

// installRetrying installs identity from the catalog, trying again while
// the catalog cannot be reached (ErrCatalogUnavailable: DNS, the network,
// the host answering 5xx) until ctx ends. A worker starting during a brief
// outage then waits it out instead of exiting and restarting; anything
// else (a digest mismatch, a release not in the catalog) fails at once.
func (m *Manager) installRetrying(ctx context.Context, indexURL string, identity DriverIdentity) (*Manifest, error) {
	wait := catalogRetryFirst
	for attempt := 1; ; attempt++ {
		installed, err := m.InstallFromCatalogAt(ctx, indexURL, identity.Name, identity.Version)
		if err == nil || !errors.Is(err, ErrCatalogUnavailable) {
			return installed, err
		}
		log.Printf("native drivers: %s %s: %v; trying again in %s (attempt %d)", identity.Name, identity.Version, err, wait, attempt)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w (gave up after %d attempts: %v)", err, attempt, ctx.Err())
		case <-time.After(wait):
		}
		if wait *= 2; wait > catalogRetryMax {
			wait = catalogRetryMax
		}
	}
}
