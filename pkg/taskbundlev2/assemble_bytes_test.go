package taskbundlev2

// Pins the archive bytes task-bundle-v2-canonicalization.md specifies
// (#986). A second builder (an SDK's bundle plugin) must produce the same
// uncompressed tar, so a struct reorder, a changed mode or a different
// escaping here is a contract change, not a refactor.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"testing"
)

func everyFieldManifest() *Manifest {
	return &Manifest{
		Format: Format, Name: "rollup <a&b>\n \b\f\x01\x7f", InterfaceDigest: "sha256:i", SourceDigest: "sha256:s",
		Build: &Build{Builder: "brokoli-jvm/0.1.0", Attestation: "att"},
		Payloads: []Payload{
			{ID: "jvm", Runtime: "jvm", Requires: ">=17", OS: "any", Arch: "any",
				Entrypoint: Entrypoint{Module: "com.example.T", Symbol: "run"}, Effects: "pure",
				DependencyLock: "lib/lock.txt", PayloadDigest: "sha256:p"},
			{ID: "native", Runtime: "native", OS: "linux", Arch: "amd64",
				Entrypoint: Entrypoint{Executable: "bin/task", Command: []string{"bin/task"}, Args: []string{"--x"}}, Effects: "pure",
				PayloadDigest: "sha256:q", Image: &OCIImage{Digest: "sha256:img", Reference: "ghcr.io/x/y:1"}},
		},
	}
}

func readArchive(t *testing.T, archive []byte) ([]*tar.Header, map[string][]byte) {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var headers []*tar.Header
	bodies := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		headers = append(headers, h)
		bodies[h.Name] = body
	}
	return headers, bodies
}

func TestAssembleWritesTheManifestBytesTheSpecSays(t *testing.T) {
	archive, err := Assemble(map[string]string{"z/b.class": "b", "a.jar": "a", "lib/lock.txt": "l"}, everyFieldManifest())
	if err != nil {
		t.Fatal(err)
	}
	_, bodies := readArchive(t, archive)
	want := `{"format":"brokoli.task-bundle/v2","name":"rollup \u003ca\u0026b\u003e\n\u2028\b\f\u0001` + "\x7f" + `","interface_digest":"sha256:i","source_digest":"sha256:s",` +
		`"build":{"builder":"brokoli-jvm/0.1.0","attestation":"att"},` +
		`"payloads":[{"id":"jvm","runtime":"jvm","requires":"\u003e=17","os":"any","arch":"any","entrypoint":{"module":"com.example.T","symbol":"run"},"effects":"pure","dependency_lock":"lib/lock.txt","payload_digest":"sha256:p"},` +
		`{"id":"native","runtime":"native","os":"linux","arch":"amd64","entrypoint":{"executable":"bin/task","command":["bin/task"],"args":["--x"]},"effects":"pure","payload_digest":"sha256:q","image":{"digest":"sha256:img","reference":"ghcr.io/x/y:1"}}],` +
		`"files":[{"path":"a.jar","size":1,"sha256":"ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb"},` +
		`{"path":"lib/lock.txt","size":1,"sha256":"acac86c0e609ca906f632b0e2dacccb2b77d22b0621f20ebece1a4835b93f6f0"},` +
		`{"path":"z/b.class","size":1,"sha256":"3e23e8160039594a33894f6564e1b1348bbd7a0088d42c4acb73eeaed59c009d"}]}`
	if got := string(bodies["manifest.json"]); got != want {
		t.Fatalf("manifest.json:\n got %s\nwant %s", got, want)
	}

	// Optional keys are left out when empty.
	minimal := &Manifest{Format: Format, Name: "n", InterfaceDigest: "i", SourceDigest: "s",
		Payloads: []Payload{{ID: "p", Runtime: "jvm", OS: "any", Arch: "any", Entrypoint: Entrypoint{Module: "M", Symbol: "s"}, Effects: "pure", PayloadDigest: "d"}}}
	archive, err = Assemble(map[string]string{}, minimal)
	if err != nil {
		t.Fatal(err)
	}
	_, bodies = readArchive(t, archive)
	want = `{"format":"brokoli.task-bundle/v2","name":"n","interface_digest":"i","source_digest":"s","payloads":[{"id":"p","runtime":"jvm","os":"any","arch":"any","entrypoint":{"module":"M","symbol":"s"},"effects":"pure","payload_digest":"d"}],"files":[]}`
	if got := string(bodies["manifest.json"]); got != want {
		t.Fatalf("minimal manifest.json:\n got %s\nwant %s", got, want)
	}
}

func TestAssembleWritesTheArchiveLayoutTheSpecSays(t *testing.T) {
	archive, err := Assemble(map[string]string{"z/b.class": "b", "a.jar": "a", "z/a/c.class": "c"}, everyFieldManifest())
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0x1f, 0x8b, 0x08, 0x00, 0, 0, 0, 0, 0x00, 0xff}; !bytes.Equal(archive[:10], want) {
		t.Fatalf("gzip header = % x, want % x (no flags, MTIME 0, OS 255)", archive[:10], want)
	}
	headers, _ := readArchive(t, archive)
	type entry struct {
		name string
		kind byte
		mode int64
	}
	want := []entry{
		{"z", tar.TypeDir, 0o700}, {"z/a", tar.TypeDir, 0o700},
		{"manifest.json", tar.TypeReg, 0o600},
		{"a.jar", tar.TypeReg, 0o600}, {"z/a/c.class", tar.TypeReg, 0o600}, {"z/b.class", tar.TypeReg, 0o600},
	}
	if len(headers) != len(want) {
		t.Fatalf("%d entries, want %d", len(headers), len(want))
	}
	for i, h := range headers {
		if h.Name != want[i].name || h.Typeflag != want[i].kind || h.Mode != want[i].mode {
			t.Errorf("entry %d = %q type %c mode %o, want %q type %c mode %o", i, h.Name, h.Typeflag, h.Mode, want[i].name, want[i].kind, want[i].mode)
		}
		if h.ModTime.Unix() != 0 || h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("entry %q: mtime %d uid %d gid %d uname %q gname %q, want all zero", h.Name, h.ModTime.Unix(), h.Uid, h.Gid, h.Uname, h.Gname)
		}
		if h.Format != tar.FormatUSTAR {
			t.Errorf("entry %q: format %v, want USTAR", h.Name, h.Format)
		}
	}
}
