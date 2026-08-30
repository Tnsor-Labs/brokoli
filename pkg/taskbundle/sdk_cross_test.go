package taskbundle

// Cross-contract verification (ADR-031): a bundle produced by the Python
// SDK must parse, validate, and extract through the same code the server
// mounts, and its modest manifest claims must hold. Enabled by pointing
// SDK_ARCHIVE at an SDK-built archive; skipped otherwise so the Go build
// never depends on a Python installation.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSDKArchiveCrossContract(t *testing.T) {
	archivePath := os.Getenv("SDK_ARCHIVE")
	if archivePath == "" {
		t.Skip("SDK_ARCHIVE not set")
	}
	raw, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read SDK archive: %v", err)
	}

	m, err := ParseArchive(raw)
	if err != nil {
		t.Fatalf("SDK archive rejected at parse: %v", err)
	}
	if m.Format != Format {
		t.Fatalf("SDK manifest format = %q, want %q", m.Format, Format)
	}
	if m.Language != "python" {
		t.Fatalf("SDK manifest language = %q, want python", m.Language)
	}
	if len(m.Files) == 0 {
		t.Fatal("SDK manifest declares no files")
	}
	if m.Entry == "" {
		t.Fatal("SDK manifest declares no entry")
	}
	wantDigest := DigestOf(raw)
	if got := m.ArchiveSHA256; got != "" && got != wantDigest {
		t.Fatalf("SDK manifest archive_sha256 %q conflicts with the archive digest %q", got, wantDigest)
	}

	dest := t.TempDir()
	extracted, err := Extract(raw, dest)
	if err != nil {
		t.Fatalf("SDK archive rejected at extract: %v", err)
	}
	for _, f := range extracted.Files {
		p := filepath.Join(dest, filepath.FromSlash(f))
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("SDK archive: declared file %q not present after extract", f)
		}
	}
}