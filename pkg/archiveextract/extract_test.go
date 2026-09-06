package archiveextract

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

type tarEntry struct {
	name    string
	body    string
	typeflg byte
	link    string
}

func buildArchive(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typeflg
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: typ,
			Size:     int64(len(e.body)),
			Mode:     0o644,
			Linkname: e.link,
		}
		if typ == tar.TypeDir {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("write body: %v", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func TestExtractHappyPath(t *testing.T) {
	archive := buildArchive(t, []tarEntry{
		{name: "dir", typeflg: tar.TypeDir},
		{name: "dir/a.txt", body: "hello"},
		{name: "b.txt", body: "world"},
	})
	dest := t.TempDir()
	if err := Extract(bytes.NewReader(archive), dest, Options{}); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "dir", "a.txt"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("dir/a.txt = %q, %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(dest, "b.txt"))
	if err != nil || string(got) != "world" {
		t.Fatalf("b.txt = %q, %v", got, err)
	}
}

func TestExtractRejectsPathTraversal(t *testing.T) {
	archive := buildArchive(t, []tarEntry{{name: "../escape.txt", body: "gotcha"}})
	dest := t.TempDir()
	if err := Extract(bytes.NewReader(archive), dest, Options{}); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "escape.txt")); err == nil {
		t.Fatal("traversal entry escaped the extraction root")
	}
}

func TestExtractRejectsAbsolutePath(t *testing.T) {
	archive := buildArchive(t, []tarEntry{{name: "/etc/passwd", body: "gotcha"}})
	dest := t.TempDir()
	if err := Extract(bytes.NewReader(archive), dest, Options{}); err == nil {
		t.Fatal("expected an absolute entry path to be rejected")
	}
}

func TestExtractRejectsSymlink(t *testing.T) {
	archive := buildArchive(t, []tarEntry{{name: "link", typeflg: tar.TypeSymlink, link: "/etc/passwd"}})
	dest := t.TempDir()
	if err := Extract(bytes.NewReader(archive), dest, Options{}); err == nil {
		t.Fatal("expected a symlink entry to be rejected")
	}
}

func TestExtractEnforcesPerFileLimit(t *testing.T) {
	archive := buildArchive(t, []tarEntry{{name: "big.txt", body: "0123456789"}})
	dest := t.TempDir()
	err := Extract(bytes.NewReader(archive), dest, Options{MaxFileBytes: 5})
	if err == nil {
		t.Fatal("expected the per-file limit to reject this entry")
	}
}

func TestExtractEnforcesTotalLimit(t *testing.T) {
	archive := buildArchive(t, []tarEntry{
		{name: "a.txt", body: "01234"},
		{name: "b.txt", body: "56789"},
	})
	dest := t.TempDir()
	err := Extract(bytes.NewReader(archive), dest, Options{MaxTotalBytes: 8})
	if err == nil {
		t.Fatal("expected the total-size limit to reject this archive")
	}
}

func TestExtractEnforcesEntryCount(t *testing.T) {
	archive := buildArchive(t, []tarEntry{
		{name: "a.txt", body: "1"},
		{name: "b.txt", body: "2"},
		{name: "c.txt", body: "3"},
	})
	dest := t.TempDir()
	err := Extract(bytes.NewReader(archive), dest, Options{MaxEntries: 2})
	if err == nil {
		t.Fatal("expected the entry-count limit to reject this archive")
	}
}

func TestExtractRejectsNonGzip(t *testing.T) {
	dest := t.TempDir()
	err := Extract(bytes.NewReader([]byte("not a gzip stream")), dest, Options{})
	if err == nil {
		t.Fatal("expected a non-gzip stream to be rejected")
	}
}

func TestExtractUnlimitedOptionsAcceptsALargeishFile(t *testing.T) {
	archive := buildArchive(t, []tarEntry{{name: "a.txt", body: string(make([]byte, 1<<20))}})
	dest := t.TempDir()
	if err := Extract(bytes.NewReader(archive), dest, Options{}); err != nil {
		t.Fatalf("expected zero-value Options to mean unlimited, got: %v", err)
	}
}
