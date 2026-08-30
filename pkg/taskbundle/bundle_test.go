package taskbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleManifest(mult int) *Manifest {
	return &Manifest{
		Format:        Format,
		Language:      "python",
		TaskName:      "sample",
		Entry:         "tasks.py",
		Files:         []string{"helpers.py", "tasks.py"},
		ArchiveSHA256: "", // Assemble fills nothing automatically; tests set it for the digest-check cases
	}
}

func TestAssembleIsDeterministicAndDigestAddressesIt(t *testing.T) {
	files := map[string]string{"helpers.py": "def apply(x):\n    return x * 2\n", "tasks.py": "from helpers import apply\n"}
	a, err := Assemble(files, sampleManifest(2))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Assemble(files, sampleManifest(2))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("Assemble is not deterministic: identical input produced different archives")
	}
	if DigestOf(a) != DigestOf(b) {
		t.Fatal("deterministic archives have different digests")
	}
	if !IsDigest(DigestOf(a)) {
		t.Fatalf("DigestOf output %q does not match the digest shape", DigestOf(a))
	}
	// Same files, different bytes anywhere => different digest (exercise the
	// "identical source, different build" property at the archive layer).
	diff := map[string]string{"helpers.py": "def apply(x):\n    return x * 3\n", "tasks.py": "from helpers import apply\n"}
	c, _ := Assemble(diff, sampleManifest(3))
	if bytes.Equal(a, c) || DigestOf(a) == DigestOf(c) {
		t.Fatal("different bytes must produce a different archive and digest")
	}
}

func TestAssembleAndExtractRoundTrip(t *testing.T) {
	files := map[string]string{"helpers.py": "def apply(x):\n    return x * 2\n", "tasks.py": "from helpers import apply\n"}
	archive, err := Assemble(files, sampleManifest(2))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m, err := Extract(archive, dir)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if m.Entry != "tasks.py" || len(m.Files) != 2 {
		t.Fatalf("extracted manifest mismatch: %+v", m)
	}
	for _, name := range []string{"helpers.py", "tasks.py", "manifest.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("file %s missing after extract: %v", name, err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("file %s is not regular: %v", name, info.Mode())
		}
	}
}

func TestParseArchiveRefusesTamperedDigestClaim(t *testing.T) {
	files := map[string]string{"tasks.py": "output_data = {'columns': [], 'rows': []}\n"}
	m := sampleManifest(1)
	m.Files = []string{"tasks.py"}
	m.ArchiveSHA256 = "sha256:" + strings.Repeat("0", 64) // lies about itself
	archive, err := Assemble(files, m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseArchive(archive); err == nil {
		t.Fatal("manifest whose archive_sha256 does not match the archive was accepted")
	}
}

func TestManifestValidation(t *testing.T) {
	cases := []struct {
		name string
		m    *Manifest
		want string // substring expected in the error
	}{
		{"wrong format", &Manifest{Format: "task-bundle/9", Language: "python", Entry: "t.py", Files: []string{"t.py"}}, "unsupported task bundle format"},
		{"non-python language", &Manifest{Format: Format, Language: "typescript", Entry: "t.ts", Files: []string{"t.ts"}}, "not executable"},
		{"empty entry", &Manifest{Format: Format, Language: "python", Entry: "", Files: []string{"t.py"}}, "no entry"},
		{"no files", &Manifest{Format: Format, Language: "python", Entry: "t.py"}, "no files"},
		{"entry not in files", &Manifest{Format: Format, Language: "python", Entry: "t.py", Files: []string{"other.py"}}, "not in the manifest's file list"},
		{"absolute entry", &Manifest{Format: Format, Language: "python", Entry: "/etc/passwd", Files: []string{"/etc/passwd"}}, "clean relative path"},
		{"traversal entry", &Manifest{Format: Format, Language: "python", Entry: "../secret.py", Files: []string{"../secret.py"}}, "clean relative path"},
		{"duplicate file", &Manifest{Format: Format, Language: "python", Entry: "t.py", Files: []string{"t.py", "t.py"}}, "listed twice"},
	}
	for _, tc := range cases {
		if err := tc.m.Validate(); err == nil {
			t.Errorf("%s: expected an error", tc.name)
			continue
		} else if !contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not contain %q", tc.name, err.Error(), tc.want)
		}
	}
	valid := &Manifest{Format: Format, Language: "python", Entry: "pkg/t.py", Files: []string{"pkg/helpers.py", "pkg/t.py"}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
}

func TestExtractRefusesPathTraversal(t *testing.T) {
	// Craft a gzipped tar by hand with a ../ entry — the Assemble path
	// would never create one; this proves the extractor refuses it.
	dir := t.TempDir()
	archive := mustGzippedTar(t, map[string]string{"../evil.txt": "pwned"})
	if _, err := Extract(archive, dir); err == nil {
		t.Fatal("a tar with a ../ entry was extracted")
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.txt")); err == nil {
		t.Fatal("traversal target was written")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// mustGzippedTar builds a gzipped tar from an arbitrary name→content map,
// direct from the adversary's perspective rather than through Assemble.
func mustGzippedTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), ModTime: timeZero}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}