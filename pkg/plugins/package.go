// Package archives (ADR-016 M1, issue #110): one published .bkg file —
// a gzipped tar of manifest.json plus per-platform/runtime payload
// directories — installs on any supported host, or fails at install
// time with a named, actionable reason. Nothing executes and nothing
// lands in the plugin directory before the selected payload's tree hash
// verifies and the plugin's own `spec` output agrees with the manifest.
package plugins

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Extraction guards: a payload archive is untrusted input. Individual
// files and the whole archive are capped, and every entry path must
// stay inside the extraction root.
const (
	maxArchiveFileBytes  = 256 << 20 // 256 MiB per file
	maxArchiveTotalBytes = 512 << 20 // 512 MiB per archive
)

// HashPayloadTree computes the canonical hash of a payload directory:
// for every regular file, in sorted relative slash-path order,
// "<path>\n<size>\n" followed by the file content is fed to one
// SHA-256. Mode bits and timestamps are deliberately excluded — they
// don't survive archives portably; the executable bit is re-applied at
// install time from the runtime class instead.
func HashPayloadTree(root string) (string, error) {
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, rel := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(full)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\n%d\n", rel, info.Size())
		f, err := os.Open(full) // #nosec G304 -- path is enumerated from the tree being hashed
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", err
		}
		f.Close()
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractArchive unpacks a .bkg (gzipped tar) into destRoot, refusing
// path traversal, links, and oversized content.
func extractArchive(archivePath, destRoot string) error {
	f, err := os.Open(archivePath) // #nosec G304 -- the operator-supplied archive being installed
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("not a gzip archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		name := filepath.FromSlash(hdr.Name)
		if filepath.IsAbs(name) || strings.Contains(name, "..") {
			return fmt.Errorf("archive entry %q escapes the extraction root", hdr.Name)
		}
		target := filepath.Join(destRoot, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if hdr.Size > maxArchiveFileBytes {
				return fmt.Errorf("archive entry %q exceeds the %d-byte file limit", hdr.Name, int64(maxArchiveFileBytes))
			}
			total += hdr.Size
			if total > maxArchiveTotalBytes {
				return fmt.Errorf("archive exceeds the %d-byte total limit", int64(maxArchiveTotalBytes))
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644) // #nosec G304 -- target is traversal-checked above
			if err != nil {
				return err
			}
			// LimitReader as defense in depth against a lying header.
			if _, err := io.Copy(out, io.LimitReader(tr, maxArchiveFileBytes+1)); err != nil { // #nosec G110 -- bounded by the per-file and total caps
				out.Close()
				return err
			}
			out.Close()
		default:
			// Symlinks, devices, fifos: nothing a plugin payload needs,
			// everything an attacker wants.
			return fmt.Errorf("archive entry %q has unsupported type %d", hdr.Name, hdr.Typeflag)
		}
	}
}

// ResolvedRuntime is how a selected payload becomes launchable on this
// host: the Binary/Args to write into the installed manifest.
type ResolvedRuntime struct {
	Binary string   // relative entrypoint (native) or absolute interpreter path
	Args   []string // leading args; for interpreters, starts with the entrypoint
}

// SelectPayload picks the first payload feasible on this host, in
// manifest order, and resolves how to launch it. The error, when every
// payload is infeasible, names each payload's reason.
func SelectPayload(m *Manifest) (*Payload, *ResolvedRuntime, error) {
	if len(m.Payloads) == 0 {
		return nil, nil, fmt.Errorf("manifest has no payloads")
	}
	var reasons []string
	for i := range m.Payloads {
		p := &m.Payloads[i]
		resolved, reason := resolvePayload(p)
		if reason == "" {
			return p, resolved, nil
		}
		reasons = append(reasons, fmt.Sprintf("payload %d (%s %s/%s): %s", i, p.Runtime, p.OS, p.Arch, reason))
	}
	return nil, nil, fmt.Errorf(
		"no payload is installable on this host (%s/%s):\n  %s",
		runtime.GOOS, runtime.GOARCH, strings.Join(reasons, "\n  "),
	)
}

func resolvePayload(p *Payload) (*ResolvedRuntime, string) {
	switch p.Runtime {
	case RuntimeNative:
		if p.OS != runtime.GOOS || p.Arch != runtime.GOARCH {
			return nil, fmt.Sprintf("built for %s/%s", p.OS, p.Arch)
		}
		return &ResolvedRuntime{Binary: p.Entrypoint, Args: p.Args}, ""
	case RuntimePython:
		if !platformMatches(p.OS, p.Arch) {
			return nil, fmt.Sprintf("declared for %s/%s", p.OS, p.Arch)
		}
		interp, reason := resolvePythonRuntime(p.Requires["python"])
		if reason != "" {
			return nil, reason
		}
		args := append([]string{p.Entrypoint}, p.Args...)
		return &ResolvedRuntime{Binary: interp, Args: args}, ""
	case RuntimeNode, RuntimeJVM:
		return nil, fmt.Sprintf("runtime class %q is not resolvable in this release (planned: #110 M2)", p.Runtime)
	default:
		return nil, fmt.Sprintf("unknown runtime class %q", p.Runtime)
	}
}

func platformMatches(osName, arch string) bool {
	osOK := osName == "" || osName == "any" || osName == runtime.GOOS
	archOK := arch == "" || arch == "any" || arch == runtime.GOARCH
	return osOK && archOK
}

// pythonVersionFn is swapped in tests to simulate hosts without (or
// with old) interpreters.
var pythonVersionFn = probePythonVersion

func probePythonVersion() (path string, major, minor int, err error) {
	path, err = exec.LookPath("python3")
	if err != nil {
		return "", 0, 0, fmt.Errorf("python3 not found on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput() // #nosec G204 -- path from LookPath("python3"), fixed argument
	if err != nil {
		return "", 0, 0, fmt.Errorf("%s --version failed: %v", path, err)
	}
	fields := strings.Fields(string(out)) // "Python 3.12.3"
	if len(fields) < 2 {
		return "", 0, 0, fmt.Errorf("unrecognized python version output %q", strings.TrimSpace(string(out)))
	}
	parts := strings.SplitN(fields[1], ".", 3)
	if len(parts) < 2 {
		return "", 0, 0, fmt.Errorf("unrecognized python version %q", fields[1])
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return "", 0, 0, fmt.Errorf("unrecognized python version %q", fields[1])
	}
	return path, major, minor, nil
}

// resolvePythonRuntime returns the interpreter path satisfying the
// constraint, or a human-readable infeasibility reason. Packaging
// version 1 understands ">=MAJOR.MINOR" (and empty = any python3).
func resolvePythonRuntime(constraint string) (path string, reason string) {
	interp, major, minor, err := pythonVersionFn()
	if err != nil {
		return "", err.Error()
	}
	constraint = strings.TrimSpace(constraint)
	if constraint == "" {
		return interp, ""
	}
	if !strings.HasPrefix(constraint, ">=") {
		return "", fmt.Sprintf("unsupported python constraint %q (packaging version 1 understands \">=MAJOR.MINOR\")", constraint)
	}
	want := strings.SplitN(strings.TrimPrefix(constraint, ">="), ".", 3)
	if len(want) < 2 {
		return "", fmt.Sprintf("unsupported python constraint %q", constraint)
	}
	wantMajor, err1 := strconv.Atoi(strings.TrimSpace(want[0]))
	wantMinor, err2 := strconv.Atoi(strings.TrimSpace(want[1]))
	if err1 != nil || err2 != nil {
		return "", fmt.Sprintf("unsupported python constraint %q", constraint)
	}
	if major > wantMajor || (major == wantMajor && minor >= wantMinor) {
		return interp, ""
	}
	return "", fmt.Sprintf("host python is %d.%d, payload requires %s", major, minor, constraint)
}

// InstallArchive installs a .bkg package into destRoot/<name>:
// extract to a scratch dir, validate the package manifest, select a
// feasible payload, verify its tree hash, resolve Binary/Args, run the
// plugin's own `spec` and require agreement with the manifest, then —
// and only then — move the staged directory into place. Any failure
// leaves destRoot untouched.
func InstallArchive(archivePath, destRoot string) (*Manifest, error) {
	scratch, err := os.MkdirTemp("", "brokoli-plugin-pkg-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)

	if err := extractArchive(archivePath, scratch); err != nil {
		return nil, fmt.Errorf("extract %s: %w", filepath.Base(archivePath), err)
	}
	raw, err := os.ReadFile(filepath.Join(scratch, "manifest.json")) // #nosec G304 -- inside our own scratch dir
	if err != nil {
		return nil, fmt.Errorf("package has no manifest.json at its root: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("package manifest: %w", err)
	}
	if len(m.Payloads) == 0 {
		return nil, fmt.Errorf("package manifest declares no payloads -- a plain directory plugin installs with 'brokoli plugins install <dir>' instead")
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("package manifest: %w", err)
	}

	payload, resolved, err := SelectPayload(&m)
	if err != nil {
		return nil, err
	}
	payloadDir := filepath.Join(scratch, filepath.FromSlash(payload.Path))
	gotHash, err := HashPayloadTree(payloadDir)
	if err != nil {
		return nil, fmt.Errorf("hash payload %s: %w", payload.Path, err)
	}
	if !strings.EqualFold(gotHash, payload.SHA256) {
		return nil, fmt.Errorf(
			"payload %s failed integrity verification: manifest says sha256 %s, tree hashes to %s -- refusing to install",
			payload.Path, payload.SHA256, gotHash,
		)
	}

	// Stage: payload tree becomes the plugin directory content, with the
	// resolved manifest written alongside.
	stage, err := os.MkdirTemp(filepath.Dir(destRoot), ".stage-"+m.Name+"-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	if err := copyTreeInternal(payloadDir, stage); err != nil {
		return nil, fmt.Errorf("stage payload: %w", err)
	}
	installed := m // shallow copy; rewrite launch fields
	installed.Binary = resolved.Binary
	installed.Args = resolved.Args
	if payload.Runtime == RuntimeNative {
		// Archives don't carry mode bits portably; the entrypoint's
		// executability comes from the runtime class.
		if err := os.Chmod(filepath.Join(stage, filepath.FromSlash(payload.Entrypoint)), 0o755); err != nil { // #nosec G302 -- plugin entrypoints must be executable
			return nil, fmt.Errorf("mark entrypoint executable: %w", err)
		}
	}
	if err := WriteManifest(stage, &installed); err != nil {
		return nil, err
	}

	staged, err := LoadManifest(stage)
	if err != nil {
		return nil, fmt.Errorf("staged plugin failed to load: %w", err)
	}
	if err := VerifySpec(staged); err != nil {
		return nil, err
	}

	dst := filepath.Join(destRoot, m.Name)
	if _, err := os.Stat(dst); err == nil {
		return nil, fmt.Errorf("plugin %q is already installed at %s -- remove it first with 'brokoli plugins remove %s'", m.Name, dst, m.Name)
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, dst); err != nil {
		// Cross-device fallback.
		if err := copyTreeInternal(stage, dst); err != nil {
			return nil, fmt.Errorf("install plugin: %w", err)
		}
	}
	return LoadManifest(dst)
}

// VerifySpec runs the plugin's own `spec` command and requires it to
// agree with the manifest about identity — the install-time drift check
// the protocol always promised (`spec` is documented as install-time
// capability caching) but nothing wired up until now.
func VerifySpec(m *Manifest) error {
	runner := NewRunner(m, 30*time.Second)
	out, err := runner.Spec(context.Background())
	if err != nil {
		return fmt.Errorf("plugin failed its own spec check: %w", err)
	}
	var spec struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &spec); err != nil {
		return fmt.Errorf("plugin spec output is not a JSON object: %w", err)
	}
	if spec.Name != m.Name || (spec.Version != "" && spec.Version != m.Version) {
		return fmt.Errorf(
			"spec drift: manifest says %s %s, the plugin's own spec says %s %s -- the package content and its manifest disagree",
			m.Name, m.Version, spec.Name, spec.Version,
		)
	}
	return nil
}

// copyTreeInternal is a minimal recursive copy (no symlinks — extraction
// already refused them).
func copyTreeInternal(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported file type at %s", path)
		}
		in, err := os.Open(path) // #nosec G304,G122 -- walking our own staged tree, not attacker-controlled paths
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm()) // #nosec G304 -- target derived from checked rel path
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}
