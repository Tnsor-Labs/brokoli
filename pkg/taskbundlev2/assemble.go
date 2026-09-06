package taskbundlev2

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

// timeZero is the zero time used for every archive header so identical
// input files produce byte-identical archives regardless of wall clock --
// the same determinism discipline as pkg/taskbundle.Assemble.
var timeZero time.Time

// Assemble builds a deterministic task-bundle/v2 archive from the given
// files (keyed by relative slash path, manifest.json excluded -- it is
// always synthesized from manifest itself) and a manifest template. It
// exists for tests and for tooling that produces bundles (the SDK/CLI is
// the production builder, per ADR-033's own scoping); output is
// reproducible -- sorted entries, zeroed timestamps, fixed modes.
//
// If manifest.Files is nil, Assemble computes it from files (path, size,
// sha256, sorted by path) -- the size/digest bookkeeping is exactly the
// kind of by-hand transcription that produced a real bug during this
// ADR's own phase 0 (fixtures with a 63-character digest instead of 64).
// A caller that explicitly sets manifest.Files (even to an empty slice)
// gets that list used as-is instead, which is what a negative test
// asserting on a manifest that lies about its own file list needs.
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

	m := *manifest
	if m.Files == nil {
		m.Files = make([]FileEntry, 0, len(paths))
		for _, p := range paths {
			sum := sha256.Sum256([]byte(files[p]))
			m.Files = append(m.Files, FileEntry{
				Path:   p,
				Size:   int64(len(files[p])),
				SHA256: hex.EncodeToString(sum[:]),
			})
		}
	}
	manifestJSON, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	into := func(p string, body []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: p, Mode: 0o600, Size: int64(len(body)), ModTime: timeZero}); err != nil {
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
		if err := tw.WriteHeader(&tar.Header{Name: d, Typeflag: tar.TypeDir, Mode: 0o700, ModTime: timeZero}); err != nil {
			return nil, err
		}
	}
	if err := into("manifest.json", manifestJSON); err != nil {
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
