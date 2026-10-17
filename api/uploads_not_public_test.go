package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/engine"
)

// Nothing in ./uploads is served without signing in. The server used to
// expose /uploads/{file} to anyone; core writes nothing there, and a
// distribution that stores files serves them behind its own checks.
func TestUploadsDirectoryIsNotServedPublicly(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("uploads", 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("uploads", "private.txt"), []byte("tenant secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newOrgCheckStore(t)
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	us := newTestUserStore(t)
	srv := NewServer(0, s, e, nil, &AuthConfig{}, us, nil, nil)
	t.Cleanup(func() { close(srv.evictDone) })

	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/uploads/private.txt", nil))
	if strings.Contains(rec.Body.String(), "tenant secret") {
		t.Fatalf("GET /uploads/private.txt served the file: %d", rec.Code)
	}
}
