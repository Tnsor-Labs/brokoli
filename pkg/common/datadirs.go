package common

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The server's own state on disk, which a pipeline's file nodes must
// never read or write however BROKOLI_DATA_DIRS is set.
//
// A data directory may be, and by default is, the working directory,
// which is also where the server keeps the SQLite database, the
// encryption key that decrypts every stored credential, the JWT signing
// secret, and the artifact store holding every run's node outputs. Those
// paths pass the data-directory check like any other file, so a
// source_file node could read the key and a sink_file node could
// overwrite it. The paths below are registered at startup and refused
// here, so reaching them does not depend on how the data directories are
// configured.
var (
	protectedMu    sync.RWMutex
	protectedPaths []string // absolute, symlinks resolved where they exist
)

// ProtectServerPath registers a file or directory as the server's own
// state, which no pipeline file node may read or write. Call it once per
// path at startup, before any run. An empty or unresolvable path is
// ignored; registering the same path twice is harmless.
func ProtectServerPath(path string) {
	if path == "" {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return
	}
	if real, err := resolveExisting(abs); err == nil {
		abs = real
	}
	protectedMu.Lock()
	defer protectedMu.Unlock()
	for _, p := range protectedPaths {
		if p == abs {
			return
		}
	}
	protectedPaths = append(protectedPaths, abs)
}

// ProtectSQLiteDatabase protects a SQLite database file and the sidecar
// files SQLite writes beside it. It does nothing for a connection URL
// (such as a PostgreSQL DSN), which is not a local file.
func ProtectSQLiteDatabase(databasePath string) {
	if databasePath == "" || strings.Contains(databasePath, "://") {
		return
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		ProtectServerPath(databasePath + suffix)
	}
}

// pathIsProtected reports whether abs (already absolute) is a protected
// server path or sits inside one. The path as written and its
// symlink-resolved form are both checked, so a symlink inside a data
// directory that points at protected state is caught too.
//
// A path that merely contains a protected path as a descendant is NOT
// refused: the artifact and checkpoint directories live inside a data
// directory by default, so a sink writing an ordinary file whose parent
// directory also contains the artifact directory must still work. The
// protected files, and anything inside the protected directories, stay
// refused by the two checks below.
func pathIsProtected(abs string) bool {
	candidates := []string{abs}
	if real, err := resolveExisting(abs); err == nil && real != abs {
		candidates = append(candidates, real)
	}
	protectedMu.RLock()
	defer protectedMu.RUnlock()
	for _, p := range protectedPaths {
		for _, c := range candidates {
			if c == p || strings.HasPrefix(c, p+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

// Where file nodes are allowed to read and write.
//
// There used to be two answers to this. The engine kept a configurable
// list (BROKOLI_DATA_DIRS, defaulting to /data, /tmp and the working
// directory) and checked a path against it before handing it to a loader;
// the loader then checked again against a hardcoded pair — the working
// directory and os.TempDir() — and refused anything else. So the two
// disagreed on their own defaults: /data was in the engine's list and
// unreachable in practice, and setting BROKOLI_DATA_DIRS to allow a new
// directory passed the first check and failed the second, with an error
// that named no directory and no setting.
//
// That mattered as soon as anyone mounted shared storage: /data is the
// obvious mount point and the one the engine already advertised, and it
// was exactly the one that could not work.
//
// One list now, defined here because this is the lower-level package and
// the loaders cannot import the engine.

// defaultDataDirs is where file nodes may read and write when
// BROKOLI_DATA_DIRS is unset. "." is the working directory, which is
// where a local `brokoli run` reads its files from.
var defaultDataDirs = []string{"/data", "/tmp", "."}

// DataDirs returns the directories file nodes may use, from
// BROKOLI_DATA_DIRS (colon-separated) or the default.
//
// Read per call rather than cached at init: tests set the variable, and
// the cost is a getenv against a file operation.
//
// An empty value falls back to the defaults rather than allowing nothing.
// Unset and empty are the same thing to most tooling, and a chart that
// renders `value: "{{ .Values.dataDirs }}"` with nothing configured
// produces an empty string — which under the other reading would disable
// every file node in the deployment with no error saying so. To restrict
// access, name the directories that are allowed.
func DataDirs() []string {
	if dirs := os.Getenv("BROKOLI_DATA_DIRS"); dirs != "" {
		var out []string
		for _, d := range strings.Split(dirs, ":") {
			if d = strings.TrimSpace(d); d != "" {
				out = append(out, d)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	// The system temp directory is included even when it is not /tmp, so
	// the default keeps working on platforms that put it elsewhere.
	if tmp := os.TempDir(); tmp != "" && tmp != "/tmp" {
		return append(append([]string{}, defaultDataDirs...), tmp)
	}
	return defaultDataDirs
}

// PathAllowed reports whether path is inside one of the data directories,
// for callers that check a path and use it later (a working directory, a
// file a wait polls for). Code that opens the file should use
// OpenInDataDir, which checks and opens in one step.
//
// The path must be inside a data directory as written and also once its
// symlinks are followed: a link inside /data pointing at /etc is refused.
// The error names the directories and the setting that changes them,
// because "not permitted" on its own leaves an operator nothing to do —
// which is exactly what the loaders' refusal used to say.
func PathAllowed(path string) error {
	err := inDataDir(path, func(root *os.Root, rel string) error {
		_, err := root.Stat(rel)
		return err
	})
	// A missing file is fine (a sink creates it), and so is a missing data
	// directory, which has nothing in it to lead anywhere. A path that
	// resolves out of the directory, even through a missing leaf, is not.
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// OpenInDataDir is os.OpenFile confined to the data directory that
// contains path. It opens through os.Root, so a symlink or ".." that
// leads out of the directory is refused by the open itself; there is no
// window between a check and the open for the file to be swapped.
func OpenInDataDir(path string, flag int, perm os.FileMode) (*os.File, error) {
	var f *os.File
	err := inDataDir(path, func(root *os.Root, rel string) error {
		var err error
		f, err = root.OpenFile(rel, flag, perm)
		return err
	})
	return f, err
}

// MkdirAllInDataDir is os.MkdirAll confined like OpenInDataDir. The data
// directory itself is created if it is missing: it is configured, so it
// is trusted, and creating it is what writing into it always did.
func MkdirAllInDataDir(path string, perm os.FileMode) error {
	dir, _, err := dataDirFor(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, perm); err != nil {
		return err
	}
	return inDataDir(path, func(root *os.Root, rel string) error {
		return root.MkdirAll(rel, perm)
	})
}

// inDataDir runs op on path through an os.Root at its data directory.
//
// os.Root refuses every absolute symlink, including one that points back
// inside the directory (/data/latest -> /data/2026/10), which is ordinary
// layout. So when op is refused as an escape, the path is resolved to
// where it really is; if that is inside the directory's real location,
// op runs again there, on a path with no symlinks left in it. The retry
// still goes through os.Root, so a link swapped in meanwhile is refused.
func inDataDir(path string, op func(root *os.Root, rel string) error) error {
	dir, rel, err := dataDirFor(path)
	if err != nil {
		return err
	}
	err = withRoot(dir, rel, op)
	if err == nil || !isEscape(err) {
		return dataDirError(path, err)
	}
	realDir, rerr := filepath.EvalSymlinks(dir)
	if rerr != nil {
		return dataDirError(path, err)
	}
	real, rerr := resolveExisting(filepath.Join(dir, rel))
	if rerr != nil {
		return dataDirError(path, err)
	}
	realRel, rerr := filepath.Rel(realDir, real)
	if rerr != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
		return dataDirError(path, err)
	}
	return dataDirError(path, withRoot(realDir, realRel, op))
}

func withRoot(dir, rel string, op func(root *os.Root, rel string) error) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // anything op opened outlives the root
	return op(root, rel)
}

// resolveExisting is filepath.EvalSymlinks for a path whose last parts
// may not exist yet: the longest existing prefix is resolved and the rest
// appended as written.
func resolveExisting(path string) (string, error) {
	missing := ""
	for {
		real, err := filepath.EvalSymlinks(path)
		if err == nil {
			return filepath.Join(real, missing), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		missing = filepath.Join(filepath.Base(path), missing)
		path = parent
	}
}

// isEscape reports an os.Root refusal for leaving the root. os has no
// exported sentinel for it, so it is recognised by its message.
func isEscape(err error) bool {
	return err != nil && strings.Contains(err.Error(), "path escapes from parent")
}

// dataDirFor finds the data directory path is in, as written, and path
// relative to it. When directories nest, the innermost one is used.
func dataDirFor(path string) (dir, rel string, err error) {
	if strings.Contains(path, "..") {
		return "", "", fmt.Errorf("path traversal not allowed")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("invalid path %q", path)
	}
	dirs := DataDirs()
	for _, d := range dirs {
		absDir, err := filepath.Abs(d)
		if err != nil {
			continue
		}
		if (abs == absDir || strings.HasPrefix(abs, absDir+string(filepath.Separator))) && len(absDir) > len(dir) {
			dir = absDir
		}
	}
	if dir == "" {
		return "", "", fmt.Errorf("file path %q outside allowed directories (%s); set BROKOLI_DATA_DIRS to allow additional paths",
			path, strings.Join(dirs, ", "))
	}
	rel, err = filepath.Rel(dir, abs)
	if err != nil {
		return "", "", fmt.Errorf("invalid path %q", path)
	}
	if pathIsProtected(abs) {
		return "", "", fmt.Errorf("file path %q is part of the server's own state and cannot be read or written by a pipeline", path)
	}
	return dir, rel, nil
}

// dataDirError puts the caller's path back into an os.Root error, which
// names the path relative to the root, and says plainly when the path
// left the directory.
func dataDirError(path string, err error) error {
	if err == nil {
		return nil
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		pe.Path = path
	}
	if isEscape(err) {
		return fmt.Errorf("file path %q leads outside the allowed directories once its symlinks are followed: %w", path, err)
	}
	return err
}
