// Package taskbundle hosts the task-bundle contract (ADR-031): the
// versioned, content-addressed project artifact a task() compiles to and
// a code node's task_bundle config references. The archive follows
// ADR-016's envelope — a gzipped tar of manifest.json plus the task's
// own files — so the server reuses the same extraction and digest
// discipline instead of inventing a second format.
//
// This package owns the format, the safe extraction, and the digest
// helpers shared by the server, its engine, and its tests. The SDK/CLI
// builds a bundle once, locally (Decision 5); the server never builds.
// What the server does here is re-verify: an uploaded archive must hash
// to the digest it claims, and what it mounts must match the manifest's
// own self-description before any bytes execute.
package taskbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Format is the task_bundle.format value this server executes (v1).
const Format = "task-bundle/1"

// MaxArchiveBytes caps an uploaded bundle before it is stored or
// extracted (Decision 6: a hard size cap enforced server-side, plus the
// same defense at materialization time).
const MaxArchiveBytes = 64 << 20 // 64 MiB

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// DigestOf returns the canonical content address of archive bytes — the
// string a code node's task_bundle.digest must equal.
func DigestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// IsDigest reports whether d is a well-formed task-bundle digest
// reference ("sha256:" followed by 64 hex chars). It does not check
// that any bytes actually hash to it — that is the upload/store
// contract.
func IsDigest(d string) bool { return digestRE.MatchString(d) }

// Manifest is the bundle's own self-description, written by the SDK
// packager and re-read by the server at mount time. The Files list is
// authoritative: the worker may import only these files (ADR-030's
// bundle-loading amendment), and the engine refuses a manifest whose
// entry is not among them.
type Manifest struct {
	// Format must be taskbundle.Format; anything else is a bundle this
	// server does not speak.
	Format string `json:"format"`

	// TaskName and Language are display/runtime metadata. Language must
	// be "python" in v1: TypeScript bundle loading is the ADR-030
	// amendment that has not shipped yet, and a bundle whose language
	// this server cannot mount must be refused here, not discovered at
	// run time.
	TaskName string `json:"task_name,omitempty"`
	Language string `json:"language"`

	// Entry is the entry module's relative slash path within the
	// archive (e.g. "tasks.py"). The engine hands this to the worker as
	// the script to execute.
	Entry string `json:"entry"`

	// Files is the authoritative, sorted list of relative slash paths of
	// every importable file in the archive (including Entry).
	Files []string `json:"files"`

	// Schema is the schema derived from the task function's type
	// information at packaging time (Decision 1: derived, never
	// hand-declared). nil or null is an honest absence — no schema is
	// recorded when the types cannot resolve to a concrete row shape.
	// The server persists and displays it; it never re-derives or
	// edits it.
	Schema json.RawMessage `json:"schema,omitempty"`

	// SourceDigest is the digest of the project files that produced this
	// bundle; BuildDigest names the build tool + options. Together they
	// make "identical source, different build" distinguishable from
	// bit-identical output. Informational on the server.
	SourceDigest string `json:"source_digest,omitempty"`
	BuildDigest  string `json:"build_digest,omitempty"`

	// ArchiveSHA256 is this archive's own digest — the same bytes the
	// IR's task_bundle.digest names. Verified against the archive when
	// it is present; a mismatch is a refused bundle.
	ArchiveSHA256 string `json:"archive_sha256"`

	// LanguageRuntime is the interpreter version constraint resolved at
	// build time (e.g. ">=3.9"). Recorded for the UI; the server's
	// python resolution is ADR-030's, as for any other python code node.
	LanguageRuntime string `json:"language_runtime,omitempty"`
}

// Validate checks that the manifest is internally consistent and within
// what this server can mount. It does not inspect the filesystem — the
// caller verifies existence of the declared files against the extraction
// root separately (Extract does).
func (m *Manifest) Validate() error {
	if m.Format != Format {
		return fmt.Errorf("unsupported task bundle format %q (this server executes %q)", m.Format, Format)
	}
	if m.Language != "python" {
		return fmt.Errorf("task bundle language %q is not executable by this server yet (v1 mounts python bundles)", m.Language)
	}
	if strings.TrimSpace(m.Entry) == "" {
		return fmt.Errorf("task bundle manifest declares no entry file")
	}
	if len(m.Files) == 0 {
		return fmt.Errorf("task bundle manifest declares no files")
	}
	entryClean, entryOK := cleanRelativePath(m.Entry)
	if !entryOK {
		return fmt.Errorf("task bundle entry %q must be a clean relative path inside the bundle", m.Entry)
	}
	seen := make(map[string]bool, len(m.Files))
	for _, f := range m.Files {
		rel, ok := cleanRelativePath(f)
		if !ok {
			return fmt.Errorf("task bundle file %q must be a clean relative path inside the bundle", f)
		}
		if seen[rel] {
			return fmt.Errorf("task bundle file %q is listed twice", rel)
		}
		seen[rel] = true
	}
	if !seen[entryClean] {
		return fmt.Errorf("task bundle entry %q is not in the manifest's file list (the file list is authoritative)", entryClean)
	}
	return nil
}

// cleanRelativePath validates a slash path as a relative path that stays
// inside the bundle root, returning the normalized slash form.
func cleanRelativePath(name string) (string, bool) {
	name = strings.ReplaceAll(name, "\\", "/")
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
	if clean == "." {
		return "", false
	}
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", false
	}
	return clean, true
}

// ParseArchive reads and validates a bundle archive's manifest without
// extracting anything. It verifies the manifest's self-consistency and,
// when ArchiveSHA256 is set, that it equals the archive's actual digest.
func ParseArchive(b []byte) (*Manifest, error) {
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("task bundle is not a gzipped tar archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var manifestRaw []byte
	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read task bundle archive: %w", err)
		}
		entries++
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			return nil, fmt.Errorf("task bundle entry %q has unsupported type %d", hdr.Name, hdr.Typeflag)
		}
		if filepath.ToSlash(filepath.Clean(filepath.FromSlash(hdr.Name))) == "manifest.json" {
			if manifestRaw != nil {
				return nil, fmt.Errorf("task bundle archive declares manifest.json more than once")
			}
			if hdr.Typeflag != tar.TypeReg {
				return nil, fmt.Errorf("task bundle manifest.json must be a regular file")
			}
			manifestRaw, err = io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return nil, fmt.Errorf("read task bundle manifest: %w", err)
			}
		} else {
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return nil, fmt.Errorf("read task bundle entry %q: %w", hdr.Name, err)
			}
		}
	}
	if manifestRaw == nil {
		return nil, fmt.Errorf("task bundle archive contains no manifest.json")
	}
	var m Manifest
	if err := json.Unmarshal(manifestRaw, &m); err != nil {
		return nil, fmt.Errorf("parse task bundle manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if m.ArchiveSHA256 != "" && m.ArchiveSHA256 != DigestOf(b) {
		return nil, fmt.Errorf("task bundle manifest archive_sha256 %q does not match the archive's actual digest %q", m.ArchiveSHA256, DigestOf(b))
	}
	return &m, nil
}

// Extract unpacks a task bundle archive into destRoot and returns its
// validated manifest. The extraction is server runtime input, so the
// same guardrails as ADR-016's plugin extraction apply: path traversal
// refused, no links/devices/fifos, per-file and total size caps, and
// every file the manifest declares must actually be present afterward.
func Extract(b []byte, destRoot string) (*Manifest, error) {
	if len(b) > MaxArchiveBytes {
		return nil, fmt.Errorf("task bundle archive %d bytes exceeds the %d-byte cap", len(b), int64(MaxArchiveBytes))
	}
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("task bundle is not a gzipped tar archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read task bundle archive: %w", err)
		}
		name := filepath.FromSlash(hdr.Name)
		if filepath.IsAbs(name) || strings.Contains(name, "..") {
			return nil, fmt.Errorf("task bundle entry %q escapes the extraction root", hdr.Name)
		}
		target := filepath.Join(destRoot, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return nil, err
			}
		case tar.TypeReg:
			if hdr.Size > MaxArchiveBytes {
				return nil, fmt.Errorf("task bundle entry %q exceeds the %d-byte file limit", hdr.Name, int64(MaxArchiveBytes))
			}
			total += hdr.Size
			if total > MaxArchiveBytes {
				return nil, fmt.Errorf("task bundle archive exceeds the %d-byte total limit", int64(MaxArchiveBytes))
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return nil, err
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
			if err != nil {
				return nil, err
			}
			_, copyErr := io.Copy(out, io.LimitReader(tr, MaxArchiveBytes+1))
			if closeErr := out.Close(); closeErr != nil && copyErr == nil {
				copyErr = closeErr
			}
			if copyErr != nil {
				return nil, copyErr
			}
		default:
			return nil, fmt.Errorf("task bundle entry %q has unsupported type %d", hdr.Name, hdr.Typeflag)
		}
	}

	m, err := ParseArchive(b)
	if err != nil {
		return nil, err
	}
	for _, f := range m.Files {
		info, err := os.Stat(filepath.Join(destRoot, filepath.FromSlash(f)))
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("task bundle manifest declares file %q but it is not present in the archive", f)
		}
	}
	return m, nil
}

// Assemble builds a deterministic task-bundle archive from the given
// files (keyed by relative slash path) and manifest fields. It exists for
// tests and for tooling that produces bundles (the SDK/CLI is the
// production builder); the output is reproducible — sorted entries, zeroed
// timestamps, fixed modes — which is what makes "content-addressed" a
// testable claim rather than a wish.
func Assemble(files map[string]string, manifest *Manifest) ([]byte, error) {
	paths := make([]string, 0, len(files))
	for p := range files {
		rel, ok := cleanRelativePath(p)
		if !ok {
			return nil, fmt.Errorf("bundle file %q must be a clean relative path", p)
		}
		paths = append(paths, rel)
	}
	sort.Strings(paths)

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	into := func(p string, body []byte) error {
		hdr := &tar.Header{
			Name:    p,
			Mode:    0o600,
			Size:    int64(len(body)),
			ModTime: timeZero,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(body)
		return err
	}
	dirs := map[string]bool{}
	for _, p := range paths {
		for dir := filepath.ToSlash(filepath.Dir(p)); dir != "." && !dirs[dir]; dir = filepath.ToSlash(filepath.Dir(dir)) {
			dirs[dir] = true
		}
	}
	dirNames := make([]string, 0, len(dirs))
	for d := range dirs {
		dirNames = append(dirNames, d)
	}
	sort.Strings(dirNames)
	for _, d := range dirNames {
		hdr := &tar.Header{Name: d, Typeflag: tar.TypeDir, Mode: 0o700, ModTime: timeZero}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
	}
	if err := into("manifest.json", mustJSON(manifest)); err != nil {
		return nil, err
	}
	for _, p := range paths {
		if err := into(p, []byte(files[p])); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// timeZero is the zero time used for every archive header so identical
// input files produce byte-identical archives regardless of wall clock.
var timeZero time.Time

func mustJSON(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}