package api

// Tests for the plugin management API (#110 M2). They drive real
// .bkg archives through the handler against a real Manager rooted in a
// temp dir, and assert the hot-reload, archive round-trip, and error
// shapes. The Python end-to-end path is skipped where python3 is absent.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/plugins"
	"github.com/go-chi/chi/v5"
)

func pythonAvailable() bool {
	_, err := exec.LookPath("python3")
	return err == nil
}

// helloPyArchive builds a .bkg from the plugins package testdata sample.
func helloPyArchive(t *testing.T) []byte {
	t.Helper()
	payloadSrc := filepath.Join("..", "pkg", "plugins", "testdata", "hello-py", "payload")
	hash, err := plugins.HashPayloadTree(payloadSrc)
	if err != nil {
		t.Fatal(err)
	}
	m := plugins.Manifest{
		ProtocolVersion:  1,
		Name:             "hello-py",
		Version:          "0.1.0",
		PackagingVersion: 1,
		NodeTypes:        []plugins.NodeTypeDecl{{Type: "source_hello_py", Kind: plugins.KindSource}},
		Payloads: []plugins.Payload{{
			Runtime: plugins.RuntimePython, OS: "any", Arch: "any",
			Path: "payload", Entrypoint: "main.py",
			Requires: map[string]string{"python": ">=3.8"}, SHA256: hash,
		}},
	}
	mj, _ := json.Marshal(&m)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(name string, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.json", mj)
	filepath.Walk(payloadSrc, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.Mode().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(payloadSrc, path)
		data, _ := os.ReadFile(path)
		write("payload/"+filepath.ToSlash(rel), data)
		return nil
	})
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func newPluginHandler(t *testing.T) (*PluginHandler, *plugins.Manager) {
	t.Helper()
	mgr, err := plugins.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewPluginHandler(mgr), mgr
}

func TestPluginInstallListRemoveLifecycle(t *testing.T) {
	if !pythonAvailable() {
		t.Skip("python3 not on PATH")
	}
	h, mgr := newPluginHandler(t)

	// Install from a raw-body upload.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/plugins", bytes.NewReader(helloPyArchive(t)))
	h.Install(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("install status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Hot reload: node type is resolvable immediately, no restart.
	if !mgr.CanHandle("source_hello_py") {
		t.Fatal("installed node type not hot-reloaded into the manager")
	}

	// List shows it, marked packaged with a digest.
	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/plugins", nil))
	var listed []pluginDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Name != "hello-py" || !listed[0].Packaged || listed[0].ArchiveSHA == "" {
		t.Fatalf("unexpected list: %+v", listed)
	}

	// Archive round-trips with the advertised digest.
	rec = httptest.NewRecorder()
	areq := withURLParam(httptest.NewRequest(http.MethodGet, "/api/plugins/hello-py/archive", nil), "name", "hello-py")
	h.Archive(rec, areq)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Content-SHA256") != listed[0].ArchiveSHA {
		t.Fatalf("archive status=%d sha=%q want %q", rec.Code, rec.Header().Get("X-Content-SHA256"), listed[0].ArchiveSHA)
	}
	if len(rec.Body.Bytes()) == 0 {
		t.Fatal("archive body empty")
	}

	// Remove hot-reloads it away.
	rec = httptest.NewRecorder()
	rreq := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/plugins/hello-py", nil), "name", "hello-py")
	h.Remove(rec, rreq)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove status=%d body=%s", rec.Code, rec.Body.String())
	}
	if mgr.CanHandle("source_hello_py") {
		t.Fatal("node type still resolvable after remove")
	}
}

func TestPluginInstallRejectsTamperedArchive(t *testing.T) {
	h, _ := newPluginHandler(t)
	archive := helloPyArchive(t)
	// Corrupt a byte deep in the gzip stream.
	archive[len(archive)/2] ^= 0xff
	rec := httptest.NewRecorder()
	h.Install(rec, httptest.NewRequest(http.MethodPost, "/api/plugins", bytes.NewReader(archive)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("tampered archive status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPluginArchiveMissingIsNotFound(t *testing.T) {
	h, _ := newPluginHandler(t)
	rec := httptest.NewRecorder()
	req := withURLParam(httptest.NewRequest(http.MethodGet, "/api/plugins/ghost/archive", nil), "name", "ghost")
	h.Archive(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rec.Code)
	}
}

func TestPluginArchiveConditionalGET(t *testing.T) {
	if !pythonAvailable() {
		t.Skip("python3 not on PATH")
	}
	h, _ := newPluginHandler(t)
	install := httptest.NewRecorder()
	h.Install(install, httptest.NewRequest(http.MethodPost, "/api/plugins", bytes.NewReader(helloPyArchive(t))))
	var dto pluginDTO
	json.Unmarshal(install.Body.Bytes(), &dto)

	req := withURLParam(httptest.NewRequest(http.MethodGet, "/api/plugins/hello-py/archive", nil), "name", "hello-py")
	req.Header.Set("If-None-Match", `"`+dto.ArchiveSHA+`"`)
	rec := httptest.NewRecorder()
	h.Archive(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("conditional GET status=%d, want 304", rec.Code)
	}
}

func TestPluginNilManagerIs503(t *testing.T) {
	h := NewPluginHandler(nil)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/plugins", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", rec.Code)
	}
}

func withURLParam(r *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestPluginIndex_BrowsesCatalog(t *testing.T) {
	idxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":1,"plugins":[` +
			`{"name":"hello","version":"1.0.0","archive_url":"https://ex/hello.bkg","sha256":"abc"}]}`))
	}))
	defer idxSrv.Close()
	t.Setenv(plugins.IndexEnvVar, idxSrv.URL)

	h, _ := newPluginHandler(t)
	rec := httptest.NewRecorder()
	h.Index(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/index", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200: %s", rec.Code, rec.Body.String())
	}
	var idx plugins.Index
	if err := json.Unmarshal(rec.Body.Bytes(), &idx); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	if len(idx.Plugins) != 1 || idx.Plugins[0].Name != "hello" {
		t.Errorf("index = %+v, want the one hello entry", idx.Plugins)
	}
}

func TestPluginIndex_FetchFailureIs502(t *testing.T) {
	// Point the override at a closed server so the fetch fails.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := dead.URL
	dead.Close()
	t.Setenv(plugins.IndexEnvVar, url)

	h, _ := newPluginHandler(t)
	rec := httptest.NewRecorder()
	h.Index(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/index", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502", rec.Code)
	}
}

func TestPluginIndex_NilManagerIs503(t *testing.T) {
	h := NewPluginHandler(nil)
	rec := httptest.NewRecorder()
	h.Index(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/index", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", rec.Code)
	}
}
