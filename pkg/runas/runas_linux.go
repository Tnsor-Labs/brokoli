//go:build linux

package runas

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// syscallNonblock opens without waiting for a FIFO's writer.
const syscallNonblock = syscall.O_NONBLOCK

// supported reports whether this platform can start a child as another user.
const supported = true

// Apply makes cmd start as id: its user and group, and no supplementary
// groups (the server's are root's). Everything else in cmd.SysProcAttr
// (the process group, a cgroup) is kept.
func (id Identity) Apply(cmd *exec.Cmd) {
	if !id.on {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: id.uid, Gid: id.uid, Groups: []uint32{}}
}

var (
	baseOnce sync.Once
	baseDir  string
	baseErr  error
)

// base is this process's parent folder for user-code folders: made fresh
// under the system temp directory, owned by the server, mode 0711. Other
// users may enter a folder in it whose name they know, and cannot list it
// or create anything in it. A fixed, shared path under /tmp could be
// created first by someone else; a fresh one cannot.
func base() (string, error) {
	baseOnce.Do(func() {
		// Files the server writes are its own from now on: with user code
		// no longer root, the default 0644 would let every tenant read
		// whatever the engine leaves in a shared directory such as /tmp
		// (staged inputs, spilled datasets). What a child needs is handed
		// to it explicitly (Own).
		syscall.Umask(0o077)
		dir, err := os.MkdirTemp("", "brokoli-usercode-")
		if err == nil {
			err = os.Chmod(dir, 0o711) // #nosec G302 -- a directory others may traverse but not list or write; the folders inside are 0700
		}
		if err == nil {
			err = os.Mkdir(filepath.Join(dir, "claimed"), 0o700)
		}
		baseDir, baseErr = dir, err
	})
	return baseDir, baseErr
}

// MkdirTemp makes a new private folder for one execution, like
// os.MkdirTemp("", pattern). It still belongs to the server: write what
// the child needs into it, then call Own.
func (id Identity) MkdirTemp(pattern string) (string, error) {
	if !id.on {
		return os.MkdirTemp("", pattern)
	}
	parent, err := base()
	if err != nil {
		return "", fmt.Errorf("user code folder: %w", err)
	}
	return os.MkdirTemp(parent, pattern)
}

// Own hands path, and everything under it, to id. Symlinks are changed
// themselves, never followed. Call it before the child starts and after
// the server has written what the child needs: once the folder is the
// child's, the server reads from it only through Claim or os.Root.
func (id Identity) Own(path string) error {
	if !id.on {
		return nil
	}
	err := filepath.WalkDir(path, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, int(id.uid), int(id.uid)) // #nosec G122 -- runs before the child starts, on a folder only the server can write yet; Lchown changes a link, never follows it
	})
	if err != nil {
		return fmt.Errorf("hand %s to %s: %w", path, id, err)
	}
	return nil
}

// Claim takes the file name, directly inside dir, out of dir: it is moved
// elsewhere and its new path returned for the caller to read and remove,
// so it outlives dir. When enabled, elsewhere is a folder only the server
// can reach, and the file must be a regular file the child owns. A rename
// acts on the name itself, so a symlink is moved as a symlink and refused,
// never followed. Not enabled, it moves to the system temp directory.
func (id Identity) Claim(dir, name string) (string, error) {
	src := filepath.Join(dir, name)
	if name == "" || filepath.Base(name) != name {
		return "", fmt.Errorf("claim %q: not a name directly inside %s", name, dir)
	}
	var suffix [8]byte
	_, _ = rand.Read(suffix[:])
	if !id.on {
		dst := filepath.Join(os.TempDir(), "brokoli-"+hex.EncodeToString(suffix[:])+"-"+name)
		if err := os.Rename(src, dst); err != nil {
			return "", fmt.Errorf("claim %s: %w", name, err)
		}
		return dst, nil
	}
	parent, err := base()
	if err != nil {
		return "", err
	}
	dst := filepath.Join(parent, "claimed", hex.EncodeToString(suffix[:])+"-"+name)
	if err := os.Rename(src, dst); err != nil {
		return "", fmt.Errorf("claim %s: %w", name, err)
	}
	info, err := os.Lstat(dst)
	if err != nil {
		return "", err
	}
	st, _ := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || st == nil || st.Uid != id.uid {
		_ = os.Remove(dst)
		return "", fmt.Errorf("user code output %s is not a regular file it wrote itself; refusing to read it", name)
	}
	return dst, nil
}

// Env is env with HOME and TMPDIR pointed at home, the child's own
// folder: the server's HOME (/root) is not the child's to use, and a
// shared temp directory is visible to every tenant. Not enabled, env is
// returned unchanged.
func (id Identity) Env(env []string, home string) []string {
	if !id.on {
		return env
	}
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "TMPDIR=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HOME="+home, "TMPDIR="+home)
}

// The capabilities the server needs to run children as other users and
// still manage them: start them as another user (SETUID, SETGID), hand
// them folders (CHOWN), read and remove what they leave (DAC_OVERRIDE,
// FOWNER), stop them (KILL), and set their resource limits from outside
// (SYS_RESOURCE, which containers do not get by default).
var required = []struct {
	bit  uint
	name string
}{
	{0, "CHOWN"}, {1, "DAC_OVERRIDE"}, {3, "FOWNER"}, {5, "KILL"},
	{6, "SETGID"}, {7, "SETUID"}, {24, "SYS_RESOURCE"},
}

// Check validates the setting at startup and says what is in effect. It
// fails when the setting is malformed or this process cannot do what it
// promises, so a deployment that asked for separate users never silently
// runs user code as root. Unset, it warns when the server is root.
func Check() error {
	first, last, ok, err := uidRange()
	if err != nil {
		return err
	}
	if !ok {
		if os.Geteuid() == 0 {
			log.Printf("warning: user code runs as root; set %s (for example 1000000000-1999999999) to run each tenant's code as its own unprivileged user", EnvUIDs)
		}
		return nil
	}
	caps, err := effectiveCaps()
	if err != nil {
		return fmt.Errorf("%s: read this process's capabilities: %w", EnvUIDs, err)
	}
	var missing []string
	for _, c := range required {
		if caps&(1<<c.bit) == 0 {
			missing = append(missing, "CAP_"+c.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s is set, but this process lacks %s: run it as root with those capabilities (Kubernetes: securityContext.capabilities.add [SYS_RESOURCE]), or unset %s",
			EnvUIDs, strings.Join(missing, ", "), EnvUIDs)
	}
	// Without it, a tenant could hard-link a root-only file into its own
	// folder and have the server read it back.
	if raw, err := os.ReadFile("/proc/sys/fs/protected_hardlinks"); err != nil || strings.TrimSpace(string(raw)) != "1" {
		return fmt.Errorf("%s is set, but fs.protected_hardlinks is not 1 on this host; separate users cannot keep a tenant from linking files it may not read", EnvUIDs)
	}
	if _, err := base(); err != nil {
		return fmt.Errorf("%s: %w", EnvUIDs, err)
	}
	log.Printf("user code runs as a separate user per tenant, uid range %d-%d", first, last)
	return nil
}

func effectiveCaps() (uint64, error) {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "CapEff:"); ok {
			return strconv.ParseUint(strings.TrimSpace(v), 16, 64)
		}
	}
	return 0, fmt.Errorf("no CapEff line")
}
