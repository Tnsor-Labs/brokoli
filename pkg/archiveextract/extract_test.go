package archiveextract

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
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

// A ".." only escapes as a whole path segment. Names that merely contain
// two dots are ordinary files and must extract (#999).
func TestExtractAcceptsDotsInsideASegment(t *testing.T) {
	names := []string{"notes..txt", "a/b..c/d", "a...b", "..hidden", "dir/x.."}
	var entries []tarEntry
	for _, n := range names {
		entries = append(entries, tarEntry{name: n, body: n})
	}
	dest := t.TempDir()
	if err := Extract(bytes.NewReader(buildArchive(t, entries)), dest, Options{}); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, n := range names {
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(n)))
		if err != nil || string(got) != n {
			t.Errorf("%s = %q, %v", n, got, err)
		}
	}
}

func TestExtractRejectsEveryEscapingName(t *testing.T) {
	for _, name := range []string{"..", "../x", "a/../../x", "a/..", "a/../b", `..\x`, `a\..\..\x`, "/etc/passwd", `\x`} {
		t.Run(name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "root")
			err := Extract(bytes.NewReader(buildArchive(t, []tarEntry{{name: name, body: "gotcha"}})), dest, Options{})
			if err == nil || !strings.Contains(err.Error(), "escapes the extraction root") {
				t.Fatalf("Extract(%q) = %v, want an escape refusal", name, err)
			}
		})
	}
}

// A second entry for the same file would replace the first on disk, so
// two readers of one archive could see different contents (#999).
func TestExtractRejectsADuplicateFile(t *testing.T) {
	for _, pair := range [][2]string{{"a.txt", "a.txt"}, {"dir/a", "dir//a"}, {"a", "./a"}} {
		t.Run(pair[0]+" then "+pair[1], func(t *testing.T) {
			archive := buildArchive(t, []tarEntry{{name: pair[0], body: "first"}, {name: pair[1], body: "second"}})
			err := Extract(bytes.NewReader(archive), t.TempDir(), Options{})
			if err == nil || !strings.Contains(err.Error(), "more than one entry named") {
				t.Fatalf("Extract = %v, want a duplicate refusal", err)
			}
		})
	}
}

func TestExtractAcceptsARepeatedDirectory(t *testing.T) {
	archive := buildArchive(t, []tarEntry{
		{name: "dir", typeflg: tar.TypeDir},
		{name: "dir", typeflg: tar.TypeDir},
		{name: "dir/a", body: "x"},
	})
	if err := Extract(bytes.NewReader(archive), t.TempDir(), Options{}); err != nil {
		t.Fatalf("Extract: %v", err)
	}
}
