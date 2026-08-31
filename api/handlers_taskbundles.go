package api

// Task bundle API (ADR-031): the host's read/write endpoints for
// tenant-scoped, content-addressed project archives — the byte store a
// code node's task_bundle.digest reference resolves against at run time.
//
// Uploads are POST /api/task-bundles/{digest}: the digest is in the URL,
// not the body, so a client can never claim a digest it did not actually
// hash — the handler re-hashes the raw body and demands equality before a
// single byte is persisted (Decision 6's verify-on-upload half). The
// manifest is also parsed and validated at upload (format, language,
// entry, file-list consistency), so a malformed bundle fails where the
// author is looking, not at first pipeline run. A byte-identical
// re-upload is a no-op (201 then 200 unchanged); a different archive
// claiming an occupied digest is a 409 collision. All three checks must
// pass before a single byte is persisted.
//
// Fetch is GET /api/task-bundles/{digest}: raw bytes with an ETag equal
// to the digest, the same fetch-by-digest shape as the plugin archive
// endpoint — the pull path a worker would use on a remote engine.
//
// Permissioning: uploads are pipelines.create (authorship of artifacts
// is authorship of pipelines); fetches are auth-only, matching the
// plugin archive endpoint. Both are org-scoped by the same
// requirePipelineOrg semantics as pipeline authorship, and the store is
// keyed by (org_id, digest), so one tenant can never read or collide
// with another tenant's bundle even when both reference the same digest.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Tnsor-Labs/brokoli/pkg/taskbundle"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"
)

// TaskBundleHandler serves the task-bundle endpoints. It holds a
// store.Store and type-asserts the optional TaskBundleStore capability per
// request, so a hand-written store that has not adopted the capability yet
// degrades to a loud 503 rather than a panic or silent no-op.
type TaskBundleHandler struct {
	store store.Store
}

func NewTaskBundleHandler(s store.Store) *TaskBundleHandler {
	return &TaskBundleHandler{store: s}
}

// bundleStore resolves the optional TaskBundleStore capability, writing a
// 503 and returning nil when the store does not have it.
func (h *TaskBundleHandler) bundleStore(w http.ResponseWriter) store.TaskBundleStore {
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "task bundle storage is not available on this server")
		return nil
	}
	sb, ok := h.store.(store.TaskBundleStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "task bundle storage is not available on this server")
		return nil
	}
	return sb
}

func (h *TaskBundleHandler) uploadOrg(w http.ResponseWriter, r *http.Request) (string, bool) {
	orgID := GetOrgIDFromRequest(r)
	if orgID != "" {
		return orgID, true
	}
	if OrgResolverFunc != nil {
		writeError(w, http.StatusBadRequest,
			"cannot upload task bundle: user has no organization membership")
		return "", false
	}
	return "", true
}

// Upload accepts a task-bundle archive as the raw request body and stores
// it content-addressed under the URL's digest. See the file doc comment
// for the verify-before-persist contract.
func (h *TaskBundleHandler) Upload(w http.ResponseWriter, r *http.Request) {
	sb := h.bundleStore(w)
	if sb == nil {
		return
	}
	orgID, ok := h.uploadOrg(w, r)
	if !ok {
		return
	}
	digest := chi.URLParam(r, "digest")
	if !taskbundle.IsDigest(digest) {
		writeError(w, http.StatusBadRequest, "digest must be a content address of the form \"sha256:<64 hex chars>\"")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, taskbundle.MaxArchiveBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read uploaded archive")
		return
	}
	if len(body) > taskbundle.MaxArchiveBytes {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("task bundle exceeds the %d-byte limit", int64(taskbundle.MaxArchiveBytes)))
		return
	}

	actual := taskbundle.DigestOf(body)
	if actual != digest {
		writeError(w, http.StatusBadRequest,
			"archive hashes to "+actual+" but was claimed as "+digest)
		return
	}

	// Validate the manifest now, not just at first mount: "deployment
	// must fail... when bundling fails" (ADR-031 Decision 5) reads most
	// naturally as failing at the point the author is looking at —
	// upload — not at first pipeline run, which could be days later and
	// on someone else's screen. ParseArchive is bounded the same way
	// Extract is (entry count, per-entry, and aggregate decompressed
	// size), so this is safe to run on the unauthenticated-content
	// upload body before anything is persisted.
	if _, err := taskbundle.ParseArchive(body); err != nil {
		writeError(w, http.StatusBadRequest, "task bundle manifest is invalid: "+err.Error())
		return
	}

	created, err := sb.PutTaskBundle(orgID, digest, body)
	if err != nil {
		if errors.Is(err, store.ErrTaskBundleCollision) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	status := http.StatusOK
	state := "unchanged"
	if created {
		status = http.StatusCreated
		state = "stored"
	}
	writeJSON(w, status, map[string]interface{}{
		"status":     state,
		"digest":     digest,
		"format":     taskbundle.Format,
		"size_bytes": len(body),
	})
}

// Get serves the stored archive bytes for an org-scoped digest. The ETag
// is the digest itself, and If-None-Match short-circuits, so a re-fetch
// is a cheap 304 exactly when the digest already guarantees the bytes
// have not changed.
func (h *TaskBundleHandler) Get(w http.ResponseWriter, r *http.Request) {
	sb := h.bundleStore(w)
	if sb == nil {
		return
	}
	orgID := GetOrgIDFromRequest(r)
	digest := chi.URLParam(r, "digest")
	if !taskbundle.IsDigest(digest) {
		writeError(w, http.StatusBadRequest, "digest must be a content address of the form \"sha256:<64 hex chars>\"")
		return
	}
	archive, err := sb.GetTaskBundle(orgID, digest)
	if err != nil {
		if errors.Is(err, store.ErrTaskBundleNotFound) {
			writeError(w, http.StatusNotFound, "no task bundle with digest "+digest+" for this org")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-SHA256", digest)
	w.Header().Set("ETag", `"`+digest+`"`)
	if match := r.Header.Get("If-None-Match"); match == `"`+digest+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = io.Copy(w, bytes.NewReader(archive))
}
