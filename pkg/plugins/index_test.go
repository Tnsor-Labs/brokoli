package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestIndexURL_DefaultAndOverride(t *testing.T) {
	t.Setenv(IndexEnvVar, "")
	if got := IndexURL(); got != DefaultIndexURL {
		t.Errorf("IndexURL with no override = %q, want default %q", got, DefaultIndexURL)
	}
	t.Setenv(IndexEnvVar, "https://mirror.example/index.json")
	if got := IndexURL(); got != "https://mirror.example/index.json" {
		t.Errorf("IndexURL with override = %q, want the override", got)
	}
}

func TestFetchIndex_ParsesCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"version": 1,
			"plugins": [
				{"name":"hello","version":"1.0.0","description":"a greeter",
				 "archive_url":"https://ex/hello-1.0.0.bkg","sha256":"abc123"},
				{"name":"csvutil","version":"0.2.0",
				 "archive_url":"https://ex/csvutil-0.2.0.bkg","sha256":"def456"}
			]
		}`))
	}))
	defer srv.Close()

	idx, err := FetchIndex(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchIndex: %v", err)
	}
	if idx.Version != 1 {
		t.Errorf("index version = %d, want 1", idx.Version)
	}
	if len(idx.Plugins) != 2 {
		t.Fatalf("got %d plugins, want 2", len(idx.Plugins))
	}
	if idx.Plugins[0].Name != "hello" || idx.Plugins[0].ArchiveURL != "https://ex/hello-1.0.0.bkg" {
		t.Errorf("first entry = %+v, want hello with its archive url", idx.Plugins[0])
	}
	if idx.Plugins[1].SHA256 != "def456" {
		t.Errorf("second entry sha256 = %q, want def456", idx.Plugins[1].SHA256)
	}
}

func TestFetchIndex_Non200IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := FetchIndex(context.Background(), srv.URL); err == nil {
		t.Fatal("expected an error for a 404 index, got nil")
	}
}

func TestFetchIndex_MalformedJSONIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version": 1, "plugins": [ this is not json`))
	}))
	defer srv.Close()

	if _, err := FetchIndex(context.Background(), srv.URL); err == nil {
		t.Fatal("expected a parse error for malformed JSON, got nil")
	}
}

func TestDownloadArchive_VerifiesDigest(t *testing.T) {
	body := []byte("pretend-this-is-a-bkg")
	sum := sha256.Sum256(body)
	wantHex := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "a.bkg")
	if err := DownloadArchive(context.Background(), srv.URL, wantHex, dest, 1<<20); err != nil {
		t.Fatalf("matching digest should succeed: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(body) {
		t.Errorf("downloaded content mismatch")
	}
}

func TestDownloadArchive_MismatchIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("actual bytes"))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "a.bkg")
	err := DownloadArchive(context.Background(), srv.URL, "0000000000000000000000000000000000000000000000000000000000000000", dest, 1<<20)
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("want ErrDigestMismatch, got %v", err)
	}
}

func TestDownloadArchive_EmptyDigestRefused(t *testing.T) {
	err := DownloadArchive(context.Background(), "http://unused.example", "", filepath.Join(t.TempDir(), "a.bkg"), 1<<20)
	if err == nil || errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("empty digest must be refused before any download, got %v", err)
	}
}

func TestDownloadArchive_Non200IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()
	err := DownloadArchive(context.Background(), srv.URL, "abc", filepath.Join(t.TempDir(), "a.bkg"), 1<<20)
	if err == nil {
		t.Fatal("expected an error for a 404 archive")
	}
}

func TestFindEntry(t *testing.T) {
	idx := &Index{Plugins: []IndexEntry{{Name: "a"}, {Name: "b"}}}
	if e := idx.FindEntry("b"); e == nil || e.Name != "b" {
		t.Errorf("FindEntry(b) = %v", e)
	}
	if e := idx.FindEntry("z"); e != nil {
		t.Errorf("FindEntry(z) = %v, want nil", e)
	}
}
