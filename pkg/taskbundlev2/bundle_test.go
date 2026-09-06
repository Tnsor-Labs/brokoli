package taskbundlev2

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

var validDigest = "sha256:" + strings.Repeat("0", 62) + "aa"

func validManifest() *Manifest {
	return &Manifest{
		Format:          Format,
		Name:            "score-model",
		InterfaceDigest: validDigest,
		SourceDigest:    validDigest,
		Payloads: []Payload{
			{
				ID:      "python-any",
				Runtime: RuntimePython,
				OS:      "any",
				Arch:    "any",
				Entrypoint: Entrypoint{
					Module: "tasks.score",
					Symbol: "run",
				},
				Effects:       EffectPure,
				PayloadDigest: validDigest,
			},
		},
		Files: []FileEntry{
			{Path: "manifest.json", Size: 0, SHA256: strings.Repeat("0", 62) + "aa"},
			{Path: "tasks/score.py", Size: 5, SHA256: fileDigest("hello")},
		},
	}
}

func fileDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func TestValidateAcceptsAWellFormedManifest(t *testing.T) {
	m := validManifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRejectsWrongFormat(t *testing.T) {
	m := validManifest()
	m.Format = "brokoli.task-bundle/v99"
	if err := m.Validate(); err == nil {
		t.Fatal("expected an unrecognized format to be rejected")
	}
}

func TestValidateRejectsMissingPayloads(t *testing.T) {
	m := validManifest()
	m.Payloads = nil
	if err := m.Validate(); err == nil {
		t.Fatal("expected zero payloads to be rejected")
	}
}

func TestValidateRejectsDuplicatePayloadID(t *testing.T) {
	m := validManifest()
	m.Payloads = append(m.Payloads, m.Payloads[0])
	if err := m.Validate(); err == nil {
		t.Fatal("expected a duplicate payload id to be rejected")
	}
}

func TestValidateRejectsContainerPayloadWithoutImage(t *testing.T) {
	m := validManifest()
	m.Payloads[0].Runtime = RuntimeContainer
	m.Payloads[0].Entrypoint = Entrypoint{Command: []string{"run"}}
	if err := m.Validate(); err == nil {
		t.Fatal("expected a container payload with no image to be rejected")
	}
	m.Payloads[0].Image = &OCIImage{Digest: validDigest}
	if err := m.Validate(); err != nil {
		t.Fatalf("expected a container payload with an image to validate, got: %v", err)
	}
}

func TestEntrypointValidateRejectsAmbiguousGrammar(t *testing.T) {
	m := validManifest()
	m.Payloads[0].Entrypoint.Executable = "run.sh" // now both managed and native are set
	if err := m.Validate(); err == nil {
		t.Fatal("expected an entrypoint satisfying two grammars at once to be rejected")
	}
}

func TestEntrypointValidateRejectsPartialManagedGrammar(t *testing.T) {
	m := validManifest()
	m.Payloads[0].Entrypoint = Entrypoint{Module: "tasks.score"} // no symbol
	if err := m.Validate(); err == nil {
		t.Fatal("expected module without symbol to be rejected")
	}
}

func TestValidateRejectsPathTraversalFileEntry(t *testing.T) {
	m := validManifest()
	m.Files = append(m.Files, FileEntry{Path: "../escape.py", SHA256: fileDigest("x")})
	if err := m.Validate(); err == nil {
		t.Fatal("expected a traversal file path to be rejected")
	}
}

func TestSelectPythonPayloadPrefersMatchingHost(t *testing.T) {
	m := validManifest()
	got, err := SelectPythonPayload(m)
	if err != nil {
		t.Fatalf("SelectPythonPayload: %v", err)
	}
	if got.ID != "python-any" {
		t.Errorf("selected %q, want python-any", got.ID)
	}
}

func TestSelectPythonPayloadSkipsNonPythonRuntime(t *testing.T) {
	m := validManifest()
	m.Payloads[0].Runtime = RuntimeNode
	m.Payloads[0].Entrypoint = Entrypoint{Executable: "index.js"}
	if _, err := SelectPythonPayload(m); err == nil {
		t.Fatal("expected no python payload to be selectable")
	}
}

func TestSelectPythonPayloadRejectsWrongPlatform(t *testing.T) {
	m := validManifest()
	m.Payloads[0].OS = "plan9"
	m.Payloads[0].Arch = "mips"
	if _, err := SelectPythonPayload(m); err == nil {
		t.Fatal("expected a platform-mismatched payload to be rejected")
	}
}

// buildTaskBundleV2Archive constructs a real gzipped tar containing
// manifest.json plus every file the manifest declares, with each file's
// content chosen so its sha256 matches the manifest's declared digest --
// exactly what a real SDK-built bundle looks like, and what Extract must
// verify against.
func buildTaskBundleV2Archive(t *testing.T, m *Manifest, fileContents map[string]string) []byte {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(name string, content []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(content)), Mode: 0o644}); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatalf("write body %s: %v", name, err)
		}
	}
	write("manifest.json", raw)
	for path, content := range fileContents {
		write(path, []byte(content))
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func TestExtractRoundTripsAWellFormedBundle(t *testing.T) {
	m := validManifest()
	m.Files = []FileEntry{
		{Path: "tasks/score.py", Size: 5, SHA256: fileDigest("hello")},
	}
	archive := buildTaskBundleV2Archive(t, m, map[string]string{"tasks/score.py": "hello"})
	dest := t.TempDir()
	got, err := Extract(archive, dest)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got.Name != m.Name {
		t.Errorf("Name = %q, want %q", got.Name, m.Name)
	}
}

func TestExtractRejectsDigestMismatch(t *testing.T) {
	m := validManifest()
	m.Files = []FileEntry{
		{Path: "tasks/score.py", Size: 5, SHA256: fileDigest("hello")},
	}
	// Archive content doesn't match the declared digest.
	archive := buildTaskBundleV2Archive(t, m, map[string]string{"tasks/score.py": "wrong"})
	dest := t.TempDir()
	if _, err := Extract(archive, dest); err == nil {
		t.Fatal("expected a content/digest mismatch to be rejected")
	}
}

func TestExtractRejectsMissingDeclaredFile(t *testing.T) {
	m := validManifest()
	m.Files = []FileEntry{
		{Path: "tasks/score.py", Size: 5, SHA256: fileDigest("hello")},
		{Path: "tasks/missing.py", Size: 1, SHA256: fileDigest("x")},
	}
	archive := buildTaskBundleV2Archive(t, m, map[string]string{"tasks/score.py": "hello"})
	dest := t.TempDir()
	if _, err := Extract(archive, dest); err == nil {
		t.Fatal("expected a manifest-declared but absent file to be rejected")
	}
}

func TestExtractRejectsInvalidManifest(t *testing.T) {
	m := validManifest()
	m.Format = "wrong"
	archive := buildTaskBundleV2Archive(t, m, nil)
	dest := t.TempDir()
	if _, err := Extract(archive, dest); err == nil {
		t.Fatal("expected an invalid manifest to be rejected")
	}
}
