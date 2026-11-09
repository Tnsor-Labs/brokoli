// Package runas runs user code as an unprivileged user of its tenant's
// own, instead of as whatever user the server runs as.
//
// The server usually runs as root in its container. Every child it
// started for user code (code nodes, bash, task harnesses, dbt) used to
// run as root too, so a script could read the shared data directory,
// look into other tenants' processes running at the same moment, and
// replace the server's own binary. With BROKOLI_USER_CODE_UIDS set, each
// such child runs as a user ID derived from its tenant (the org and
// workspace the run belongs to) inside the configured range:
//
//   - two tenants get different users, so neither can read the other's
//     files or /proc entries, or signal the other's processes;
//   - neither is root, so neither can read root-only files, write the
//     server's binary, or raise the resource limits it was given.
//
// The server prepares a private folder per execution (MkdirTemp), writes
// what the child needs into it, hands it to the tenant's user (Own), and
// starts the child with Apply. Files the child leaves behind are read
// back through Claim or os.Root, never by following a path the child
// could have redirected: the server is more privileged than the child,
// so a symlink planted in the folder would otherwise make the server
// read a root-only file on the tenant's behalf.
//
// Unset, nothing changes: children run as the server's own user, in the
// same places as before.
package runas

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// EnvUIDs names the setting: an inclusive range "FIRST-LAST" of user IDs
// for user code, for example 1000000000-1999999999. Each tenant's user ID
// is a hash of its tenant key into the range, so the range should be wide:
// with a billion IDs, a thousand tenants share one with a probability of
// about one in two thousand.
const EnvUIDs = "BROKOLI_USER_CODE_UIDS"

// minUID keeps the range clear of system and login accounts.
const minUID = 10000

// maxUID is the largest user ID Linux accepts; (uid_t)-1 means "no change".
const maxUID = 4294967294

// Identity is the user a child runs as. The zero value is "the server's
// own user": every method is then a no-op that keeps the old behaviour.
type Identity struct {
	uid uint32
	on  bool
}

// Enabled reports whether children run as a separate user.
func (id Identity) Enabled() bool { return id.on }

// UID is the user (and group) ID the child runs as. Meaningful only when
// Enabled.
func (id Identity) UID() uint32 { return id.uid }

func (id Identity) String() string {
	if !id.on {
		return "the server's own user"
	}
	return "uid " + strconv.FormatUint(uint64(id.uid), 10)
}

// uidRange parses EnvUIDs. ok is false when it is unset.
func uidRange() (first, last uint32, ok bool, err error) {
	raw := strings.TrimSpace(os.Getenv(EnvUIDs))
	if raw == "" {
		return 0, 0, false, nil
	}
	a, b, found := strings.Cut(raw, "-")
	if !found {
		return 0, 0, false, fmt.Errorf("%s=%q: want FIRST-LAST, for example 1000000000-1999999999", EnvUIDs, raw)
	}
	lo, errA := strconv.ParseUint(strings.TrimSpace(a), 10, 32)
	hi, errB := strconv.ParseUint(strings.TrimSpace(b), 10, 32)
	switch {
	case errA != nil || errB != nil:
		return 0, 0, false, fmt.Errorf("%s=%q: both ends must be user IDs", EnvUIDs, raw)
	case lo < minUID:
		return 0, 0, false, fmt.Errorf("%s=%q: the range must start at %d or above, clear of system accounts", EnvUIDs, raw, minUID)
	case hi > maxUID:
		return 0, 0, false, fmt.Errorf("%s=%q: the range must end at %d or below", EnvUIDs, raw, uint64(maxUID))
	case hi < lo:
		return 0, 0, false, fmt.Errorf("%s=%q: the range ends before it starts", EnvUIDs, raw)
	}
	return uint32(lo), uint32(hi), true, nil
}

// For returns the identity user code of tenant runs as. tenant is the
// org/workspace key the run belongs to; the empty tenant (a single-tenant
// server, or work that names none) gets an identity of its own like any
// other. The error is a misconfigured EnvUIDs, which must stop the
// execution rather than quietly run it as root.
func For(tenant string) (Identity, error) {
	first, last, ok, err := uidRange()
	if err != nil || !ok {
		return Identity{}, err
	}
	if !supported {
		return Identity{}, ErrNotSupported
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(tenant))
	span := uint64(last) - uint64(first) + 1
	return Identity{uid: first + uint32(h.Sum64()%span), on: true}, nil // #nosec G115 -- the remainder is below span, and first+span-1 is last, a uint32
}

// Configured reports whether EnvUIDs is set to a valid range: user code
// runs as other users, so a path it could have written must not be
// followed out of its folder.
func Configured() bool {
	_, _, ok, err := uidRange()
	return ok && err == nil
}

type tenantKey struct{}

// WithTenant marks ctx as running work for tenant (org/workspace). User
// code started under ctx runs as that tenant's identity.
func WithTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, tenantKey{}, tenant)
}

// TenantFrom is the tenant WithTenant recorded, or "".
func TenantFrom(ctx context.Context) string {
	tenant, _ := ctx.Value(tenantKey{}).(string)
	return tenant
}

// ForContext is For(TenantFrom(ctx)).
func ForContext(ctx context.Context) (Identity, error) {
	return For(TenantFrom(ctx))
}

// ErrNotSupported is returned on platforms that cannot start a process
// as another user.
var ErrNotSupported = errors.New(EnvUIDs + " is supported on Linux only")

// OpenIn opens the file rel inside dir for reading. When user code runs as
// other users (Configured), dir is one such code may have written, so rel
// is resolved without leaving dir (os.Root), and must name a regular file;
// it is opened without blocking, so a FIFO planted in its place cannot
// stall the server. Otherwise it is os.Open(dir/rel), as before.
func OpenIn(dir, rel string) (*os.File, error) {
	if !Configured() {
		return os.Open(filepath.Join(dir, rel)) // #nosec G304 -- dir and rel are the caller's
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(rel, os.O_RDONLY|syscallNonblock, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("%s is not a regular file", filepath.Join(dir, rel))
	}
	return f, nil
}
