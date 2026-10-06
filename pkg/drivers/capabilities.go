package drivers

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Tnsor-Labs/brokoli/models"
)

const (
	NativeADBCCapability      = "native-adbc"
	libraryDigestPrefixLength = 12
)

var (
	// ErrInvalidDriverRequest means a caller did not provide a complete,
	// canonical driver identity. It must not be treated as a broad driver match.
	ErrInvalidDriverRequest = errors.New("invalid native ADBC driver request")
	// ErrDriverNotInstalled means no installed driver has the requested name.
	ErrDriverNotInstalled = errors.New("native ADBC driver is not installed")
	// ErrDriverIdentityMismatch means the named driver is installed but its
	// version or library content differs from the requested identity.
	ErrDriverIdentityMismatch = errors.New("native ADBC driver identity does not match")
)

// DriverIdentity pins a native driver's selected artifact. It is the model
// type, aliased so this package's API reads naturally.
type DriverIdentity = models.DriverIdentity

// CapabilityError identifies why a native-driver capability request was
// refused. The sentinels above support errors.Is; callers may use Identity for
// an actionable diagnostic without relying on Error text.
type CapabilityError struct {
	Err      error
	Identity DriverIdentity
}

func (e *CapabilityError) Error() string {
	if e.Identity.Name == "" {
		return e.Err.Error()
	}
	if e.Identity.Version == "" {
		return fmt.Sprintf("%v: %s", e.Err, e.Identity.Name)
	}
	return fmt.Sprintf("%v: %s %s", e.Err, e.Identity.Name, e.Identity.Version)
}

func (e *CapabilityError) Unwrap() error { return e.Err }

// ValidIdentity reports whether identity is complete and canonical: a valid
// name and version and a full SHA-256 library digest.
func ValidIdentity(identity DriverIdentity) bool {
	return driverNameRE.MatchString(identity.Name) &&
		versionRE.MatchString(identity.Version) &&
		sha256Hex(identity.LibrarySHA256)
}

// Capabilities returns the worker capability tags a run needs to execute
// with identity. It checks only that the identity is well formed, never that
// it is installed here: the process scheduling a run is not necessarily one
// that executes it, and a control plane with no drivers must still be able
// to route work to the workers that have them.
func Capabilities(identity DriverIdentity) ([]string, error) {
	if !ValidIdentity(identity) {
		return nil, &CapabilityError{Err: ErrInvalidDriverRequest, Identity: identity}
	}
	n := identity.Normalized()
	return []string{
		NativeADBCCapability,
		NativeADBCCapability + ":" + n.Name,
		NativeADBCCapability + ":" + n.Name + ":" + n.Version + ":" + n.LibrarySHA256[:libraryDigestPrefixLength],
	}, nil
}

// RequiredCapabilities is Capabilities, additionally requiring that this
// manager has exactly that build installed. It never falls back to a
// name-only or version-only match.
func (m *Manager) RequiredCapabilities(identity DriverIdentity) ([]string, error) {
	if err := m.CheckInstalled(identity); err != nil {
		return nil, err
	}
	return Capabilities(identity)
}

// CheckInstalled reports whether exactly identity is installed, naming the
// difference when it is not: "not installed" sends an operator to install
// the driver, while a mismatch means the pinned build is absent or was
// replaced by another.
func (m *Manager) CheckInstalled(identity DriverIdentity) error {
	if !ValidIdentity(identity) {
		return &CapabilityError{Err: ErrInvalidDriverRequest, Identity: identity}
	}
	if m.GetIdentity(identity) != nil {
		return nil
	}
	if m.Get(identity.Name) == nil {
		return &CapabilityError{Err: ErrDriverNotInstalled, Identity: identity}
	}
	return &CapabilityError{Err: ErrDriverIdentityMismatch, Identity: identity}
}

// Advertised returns every capability tag the installed drivers provide,
// sorted. A worker advertises exactly this set.
func (m *Manager) Advertised() []string {
	seen := make(map[string]struct{})
	for _, manifest := range m.List() {
		tags, err := Capabilities(manifest.Identity())
		if err != nil {
			continue
		}
		for _, tag := range tags {
			seen[tag] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for tag := range seen {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// MissingCapability returns the first native-driver tag in required that
// advertised lacks, or "". Tags in other namespaces are the queue's to
// judge and are ignored.
func MissingCapability(required, advertised []string) string {
	have := make(map[string]struct{}, len(advertised))
	for _, tag := range advertised {
		have[tag] = struct{}{}
	}
	for _, tag := range required {
		if tag != NativeADBCCapability && !strings.HasPrefix(tag, NativeADBCCapability+":") {
			continue
		}
		if _, ok := have[tag]; !ok {
			return tag
		}
	}
	return ""
}
