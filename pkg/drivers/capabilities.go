package drivers

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
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

// DriverIdentity pins a native driver's selected artifact. LibrarySHA256 is
// the SHA-256 of the installed shared library, not of its distribution archive.
type DriverIdentity struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	LibrarySHA256 string `json:"library_sha256"`
}

// CapabilityError identifies why a native-driver capability request was
// refused. The sentinels above support errors.Is; callers may use Identity for
// an actionable diagnostic without relying on Error text.
type CapabilityError struct {
	Err      error
	Identity DriverIdentity
}

func (e *CapabilityError) Error() string {
	return fmt.Sprintf("%v: %s", e.Err, e.Identity.Name)
}

func (e *CapabilityError) Unwrap() error { return e.Err }

// RequiredCapabilities validates identity against this manager's loaded,
// verified manifests and returns the exact tags a scheduler must require. It
// never falls back to a name-only or version-only match.
func (m *Manager) RequiredCapabilities(identity DriverIdentity) ([]string, error) {
	if !validDriverIdentity(identity) {
		return nil, &CapabilityError{Err: ErrInvalidDriverRequest, Identity: identity}
	}

	if m.Get(identity.Name) == nil {
		return nil, &CapabilityError{Err: ErrDriverNotInstalled, Identity: identity}
	}
	manifest := m.GetIdentity(identity)
	if manifest == nil {
		// The driver is installed but not this build. Naming the difference
		// matters: "not installed" sends an operator to install the driver,
		// while a mismatch means the pinned build is absent or was replaced.
		return nil, &CapabilityError{Err: ErrDriverIdentityMismatch, Identity: identity}
	}

	return nativeADBCCapabilities(identity), nil
}

func nativeADBCCapabilities(identity DriverIdentity) []string {
	return []string{
		NativeADBCCapability,
		NativeADBCCapability + ":" + identity.Name,
		NativeADBCCapability + ":" + identity.Name + ":" + identity.Version + ":" + strings.ToLower(identity.LibrarySHA256[:libraryDigestPrefixLength]),
	}
}

func validDriverIdentity(identity DriverIdentity) bool {
	return driverNameRE.MatchString(identity.Name) &&
		capabilityVersionRE.MatchString(identity.Version) &&
		sha256Hex(identity.LibrarySHA256)
}

// A version is embedded as one colon-delimited opaque tag component.
var capabilityVersionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
