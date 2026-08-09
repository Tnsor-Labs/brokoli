package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestRoundTrip(t *testing.T) {
	t.Run("artifact", func(t *testing.T) {
		st := NewLocalDiskStore(t.TempDir())
		ref, err := st.Put(context.Background(), "run-1", strings.NewReader("hello"), PutOptions{MediaType: "text/plain"})
		if err != nil {
			t.Fatal(err)
		}
		data, err := MarshalManifest(NewArtifactManifest(ref))
		if err != nil {
			t.Fatal(err)
		}
		got, err := UnmarshalManifest(data)
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != KindArtifact || got.Dataset != nil {
			t.Fatalf("kind=%q dataset=%v, want an artifact manifest", got.Kind, got.Dataset)
		}
		if *got.Artifact != *ref {
			t.Errorf("round trip changed the reference:\n got %+v\nwant %+v", *got.Artifact, *ref)
		}
	})

	t.Run("dataset", func(t *testing.T) {
		st := NewLocalDiskStore(t.TempDir())
		base, err := st.Put(context.Background(), "run-1", strings.NewReader(`{"a":1}`+"\n"), PutOptions{MediaType: MediaTypeNDJSON})
		if err != nil {
			t.Fatal(err)
		}
		ds := &DatasetRef{ArtifactRef: *base, Format: FormatNDJSON, Columns: []string{"a"}, RowCount: 1}
		data, err := MarshalManifest(NewDatasetManifest(ds))
		if err != nil {
			t.Fatal(err)
		}
		got, err := UnmarshalManifest(data)
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != KindDataset || got.Artifact != nil {
			t.Fatalf("kind=%q artifact=%v, want a dataset manifest", got.Kind, got.Artifact)
		}
		if got.Dataset.RowCount != 1 || got.Dataset.Format != FormatNDJSON {
			t.Errorf("dataset fields lost: %+v", got.Dataset)
		}
		// The embedded artifact fields must survive too — a DatasetRef is
		// useless if it round-trips its row count but loses its URI.
		if got.Dataset.URI != base.URI || got.Dataset.Checksum != base.Checksum {
			t.Errorf("embedded artifact fields lost: %+v", got.Dataset.ArtifactRef)
		}
	})
}

// A manifest written by a newer build must be refused, not read with this
// build's assumptions — it points at data a pipeline is about to consume.
func TestUnmarshalManifest_RejectsUnknownVersion(t *testing.T) {
	raw := []byte(`{"version":99,"kind":"artifact","artifact":{"uri":"local://ab/cd","media_type":"text/plain","size_bytes":5,"checksum":"sha256:` + strings.Repeat("a", 64) + `"}}`)
	_, err := UnmarshalManifest(raw)
	if err == nil {
		t.Fatal("accepted a manifest at version 99")
	}
	if !strings.Contains(err.Error(), "unsupported manifest version") {
		t.Errorf("error should name the version problem, got: %v", err)
	}
}

func TestManifestValidate_RejectsMalformed(t *testing.T) {
	good := &ArtifactRef{URI: "local://ab/cd", MediaType: "text/plain", SizeBytes: 1, Checksum: "sha256:" + strings.Repeat("a", 64)}
	cases := []struct {
		name string
		m    *Manifest
		want string
	}{
		{"kind with no reference", &Manifest{Version: ManifestVersion, Kind: KindArtifact}, "no artifact reference"},
		{"unknown kind", &Manifest{Version: ManifestVersion, Kind: "scalar", Artifact: good}, "unknown manifest kind"},
		{"both references set", &Manifest{Version: ManifestVersion, Kind: KindArtifact, Artifact: good, Dataset: &DatasetRef{ArtifactRef: *good, Format: FormatNDJSON}}, "also carries a dataset"},
		{"no URI", &Manifest{Version: ManifestVersion, Kind: KindArtifact, Artifact: &ArtifactRef{Checksum: good.Checksum}}, "no URI"},
		{"bad checksum", &Manifest{Version: ManifestVersion, Kind: KindArtifact, Artifact: &ArtifactRef{URI: "local://ab/cd", Checksum: "md5:abc"}}, "not a sha256"},
		{"dataset without format", &Manifest{Version: ManifestVersion, Kind: KindDataset, Dataset: &DatasetRef{ArtifactRef: *good}}, "no format"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.m.Validate()
			if err == nil {
				t.Fatalf("accepted %s", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should mention %q", err, c.want)
			}
		})
	}
}

// MarshalManifest must refuse to write something it could not read back.
func TestMarshalManifest_RefusesInvalid(t *testing.T) {
	if _, err := MarshalManifest(&Manifest{Version: ManifestVersion, Kind: KindArtifact}); err == nil {
		t.Fatal("wrote a manifest with no reference in it")
	}
}

func TestLocalDiskStore_PutOpenRoundTrip(t *testing.T) {
	st := NewLocalDiskStore(t.TempDir())
	content := strings.Repeat("payload-", 5000) // ~40KB, past a single buffer

	ref, err := st.Put(context.Background(), "run-1", strings.NewReader(content), PutOptions{MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if ref.SizeBytes != int64(len(content)) {
		t.Errorf("size = %d, want %d", ref.SizeBytes, len(content))
	}
	if ref.MediaType != "text/plain" {
		t.Errorf("media type = %q", ref.MediaType)
	}
	if err := ref.Validate(); err != nil {
		t.Errorf("Put produced a reference that does not validate: %v", err)
	}

	rc, err := st.Open(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("read back %d bytes, want %d", len(got), len(content))
	}
}

// An empty artifact is a legitimate result — a query that matched nothing,
// a file that is genuinely empty — and must not be confused with absence.
func TestLocalDiskStore_EmptyContent(t *testing.T) {
	st := NewLocalDiskStore(t.TempDir())
	ref, err := st.Put(context.Background(), "run-1", strings.NewReader(""), PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ref.SizeBytes != 0 {
		t.Errorf("size = %d, want 0", ref.SizeBytes)
	}
	rc, err := st.Open(context.Background(), ref)
	if err != nil {
		t.Fatalf("could not read back an empty artifact: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if len(got) != 0 {
		t.Errorf("read %d bytes from an empty artifact", len(got))
	}
}

func TestLocalDiskStore_DefaultsMediaType(t *testing.T) {
	st := NewLocalDiskStore(t.TempDir())
	ref, err := st.Put(context.Background(), "run-1", strings.NewReader("x"), PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ref.MediaType != MediaTypeOctetStream {
		t.Errorf("media type = %q, want %q", ref.MediaType, MediaTypeOctetStream)
	}
}

// Identical content in one namespace is stored once. This is what makes a
// pipeline that produces the same result twice cheap rather than doubling.
func TestLocalDiskStore_DeduplicatesWithinNamespace(t *testing.T) {
	base := t.TempDir()
	st := NewLocalDiskStore(base)

	a, err := st.Put(context.Background(), "run-1", strings.NewReader("same bytes"), PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Put(context.Background(), "run-1", strings.NewReader("same bytes"), PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if a.URI != b.URI {
		t.Errorf("identical content produced two URIs:\n  %s\n  %s", a.URI, b.URI)
	}

	blobs := countBlobs(t, base)
	if blobs != 1 {
		t.Errorf("stored %d blobs for identical content, want 1", blobs)
	}
}

// Namespaces do not share storage, so one run's retention can never delete
// bytes another run still points at.
func TestLocalDiskStore_NamespacesAreIsolated(t *testing.T) {
	base := t.TempDir()
	st := NewLocalDiskStore(base)

	a, err := st.Put(context.Background(), "run-1", strings.NewReader("shared"), PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Put(context.Background(), "run-2", strings.NewReader("shared"), PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if a.URI == b.URI {
		t.Fatal("the same content in two namespaces produced one URI — deleting one run would take the other's data")
	}

	if err := st.DeleteNamespace(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Open(context.Background(), a); !errors.Is(err, ErrNotFound) {
		t.Errorf("run-1's blob should be gone, got %v", err)
	}
	rc, err := st.Open(context.Background(), b)
	if err != nil {
		t.Fatalf("run-2's blob was collateral damage: %v", err)
	}
	rc.Close()
}

func TestLocalDiskStore_DeleteNamespaceIsIdempotent(t *testing.T) {
	st := NewLocalDiskStore(t.TempDir())
	if err := st.DeleteNamespace(context.Background(), "never-written"); err != nil {
		t.Errorf("deleting a namespace that was never written should be a no-op, got %v", err)
	}
}

func TestLocalDiskStore_OpenMissingIsNotFound(t *testing.T) {
	st := NewLocalDiskStore(t.TempDir())
	ref := &ArtifactRef{URI: "local://" + strings.Repeat("ab", 32) + "/" + strings.Repeat("cd", 32), Checksum: "sha256:" + strings.Repeat("e", 64)}
	_, err := st.Open(context.Background(), ref)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// Corruption must be distinguishable from absence: they call for different
// responses, and the whole point of storing a checksum is to refuse to hand
// altered bytes to a node as its input.
func TestLocalDiskStore_DetectsCorruption(t *testing.T) {
	base := t.TempDir()
	st := NewLocalDiskStore(base)
	ref, err := st.Put(context.Background(), "run-1", strings.NewReader("original content"), PutOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Tamper with the stored bytes behind the store's back.
	var blob string
	_ = filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".blob") {
			blob = p
		}
		return nil
	})
	if blob == "" {
		t.Fatal("could not find the stored blob")
	}
	if err := os.WriteFile(blob, []byte("tampered content"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = st.Open(context.Background(), ref)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want ErrChecksumMismatch, got %v", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("corruption must not be reported as absence")
	}
}

// Namespaces come from run IDs and, elsewhere in this codebase, from
// user-controlled pipeline JSON. None of it may reach the filesystem path.
func TestLocalDiskStore_NamespaceCannotEscapeBaseDir(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside-marker")
	st := NewLocalDiskStore(filepath.Join(base, "store"))

	for _, ns := range []string{"../../etc/passwd", "..", "a/b/../../../c", "\x00null"} {
		if _, err := st.Put(context.Background(), ns, strings.NewReader("x"), PutOptions{}); err != nil {
			continue // refusing outright is also fine
		}
		if _, err := os.Stat(outside); err == nil {
			t.Fatalf("namespace %q escaped the base directory", ns)
		}
	}

	// Everything written must live under the store root.
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "store" {
			t.Errorf("namespace escaped: unexpected entry %q beside the store root", e.Name())
		}
	}
}

// A reference from another backend must be refused rather than resolved
// against local disk on a hopeful reading of its URI.
func TestLocalDiskStore_RejectsForeignURI(t *testing.T) {
	st := NewLocalDiskStore(t.TempDir())
	for _, uri := range []string{
		"s3://bucket/key",
		"local://not-hex/" + strings.Repeat("a", 64),
		"local://" + strings.Repeat("ab", 32),
		"local://../../etc/passwd",
		"",
	} {
		ref := &ArtifactRef{URI: uri, Checksum: "sha256:" + strings.Repeat("a", 64)}
		if _, err := st.Open(context.Background(), ref); err == nil {
			t.Errorf("accepted URI %q", uri)
		}
	}
}

func TestLocalDiskStore_PutRejectsEmptyNamespace(t *testing.T) {
	st := NewLocalDiskStore(t.TempDir())
	if _, err := st.Put(context.Background(), "", strings.NewReader("x"), PutOptions{}); err == nil {
		t.Error("accepted an empty namespace")
	}
}

// A cancelled context must stop the write rather than leave a blob behind.
func TestLocalDiskStore_HonoursContextCancellation(t *testing.T) {
	base := t.TempDir()
	st := NewLocalDiskStore(base)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := st.Put(ctx, "run-1", strings.NewReader("x"), PutOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Put: want context.Canceled, got %v", err)
	}
	if n := countBlobs(t, base); n != 0 {
		t.Errorf("a cancelled Put left %d blobs behind", n)
	}
}

// A failed write must not leave its partial temp file lying around.
func TestLocalDiskStore_CleansUpAfterReadFailure(t *testing.T) {
	base := t.TempDir()
	st := NewLocalDiskStore(base)

	_, err := st.Put(context.Background(), "run-1", io.MultiReader(
		strings.NewReader("some bytes"),
		&failingReader{},
	), PutOptions{})
	if err == nil {
		t.Fatal("Put succeeded despite a failing reader")
	}

	var leftovers []string
	_ = filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			leftovers = append(leftovers, filepath.Base(p))
		}
		return nil
	})
	if len(leftovers) != 0 {
		t.Errorf("failed write left files behind: %v", leftovers)
	}
}

type failingReader struct{}

func (f *failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

// The reference is the contract with future backends, so its JSON field
// names are part of the format and should not drift silently.
func TestArtifactRef_JSONFieldNames(t *testing.T) {
	data, err := json.Marshal(&ArtifactRef{URI: "local://a/b", Checksum: "sha256:x"})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"uri", "media_type", "size_bytes", "checksum", "created_at"} {
		if !bytes.Contains(data, []byte(`"`+field+`"`)) {
			t.Errorf("field %q missing from serialized reference: %s", field, data)
		}
	}
}

func countBlobs(t *testing.T, base string) int {
	t.Helper()
	n := 0
	_ = filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".blob") {
			n++
		}
		return nil
	})
	return n
}
