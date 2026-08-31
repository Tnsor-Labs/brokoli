package api

// Tests for the task-bundle API (ADR-031): upload verifies digest,
// manifest validity, and tenant scoping before a single byte persists;
// fetch never crosses an org boundary.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/taskbundle"
	"github.com/Tnsor-Labs/brokoli/store"
)

func newTaskBundleTestHandler(t *testing.T) *TaskBundleHandler {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "taskbundles.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return NewTaskBundleHandler(s)
}

func taskBundleRequestWithOrg(method, path string, body []byte, orgID string) *http.Request {
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
	}
	ctx := context.WithValue(r.Context(), OrgIDContextKey{}, orgID)
	return r.WithContext(ctx)
}

func validTestBundle(t *testing.T) []byte {
	t.Helper()
	m := &taskbundle.Manifest{
		Format: taskbundle.Format, Language: "python", TaskName: "sample",
		Entry: "tasks.py", Files: []string{"tasks.py"},
	}
	archive, err := taskbundle.Assemble(map[string]string{"tasks.py": "output_data = {}\n"}, m)
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func TestTaskBundleUploadFetchRoundTrip(t *testing.T) {
	h := newTaskBundleTestHandler(t)
	archive := validTestBundle(t)
	digest := taskbundle.DigestOf(archive)
	path := "/api/task-bundles/" + digest

	req := withURLParam(taskBundleRequestWithOrg(http.MethodPost, path, archive, "org-a"), "digest", digest)
	rec := httptest.NewRecorder()
	h.Upload(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first upload = %d, body %s", rec.Code, rec.Body.String())
	}

	// A byte-identical re-upload is a no-op, not a second stored copy.
	req2 := withURLParam(taskBundleRequestWithOrg(http.MethodPost, path, archive, "org-a"), "digest", digest)
	rec2 := httptest.NewRecorder()
	h.Upload(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("re-upload = %d, want 200 unchanged: %s", rec2.Code, rec2.Body.String())
	}

	getReq := withURLParam(taskBundleRequestWithOrg(http.MethodGet, path, nil, "org-a"), "digest", digest)
	getRec := httptest.NewRecorder()
	h.Get(getRec, getReq)
	if getRec.Code != http.StatusOK || !bytes.Equal(getRec.Body.Bytes(), archive) {
		t.Fatalf("fetch = %d, %d bytes back, want %d", getRec.Code, getRec.Body.Len(), len(archive))
	}
}

func TestTaskBundleUploadRefusesDigestMismatch(t *testing.T) {
	h := newTaskBundleTestHandler(t)
	archive := validTestBundle(t)
	wrongDigest := "sha256:" + strings.Repeat("0", 64)

	req := withURLParam(taskBundleRequestWithOrg(http.MethodPost, "/api/task-bundles/"+wrongDigest, archive, "org-a"), "digest", wrongDigest)
	rec := httptest.NewRecorder()
	h.Upload(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("digest mismatch = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestTaskBundleUploadRefusesOversize(t *testing.T) {
	h := newTaskBundleTestHandler(t)
	body := bytes.Repeat([]byte("x"), taskbundle.MaxArchiveBytes+1)
	digest := taskbundle.DigestOf(body)

	req := withURLParam(taskBundleRequestWithOrg(http.MethodPost, "/api/task-bundles/"+digest, body, "org-a"), "digest", digest)
	rec := httptest.NewRecorder()
	h.Upload(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload = %d, want 413: %s", rec.Code, rec.Body.String())
	}
}

// TestTaskBundleUploadValidatesManifest is the new upload-time check: a
// digest-correct archive whose manifest is nonetheless invalid (here, an
// entry excluded from its own file list) must fail at upload, not wait
// for the first pipeline run to discover it — the same failure engine's
// TestTaskBundleEntryOutsideFileListFailsTheRun exercises at mount time.
func TestTaskBundleUploadValidatesManifest(t *testing.T) {
	h := newTaskBundleTestHandler(t)
	m := &taskbundle.Manifest{
		Format: taskbundle.Format, Language: "python",
		Entry: "tasks.py", Files: []string{"other.py"}, // lies: excludes its own entry
	}
	archive, err := taskbundle.Assemble(map[string]string{"tasks.py": "output_data = {}\n"}, m)
	if err != nil {
		t.Fatal(err)
	}
	digest := taskbundle.DigestOf(archive)

	req := withURLParam(taskBundleRequestWithOrg(http.MethodPost, "/api/task-bundles/"+digest, archive, "org-a"), "digest", digest)
	rec := httptest.NewRecorder()
	h.Upload(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid-manifest upload = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "manifest") {
		t.Fatalf("400 body does not explain the manifest failure: %s", rec.Body.String())
	}

	// Nothing should have persisted: a subsequent fetch for this org finds nothing.
	getReq := withURLParam(taskBundleRequestWithOrg(http.MethodGet, "/api/task-bundles/"+digest, nil, "org-a"), "digest", digest)
	getRec := httptest.NewRecorder()
	h.Get(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("a rejected upload's digest was fetchable: %d", getRec.Code)
	}
}

func TestTaskBundleFetchDoesNotCrossOrgBoundary(t *testing.T) {
	h := newTaskBundleTestHandler(t)
	archive := validTestBundle(t)
	digest := taskbundle.DigestOf(archive)

	upReq := withURLParam(taskBundleRequestWithOrg(http.MethodPost, "/api/task-bundles/"+digest, archive, "org-a"), "digest", digest)
	h.Upload(httptest.NewRecorder(), upReq)

	getReq := withURLParam(taskBundleRequestWithOrg(http.MethodGet, "/api/task-bundles/"+digest, nil, "org-b"), "digest", digest)
	getRec := httptest.NewRecorder()
	h.Get(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("org-b fetched org-a's bundle: %d", getRec.Code)
	}
}

// A genuine 409 collision (the store already holds different bytes at
// this exact digest) requires an actual SHA-256 collision, since the
// handler re-hashes the body before ever reaching the store — nothing
// a test can construct here. That path belongs to, and is already
// covered by, store/taskbundle_test.go and taskbundle_postgres_test.go,
// which exercise PutTaskBundle directly.
