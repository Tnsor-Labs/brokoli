// Package archiveextract holds the one gzipped-tar extraction guard this
// codebase needs in three places (pkg/plugins package archives, ADR-016;
// pkg/taskbundle task-bundle/1 archives, ADR-031; pkg/taskbundlev2
// task-bundle/v2 archives, ADR-033): refuse path traversal, refuse
// anything that isn't a plain file or directory, and cap both individual
// entries and the archive as a whole before any byte reaches disk.
//
// task-bundle/1 (pkg/taskbundle/bundle.go's own Extract) is frozen per
// ADR-035 Decision 1 and deliberately keeps its independent
// implementation rather than migrating — only pkg/plugins and the new
// pkg/taskbundlev2 build on this package.
package archiveextract

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Options bounds one Extract call. Zero means unlimited for MaxFileBytes
// and MaxTotalBytes; zero means uncapped for MaxEntries too — a caller
// that doesn't set a field is choosing not to enforce that guard, not
// getting one for free.
type Options struct {
	MaxFileBytes  int64
	MaxTotalBytes int64
	MaxEntries    int
}

// Extract unpacks a gzipped tar stream into destRoot. Every entry path
// must resolve inside destRoot; only regular files and directories are
// accepted (symlinks, devices, and fifos are refused — the archive is
// untrusted input, and none of ADR-016's or ADR-033's package formats
// need them).
func Extract(r io.Reader, destRoot string, opts Options) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("not a gzip archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	fileLimit := opts.MaxFileBytes
	if fileLimit <= 0 {
		fileLimit = math.MaxInt64
	}

	var total int64
	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		entries++
		if opts.MaxEntries > 0 && entries > opts.MaxEntries {
			return fmt.Errorf("archive has more than %d entries", opts.MaxEntries)
		}
		name := filepath.FromSlash(hdr.Name)
		if filepath.IsAbs(name) || strings.Contains(name, "..") {
			return fmt.Errorf("archive entry %q escapes the extraction root", hdr.Name)
		}
		target := filepath.Join(destRoot, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			if hdr.Size > fileLimit {
				return fmt.Errorf("archive entry %q exceeds the %d-byte file limit", hdr.Name, opts.MaxFileBytes)
			}
			total += hdr.Size
			if opts.MaxTotalBytes > 0 && total > opts.MaxTotalBytes {
				return fmt.Errorf("archive exceeds the %d-byte total limit", opts.MaxTotalBytes)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- target is traversal-checked above and joined under the caller-chosen destRoot
			if err != nil {
				return err
			}
			// LimitReader as defense in depth against a lying header.
			limit := fileLimit
			if limit < math.MaxInt64 {
				limit++
			}
			_, copyErr := io.Copy(out, io.LimitReader(tr, limit)) // #nosec G110 -- bounded by the per-file and total caps above
			if closeErr := out.Close(); closeErr != nil && copyErr == nil {
				copyErr = closeErr
			}
			if copyErr != nil {
				return copyErr
			}
		default:
			// Symlinks, devices, fifos: nothing a package payload needs,
			// everything an attacker wants.
			return fmt.Errorf("archive entry %q has unsupported type %d", hdr.Name, hdr.Typeflag)
		}
	}
}
