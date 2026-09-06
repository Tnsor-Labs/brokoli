package api

// Tests for the task-bundle/v2 API (ADR-033) -- mirrors
// handlers_taskbundles_test.go's invariants for the v1 endpoints exactly
// (digest verification, manifest validity at upload, tenant scoping).

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
	"github.com/Tnsor-Labs/brokoli/store"
)

func newTaskBundleV2TestHandler(t *testing.T) *TaskBundleV2Handler {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "taskbundlesv2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return NewTaskBundleV2Handler(s)
}

func taskBundleV2RequestWithOrg(method, path string, body []byte, orgID string) *http.Request {
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
	}
	ctx := context.WithValue(r.Context(), OrgIDContextKey{}, orgID)
	return r.WithContext(ctx)
}

func validTestBundleV2(t *testing.T) []byte {
	t.Helper()
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	archive, err := taskbundlev2.Assemble(
		map[string]string{"fixture_task.py": "def run():\n    return 1\n"},
		&taskbundlev2.Manifest{
			Format:          taskbundlev2.Format,
			Name:            "fixture-task",
			InterfaceDigest: digest,
			SourceDigest:    digest,
			Payloads: []taskbundlev2.Payload{{
				ID:            "python-any",
				Runtime:       taskbundlev2.RuntimePython,
				OS:            "any",
				Arch:          "any",
				Entrypoint:    taskbundlev2.Entrypoint{Module: "fixture_task", Symbol: "run"},
				Effects:       taskbundlev2.EffectPure,
				PayloadDigest: digest,
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func TestTaskBundleV2UploadFetchRoundTrip(t *testing.T) {
	h := newTaskBundleV2TestHandler(t)
	archive := validTestBundleV2(t)
	digest := taskbundlev2.DigestOf(archive)
	path := "/api/task-bundles-v2/" + digest

	req := withURLParam(taskBundleV2RequestWithOrg(http.MethodPost, path, archive, "org-a"), "digest", digest)
	rec := httptest.NewRecorder()
	h.Upload(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first upload = %d, body %s", rec.Code, rec.Body.String())
	}

	req2 := withURLParam(taskBundleV2RequestWithOrg(http.MethodPost, path, archive, "org-a"), "digest", digest)
	rec2 := httptest.NewRecorder()
	h.Upload(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("re-upload = %d, want 200 unchanged: %s", rec2.Code, rec2.Body.String())
	}

	getReq := withURLParam(taskBundleV2RequestWithOrg(http.MethodGet, path, nil, "org-a"), "digest", digest)
	getRec := httptest.NewRecorder()
	h.Get(getRec, getReq)
	if getRec.Code != http.StatusOK || !bytes.Equal(getRec.Body.Bytes(), archive) {
		t.Fatalf("fetch = %d, %d bytes back, want %d", getRec.Code, getRec.Body.Len(), len(archive))
	}
}

func TestTaskBundleV2UploadRefusesDigestMismatch(t *testing.T) {
	h := newTaskBundleV2TestHandler(t)
	archive := validTestBundleV2(t)
	wrongDigest := "sha256:" + strings.Repeat("0", 64)

	req := withURLParam(taskBundleV2RequestWithOrg(http.MethodPost, "/api/task-bundles-v2/"+wrongDigest, archive, "org-a"), "digest", wrongDigest)
	rec := httptest.NewRecorder()
	h.Upload(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("digest mismatch = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestTaskBundleV2UploadRefusesOversize(t *testing.T) {
	h := newTaskBundleV2TestHandler(t)
	body := bytes.Repeat([]byte("x"), taskbundlev2.MaxArchiveBytes+1)
	digest := taskbundlev2.DigestOf(body)

	req := withURLParam(taskBundleV2RequestWithOrg(http.MethodPost, "/api/task-bundles-v2/"+digest, body, "org-a"), "digest", digest)
	rec := httptest.NewRecorder()
	h.Upload(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload = %d, want 413: %s", rec.Code, rec.Body.String())
	}
}

// TestTaskBundleV2UploadValidatesManifest exercises the stronger check
// v2's upload gets over v1's: a full Extract at upload time catches a
// content/digest mismatch, not just a structurally malformed manifest.
func TestTaskBundleV2UploadValidatesManifest(t *testing.T) {
	h := newTaskBundleV2TestHandler(t)
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	// A manifest declaring a file digest that does NOT match the actual
	// content -- Extract's own integrity check must catch this.
	archive, err := taskbundlev2.Assemble(
		map[string]string{"fixture_task.py": "def run():\n    return 1\n"},
		&taskbundlev2.Manifest{
			Format:          taskbundlev2.Format,
			Name:            "fixture-task",
			InterfaceDigest: digest,
			SourceDigest:    digest,
			Payloads: []taskbundlev2.Payload{{
				ID:            "python-any",
				Runtime:       taskbundlev2.RuntimePython,
				OS:            "any",
				Arch:          "any",
				Entrypoint:    taskbundlev2.Entrypoint{Module: "fixture_task", Symbol: "run"},
				Effects:       taskbundlev2.EffectPure,
				PayloadDigest: digest,
			}},
			Files: []taskbundlev2.FileEntry{
				{Path: "fixture_task.py", Size: 1, SHA256: strings.Repeat("0", 64)}, // lies
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	uploadDigest := taskbundlev2.DigestOf(archive)

	req := withURLParam(taskBundleV2RequestWithOrg(http.MethodPost, "/api/task-bundles-v2/"+uploadDigest, archive, "org-a"), "digest", uploadDigest)
	rec := httptest.NewRecorder()
	h.Upload(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid-manifest upload = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	getReq := withURLParam(taskBundleV2RequestWithOrg(http.MethodGet, "/api/task-bundles-v2/"+uploadDigest, nil, "org-a"), "digest", uploadDigest)
	getRec := httptest.NewRecorder()
	h.Get(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("a rejected upload's digest was fetchable: %d", getRec.Code)
	}
}

func TestTaskBundleV2FetchDoesNotCrossOrgBoundary(t *testing.T) {
	h := newTaskBundleV2TestHandler(t)
	archive := validTestBundleV2(t)
	digest := taskbundlev2.DigestOf(archive)

	upReq := withURLParam(taskBundleV2RequestWithOrg(http.MethodPost, "/api/task-bundles-v2/"+digest, archive, "org-a"), "digest", digest)
	h.Upload(httptest.NewRecorder(), upReq)

	getReq := withURLParam(taskBundleV2RequestWithOrg(http.MethodGet, "/api/task-bundles-v2/"+digest, nil, "org-b"), "digest", digest)
	getRec := httptest.NewRecorder()
	h.Get(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("org-b fetched org-a's bundle: %d", getRec.Code)
	}
}
